// SPDX-License-Identifier: Apache-2.0
//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
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
	return windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + sid.String() + ")")
}

func ownerEndpoint(_ string, id string) (string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return "", err
	}
	return `\\.\pipe\keel-owner-` + sid.String() + "-" + id, nil
}

func secureOwnerPath(path string, _ bool) error {
	sd, err := currentUserSecurityDescriptor()
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

type pipeConn struct {
	*os.File
	addr pipeAddr
}

func (c *pipeConn) LocalAddr() net.Addr                { return c.addr }
func (c *pipeConn) RemoteAddr() net.Addr               { return c.addr }
func (c *pipeConn) SetDeadline(t time.Time) error      { return c.File.SetDeadline(t) }
func (c *pipeConn) SetReadDeadline(t time.Time) error  { return c.File.SetReadDeadline(t) }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { return c.File.SetWriteDeadline(t) }

func newPipeInstance(name string, first bool) (windows.Handle, error) {
	sd, err := currentUserSecurityDescriptor()
	if err != nil {
		return windows.InvalidHandle, err
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	flags := uint32(windows.PIPE_ACCESS_DUPLEX)
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
	err := windows.ConnectNamedPipe(h, nil)
	l.mu.Lock()
	if l.connecting == h {
		l.connecting = windows.InvalidHandle
	}
	l.mu.Unlock()
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	return &pipeConn{File: os.NewFile(uintptr(h), l.name), addr: pipeAddr(l.name)}, nil
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
		h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
			0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			return &pipeConn{File: os.NewFile(uintptr(h), p.endpoint), addr: pipeAddr(p.endpoint)}, nil
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
