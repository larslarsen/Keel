// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"net"
	"testing"
)

func TestOwnerHandshakeAuthenticatesAndNegotiates(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		action, err := acceptOwnerHandshake(server, "correct-secret")
		if err == nil && action != "session" {
			t.Errorf("action = %q, want session", action)
		}
		done <- err
	}()
	ack, err := startOwnerHandshake(client, "correct-secret", "session")
	if err != nil {
		t.Fatal(err)
	}
	if !ack.OK || ack.OwnerIPC != ownerIPCVersion {
		t.Fatalf("ack = %+v", ack)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOwnerHandshakeRejectsWrongSecret(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		_, err := acceptOwnerHandshake(server, "correct-secret")
		done <- err
	}()
	ack, err := startOwnerHandshake(client, "wrong-secret", "session")
	if err == nil {
		t.Fatal("wrong secret was accepted")
	}
	if ack.Code != "auth_failed" {
		t.Fatalf("code = %q, want auth_failed", ack.Code)
	}
	if err := <-done; err == nil {
		t.Fatal("server accepted wrong secret")
	}
}

func TestOwnerHandshakeRejectsIncompatibleRevision(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		_, err := acceptOwnerHandshake(server, "correct-secret")
		done <- err
	}()
	hello, err := json.Marshal(ownerHello{
		OwnerIPC: ownerIPCVersion + 1, Secret: "correct-secret",
		Action: "session", ProxyVersion: version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIPCFrame(client, hello, ownerHandshakeLimit); err != nil {
		t.Fatal(err)
	}
	raw, err := readIPCFrame(client, ownerHandshakeLimit)
	if err != nil {
		t.Fatal(err)
	}
	var ack ownerHelloAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.OK || ack.Code != "incompatible_owner_ipc" {
		t.Fatalf("ack = %+v", ack)
	}
	if err := <-done; err == nil {
		t.Fatal("server accepted incompatible revision")
	}
}

func TestOwnerIPCFrameBounds(t *testing.T) {
	var sink discardWriter
	if err := writeIPCFrame(&sink, make([]byte, ownerHandshakeLimit+1), ownerHandshakeLimit); err == nil {
		t.Fatal("oversized owner handshake frame was accepted")
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
