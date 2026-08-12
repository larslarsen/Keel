// SPDX-License-Identifier: Apache-2.0
package main

import (
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

const ownerIPCVersion = 1
const ownerHandshakeLimit = 16 << 10

type ownerHello struct {
	OwnerIPC     int    `json:"owner_ipc"`
	Secret       string `json:"secret"`
	Action       string `json:"action"`
	ProxyVersion string `json:"proxy_version"`
}

type ownerHelloAck struct {
	OwnerIPC     int    `json:"owner_ipc"`
	OwnerVersion string `json:"owner_version"`
	// OwnerBuild identifies the exact binary the owner is running. See
	// buildIdentity: the version string cannot answer this, because releases
	// deliberately reuse one version.
	OwnerBuild string `json:"owner_build,omitempty"`
	OK         bool   `json:"ok"`
	Code       string `json:"code,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// buildIdentity names the exact binary this process is running: its resolved
// path and modification time.
//
// Deliberately not the version string. Releases reuse one version on purpose,
// so version cannot distinguish a new build from an old one — and that is
// exactly the question that matters here. The owner outlives the browser, so
// after an upgrade the previous build can still be resident and answering while
// the new one sits unused on disk. Path plus mtime changes on every build and
// answers it directly.
//
// An empty result means "unknown": callers must not act on it.
func buildIdentity() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s@%d", exe, fi.ModTime().UnixNano())
}

// ownerIsStale reports whether a resident owner is running a different binary
// from this process, and should therefore be retired rather than talked to.
//
// An owner that reports no build at all predates this check, which makes it
// older than us by construction — that is the upgrade this was written for.
// If we cannot identify ourselves we never replace anything: an unknown state
// is not grounds for killing a working owner.
func ownerIsStale(ownerBuild string) bool {
	self := buildIdentity()
	if self == "" {
		return false
	}
	return ownerBuild != self
}

func readIPCFrame(r io.Reader, max uint32) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.NativeEndian.Uint32(lenBuf[:])
	if n == 0 || n > max {
		return nil, fmt.Errorf("invalid IPC frame size %d", n)
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}

func writeIPCFrame(w io.Writer, payload []byte, max uint32) error {
	if len(payload) == 0 || uint64(len(payload)) > uint64(max) {
		return fmt.Errorf("invalid IPC frame size %d", len(payload))
	}
	var lenBuf [4]byte
	binary.NativeEndian.PutUint32(lenBuf[:], uint32(len(payload)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func writeOwnerAck(conn net.Conn, ack ownerHelloAck) error {
	b, err := json.Marshal(ack)
	if err != nil {
		return err
	}
	return writeIPCFrame(conn, b, ownerHandshakeLimit)
}

// acceptOwnerHandshake authenticates before the caller touches application
// frames. It returns the requested action only after both the secret and the
// owner IPC schema revision match.
func acceptOwnerHandshake(conn net.Conn, secret string) (string, error) {
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	raw, err := readIPCFrame(conn, ownerHandshakeLimit)
	if err != nil {
		return "", err
	}
	var hello ownerHello
	if err := json.Unmarshal(raw, &hello); err != nil {
		_ = writeOwnerAck(conn, ownerHelloAck{OwnerIPC: ownerIPCVersion, OwnerVersion: version, OwnerBuild: buildIdentity(),
			Code: "bad_handshake", Reason: "invalid owner handshake"})
		return "", err
	}
	if subtle.ConstantTimeCompare([]byte(hello.Secret), []byte(secret)) != 1 {
		_ = writeOwnerAck(conn, ownerHelloAck{OwnerIPC: ownerIPCVersion, OwnerVersion: version, OwnerBuild: buildIdentity(),
			Code: "auth_failed", Reason: "local owner authentication failed"})
		return "", errors.New("owner authentication failed")
	}
	if hello.OwnerIPC != ownerIPCVersion {
		_ = writeOwnerAck(conn, ownerHelloAck{OwnerIPC: ownerIPCVersion, OwnerVersion: version, OwnerBuild: buildIdentity(),
			Code: "incompatible_owner_ipc", Reason: "desktop app components must be updated together"})
		return "", fmt.Errorf("owner IPC %d is incompatible with %d", hello.OwnerIPC, ownerIPCVersion)
	}
	if hello.Action != "session" && hello.Action != "shutdown" && hello.Action != "status" {
		_ = writeOwnerAck(conn, ownerHelloAck{OwnerIPC: ownerIPCVersion, OwnerVersion: version, OwnerBuild: buildIdentity(),
			Code: "bad_action", Reason: "unknown owner action"})
		return "", fmt.Errorf("unknown owner action %q", hello.Action)
	}
	if err := writeOwnerAck(conn, ownerHelloAck{OwnerIPC: ownerIPCVersion,
		OwnerVersion: version, OwnerBuild: buildIdentity(), OK: true}); err != nil {
		return "", err
	}
	_ = conn.SetDeadline(time.Time{})
	return hello.Action, nil
}

func startOwnerHandshake(conn net.Conn, secret, action string) (ownerHelloAck, error) {
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	hello := ownerHello{OwnerIPC: ownerIPCVersion, Secret: secret,
		Action: action, ProxyVersion: version}
	b, err := json.Marshal(hello)
	if err != nil {
		return ownerHelloAck{}, err
	}
	if err := writeIPCFrame(conn, b, ownerHandshakeLimit); err != nil {
		return ownerHelloAck{}, err
	}
	raw, err := readIPCFrame(conn, ownerHandshakeLimit)
	if err != nil {
		return ownerHelloAck{}, err
	}
	var ack ownerHelloAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		return ownerHelloAck{}, err
	}
	if !ack.OK {
		return ack, fmt.Errorf("%s: %s", ack.Code, ack.Reason)
	}
	if ack.OwnerIPC != ownerIPCVersion {
		return ack, fmt.Errorf("incompatible owner IPC %d", ack.OwnerIPC)
	}
	_ = conn.SetDeadline(time.Time{})
	return ack, nil
}
