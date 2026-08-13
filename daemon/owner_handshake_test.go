// SPDX-License-Identifier: Apache-2.0
package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
)

// TestIsOwnerGoneErrorDistinguishesShutdownFromRefusal pins the
// classification connectOrSpawnOwner's race fix depends on (50ae9f9,
// reverted with no recorded reason by f2b8ce0, restored here after an
// adversarial re-read found the race still live in current code — see
// isOwnerGoneError's doc comment).
//
// Every install stops the previous owner before starting the next one, so a
// dial can land on an endpoint that is still technically open but closing
// underneath the handshake. That must read as "no owner, start one" — never
// as an incompatible owner, which must never be replaced or killed. Getting
// this classification wrong in either direction reintroduces a real bug:
// too broad, and a genuinely incompatible owner gets killed; too narrow, and
// every install-triggered shutdown fails the browser with a transport error
// it can do nothing about.
func TestIsOwnerGoneErrorDistinguishesShutdownFromRefusal(t *testing.T) {
	gone := []struct {
		name string
		err  error
	}{
		{"EOF", io.EOF},
		{"ErrUnexpectedEOF", io.ErrUnexpectedEOF},
		{"ErrClosedPipe", io.ErrClosedPipe},
		{"net.ErrClosed", net.ErrClosed},
		{"EPIPE", syscall.EPIPE},
		{"wrapped EOF", fmt.Errorf("read handshake: %w", io.EOF)},
		{"wrapped net.ErrClosed", fmt.Errorf("dial: %w", net.ErrClosed)},
	}
	for _, tc := range gone {
		if !isOwnerGoneError(tc.err) {
			t.Errorf("isOwnerGoneError(%s) = false, want true — a closing owner must be treated as no owner", tc.name)
		}
	}

	refused := []struct {
		name string
		err  error
	}{
		{"generic mismatch", errors.New("owner refused: incompatible protocol version")},
		{"plain nil-adjacent", errors.New("bad secret")},
	}
	for _, tc := range refused {
		if isOwnerGoneError(tc.err) {
			t.Errorf("isOwnerGoneError(%s) = true, want false — an incompatible owner must never be treated as gone and replaced", tc.name)
		}
	}
}
