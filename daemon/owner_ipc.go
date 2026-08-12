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
	OK           bool   `json:"ok"`
	Code         string `json:"code,omitempty"`
	Reason       string `json:"reason,omitempty"`
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
		_ = writeOwnerAck(conn, ownerHelloAck{OwnerIPC: ownerIPCVersion, OwnerVersion: version,
			Code: "bad_handshake", Reason: "invalid owner handshake"})
		return "", err
	}
	if subtle.ConstantTimeCompare([]byte(hello.Secret), []byte(secret)) != 1 {
		_ = writeOwnerAck(conn, ownerHelloAck{OwnerIPC: ownerIPCVersion, OwnerVersion: version,
			Code: "auth_failed", Reason: "local owner authentication failed"})
		return "", errors.New("owner authentication failed")
	}
	if hello.OwnerIPC != ownerIPCVersion {
		_ = writeOwnerAck(conn, ownerHelloAck{OwnerIPC: ownerIPCVersion, OwnerVersion: version,
			Code: "incompatible_owner_ipc", Reason: "desktop app components must be updated together"})
		return "", fmt.Errorf("owner IPC %d is incompatible with %d", hello.OwnerIPC, ownerIPCVersion)
	}
	if hello.Action != "session" && hello.Action != "shutdown" && hello.Action != "status" {
		_ = writeOwnerAck(conn, ownerHelloAck{OwnerIPC: ownerIPCVersion, OwnerVersion: version,
			Code: "bad_action", Reason: "unknown owner action"})
		return "", fmt.Errorf("unknown owner action %q", hello.Action)
	}
	if err := writeOwnerAck(conn, ownerHelloAck{OwnerIPC: ownerIPCVersion,
		OwnerVersion: version, OK: true}); err != nil {
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
