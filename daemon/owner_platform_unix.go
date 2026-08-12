// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var errOwnerAlreadyRunning = errors.New("owner already running")

func ownerEndpoint(runtimeDir, id string) (string, error) {
	p := filepath.Join(runtimeDir, "owner-"+id+".sock")
	// Darwin's sockaddr_un limit is 104 bytes; stay below the smallest target
	// limit rather than allowing a platform-dependent bind failure.
	if len(p) >= 100 {
		return "", fmt.Errorf("Keel runtime path is too long for a Unix socket: %s", p)
	}
	return p, nil
}

func secureOwnerPath(path string, dir bool) error {
	mode := fs.FileMode(0o600)
	if dir {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}

func validateOwnerSecretPermissions(_ string, fi fs.FileInfo) error {
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("owner secret permissions are %o, want 600", fi.Mode().Perm())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("owner secret is not owned by the current user")
	}
	return nil
}

type ownerUnixListener struct {
	*net.UnixListener
	path  string
	dev   uint64
	ino   uint64
	guard *os.File
}

func wrapOwnerUnixListener(ln *net.UnixListener, path string, guard *os.File) (*ownerUnixListener, error) {
	ln.SetUnlinkOnClose(false)
	fi, err := os.Lstat(path)
	if err != nil {
		_ = ln.Close()
		_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
		_ = guard.Close()
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		_ = ln.Close()
		_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
		_ = guard.Close()
		return nil, fmt.Errorf("cannot inspect owner socket")
	}
	return &ownerUnixListener{UnixListener: ln, path: path, dev: uint64(st.Dev), ino: st.Ino, guard: guard}, nil
}

func (l *ownerUnixListener) Close() error {
	err := l.UnixListener.Close()
	fi, statErr := os.Lstat(l.path)
	if statErr == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && uint64(st.Dev) == l.dev && st.Ino == l.ino {
			_ = os.Remove(l.path)
		}
	}
	if l.guard != nil {
		_ = unix.Flock(int(l.guard.Fd()), unix.LOCK_UN)
		_ = l.guard.Close()
		l.guard = nil
	}
	return err
}

// listenOwnerEndpoint makes the socket itself the service authority. The flock
// serializes election and stale-socket cleanup and remains held for the
// listener lifetime so a temporarily saturated socket cannot be unlinked. It
// is never used as a remotely reachable service endpoint.
func listenOwnerEndpoint(p ownerPaths) (net.Listener, error) {
	if err := prepareOwnerPaths(p); err != nil {
		return nil, err
	}
	guard, err := os.OpenFile(p.guard, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(p.guard, 0o600); err != nil {
		_ = guard.Close()
		return nil, err
	}
	// Keep the kernel-backed election guard for the listener lifetime. It is
	// not the service endpoint, but it prevents a contender from mistaking a
	// temporarily full/unresponsive active socket for a stale filesystem node.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = unix.Flock(int(guard.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = guard.Close()
			return nil, err
		}
		if conn, dialErr := net.DialTimeout("unix", p.endpoint, 100*time.Millisecond); dialErr == nil {
			_ = conn.Close()
			_ = guard.Close()
			return nil, errOwnerAlreadyRunning
		}
		if time.Now().After(deadline) {
			_ = guard.Close()
			return nil, errOwnerAlreadyRunning
		}
		time.Sleep(20 * time.Millisecond)
	}

	if conn, err := net.DialTimeout("unix", p.endpoint, 150*time.Millisecond); err == nil {
		_ = conn.Close()
		_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
		_ = guard.Close()
		return nil, errOwnerAlreadyRunning
	}
	fi, err := os.Lstat(p.endpoint)
	if err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
			_ = guard.Close()
			return nil, fmt.Errorf("refusing to remove non-socket owner endpoint %s", p.endpoint)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
			_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
			_ = guard.Close()
			return nil, fmt.Errorf("refusing to remove owner socket owned by another user")
		}
		if err := os.Remove(p.endpoint); err != nil {
			_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
			_ = guard.Close()
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
		_ = guard.Close()
		return nil, err
	}

	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: p.endpoint, Net: "unix"})
	if err != nil {
		if conn, dialErr := net.DialTimeout("unix", p.endpoint, 150*time.Millisecond); dialErr == nil {
			_ = conn.Close()
			_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
			_ = guard.Close()
			return nil, errOwnerAlreadyRunning
		}
		_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
		_ = guard.Close()
		return nil, err
	}
	if err := os.Chmod(p.endpoint, 0o600); err != nil {
		_ = ln.Close()
		_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
		_ = guard.Close()
		return nil, err
	}
	return wrapOwnerUnixListener(ln, p.endpoint, guard)
}

func dialOwnerEndpoint(ctx context.Context, p ownerPaths) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", p.endpoint)
}

func detachOwnerProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
