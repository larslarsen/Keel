// SPDX-License-Identifier: Apache-2.0
//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var errOwnerAlreadyRunning = errors.New("owner already running")

func currentUserSID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	u, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid.Copy()
}

func currentUserSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	return windows.SecurityDescriptorFromString(ownerSDDL(sid.String(), false))
}

// currentUserDirectorySecurityDescriptor is the same grant, made inheritable.
func currentUserDirectorySecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	return windows.SecurityDescriptorFromString(ownerSDDL(sid.String(), true))
}

func ownerEndpoint(_ string, id string) (string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return "", err
	}
	return `\\.\pipe\keel-owner-` + sid.String() + "-" + id, nil
}

// secureOwnerPath locks a path to the current user. isDir matters: see
// ownerSDDL. It was accepted and ignored here, which is the whole reason a
// file inside Keel's own directory could end up unopenable by its own daemon.
func secureOwnerPath(path string, isDir bool) error {
	sd, err := currentUserSecurityDescriptor()
	if isDir {
		sd, err = currentUserDirectorySecurityDescriptor()
	}
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

func validateOwnerSecretPermissions(path string, _ fs.FileInfo) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("owner secret DACL is not protected")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || dacl.AceCount != 1 {
		return errors.New("owner secret must grant access only to the current user")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return err
	}
	if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		!grantsFullControl(uint32(ace.Mask)) {
		return fmt.Errorf("owner secret has an unexpected access rule; "+
			"delete %s and run the installer again to recreate it", path)
	}
	current, err := currentUserSID()
	if err != nil {
		return err
	}
	granted := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !granted.IsValid() || !granted.Equals(current) {
		return errors.New("owner secret grants access to a different user")
	}
	return nil
}

type pipeAddr string

func (a pipeAddr) Network() string { return "named-pipe" }
func (a pipeAddr) String() string  { return string(a) }

// overlappedIO performs one read or write on an overlapped handle and waits for
// it, using an event of its own.
//
// Each call gets its own OVERLAPPED and event, which is what lets a read and a
// write proceed at the same time on one handle — the property the proxy depends
// on and a synchronous handle does not have.
func overlappedIO(h windows.Handle, p []byte, write bool) (uint32, error) {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(ev)
	ov := &windows.Overlapped{HEvent: ev}

	var done uint32
	if write {
		err = windows.WriteFile(h, p, &done, ov)
	} else {
		err = windows.ReadFile(h, p, &done, ov)
	}
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		return done, err
	}
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		if err := windows.GetOverlappedResult(h, ov, &done, true); err != nil {
			return done, err
		}
	}
	return done, nil
}

// connectOverlapped waits for a client on an overlapped listening handle.
func connectOverlapped(h windows.Handle) error {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ev)
	ov := &windows.Overlapped{HEvent: ev}

	err = windows.ConnectNamedPipe(h, ov)
	if err == nil || errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		return nil
	}
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		return err
	}
	var done uint32
	if err := windows.GetOverlappedResult(h, ov, &done, true); err != nil {
		if errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			return nil
		}
		return err
	}
	return nil
}

// pipeConn is a net.Conn over an OVERLAPPED named-pipe handle.
//
// Two things about this were wrong in turn, and both presented to the user as
// "the desktop app is not running".
//
// It began as os.NewFile(handle) in a struct. os.NewFile registers the handle
// with the Go runtime's I/O completion port, which requires a handle opened for
// overlapped I/O — and the pipe was created synchronous, so the very first read
// failed with "The handle is invalid" and the owner dropped every client.
//
// Reading the handle directly fixed that and introduced the next failure:
// Windows serializes I/O on a SYNCHRONOUS handle, so a blocking ReadFile holds
// it and a concurrent WriteFile queues behind. The proxy runs exactly that
// shape — one goroutine copying browser→owner, another copying owner→browser —
// so the browser's first frame was never written, the owner never answered, and
// the client timed out with nothing in any log.
//
// So: overlapped handles, and each read and write carries its own OVERLAPPED
// and event. That is what makes a read and a write independent on one handle,
// which is the property the proxy requires.
type pipeConn struct {
	h    windows.Handle
	addr pipeAddr

	mu     sync.Mutex
	closed bool
}

func (c *pipeConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, err := overlappedIO(c.h, p, false)
	if err != nil {
		// A peer that hung up is EOF, not a fault: the proxy exits when the
		// browser closes the port, and that is the ordinary end of a session.
		if errors.Is(err, windows.ERROR_BROKEN_PIPE) ||
			errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) ||
			errors.Is(err, windows.ERROR_NO_DATA) {
			return int(n), io.EOF
		}
		return int(n), err
	}
	if n == 0 {
		return 0, io.EOF
	}
	return int(n), nil
}

func (c *pipeConn) Write(p []byte) (int, error) {
	total := 0
	for total < len(p) {
		n, err := overlappedIO(c.h, p[total:], true)
		if err != nil {
			if errors.Is(err, windows.ERROR_BROKEN_PIPE) ||
				errors.Is(err, windows.ERROR_NO_DATA) {
				return total, io.ErrClosedPipe
			}
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
		total += int(n)
	}
	return total, nil
}

func (c *pipeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return windows.CloseHandle(c.h)
}

func (c *pipeConn) LocalAddr() net.Addr  { return c.addr }
func (c *pipeConn) RemoteAddr() net.Addr { return c.addr }

// Deadlines would need CancelIoEx wired into every pending operation; they are
// reported unsupported rather than
// silently accepted, so nothing can come to depend on a timer that will never
// fire; every caller here already ignores the result. Sessions are one
// goroutine each, so a peer that never speaks blocks only itself.
func (c *pipeConn) SetDeadline(time.Time) error      { return os.ErrNoDeadline }
func (c *pipeConn) SetReadDeadline(time.Time) error  { return os.ErrNoDeadline }
func (c *pipeConn) SetWriteDeadline(time.Time) error { return os.ErrNoDeadline }

func newPipeInstance(name string, first bool) (windows.Handle, error) {
	sd, err := currentUserSecurityDescriptor()
	if err != nil {
		return windows.InvalidHandle, err
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	// FILE_FLAG_OVERLAPPED is not optional here. A synchronous handle serializes
	// I/O: a blocking ReadFile holds the handle, and a concurrent WriteFile on
	// the same handle waits behind it. The proxy runs exactly that pattern — one
	// goroutine copying browser→owner, another copying owner→browser — so on a
	// synchronous pipe the browser's first frame is never written, the owner
	// never answers, and the panel reports the desktop app as not running.
	flags := uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED)
	if first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	return windows.CreateNamedPipe(windows.StringToUTF16Ptr(name), flags,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
		windows.PIPE_UNLIMITED_INSTANCES, 64<<10, 64<<10, 5000, sa)
}

type ownerPipeListener struct {
	mu         sync.Mutex
	name       string
	pending    windows.Handle
	connecting windows.Handle
	closed     chan struct{}
}

func listenOwnerEndpoint(p ownerPaths) (net.Listener, error) {
	if err := prepareOwnerPaths(p); err != nil {
		return nil, err
	}
	h, err := newPipeInstance(p.endpoint, true)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, errOwnerAlreadyRunning
		}
		return nil, err
	}
	return &ownerPipeListener{name: p.endpoint, pending: h,
		connecting: windows.InvalidHandle, closed: make(chan struct{})}, nil
}

func (l *ownerPipeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	select {
	case <-l.closed:
		l.mu.Unlock()
		return nil, net.ErrClosed
	default:
	}
	h := l.pending
	if h == windows.InvalidHandle {
		var err error
		h, err = newPipeInstance(l.name, false)
		if err != nil {
			l.mu.Unlock()
			return nil, err
		}
	}
	l.pending = windows.InvalidHandle
	l.connecting = h
	l.mu.Unlock()
	err := connectOverlapped(h)
	l.mu.Lock()
	if l.connecting == h {
		l.connecting = windows.InvalidHandle
	}
	l.mu.Unlock()
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	return &pipeConn{h: h, addr: pipeAddr(l.name)}, nil
}

func (l *ownerPipeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.closed:
		return net.ErrClosed
	default:
		close(l.closed)
	}
	if l.connecting != windows.InvalidHandle {
		_ = windows.CloseHandle(l.connecting)
		l.connecting = windows.InvalidHandle
	}
	if l.pending != windows.InvalidHandle {
		err := windows.CloseHandle(l.pending)
		l.pending = windows.InvalidHandle
		return err
	}
	return nil
}

func (l *ownerPipeListener) Addr() net.Addr { return pipeAddr(l.name) }

func dialOwnerEndpoint(ctx context.Context, p ownerPaths) (net.Conn, error) {
	name := windows.StringToUTF16Ptr(p.endpoint)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		// Overlapped on this side too: the proxy reads and writes this handle
		// from two goroutines at once.
		h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
			0, nil, windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			// Same rule as the listener: keep the handle out of the Go
			// runtime's poller. The client opens a synchronous handle too, and
			// os.NewFile would break it identically.
			return &pipeConn{h: h, addr: pipeAddr(p.endpoint)}, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func detachOwnerProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP |
		windows.DETACHED_PROCESS | windows.CREATE_NO_WINDOW}
}
