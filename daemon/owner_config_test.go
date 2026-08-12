// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestOwnerSecretConcurrentCreationAndPermissions(t *testing.T) {
	dir := t.TempDir()
	p := ownerPaths{
		configDir:  dir,
		runtimeDir: filepath.Join(dir, "runtime"),
		secret:     filepath.Join(dir, "owner.secret"),
	}
	const clients = 20
	values := make(chan string, clients)
	errs := make(chan error, clients)
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := ownerSecret(p)
			values <- v
			errs <- err
		}()
	}
	wg.Wait()
	close(values)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var want string
	for got := range values {
		if want == "" {
			want = got
		}
		if got != want {
			t.Fatalf("concurrent callers got different secrets")
		}
	}
	fi, err := os.Stat(p.secret)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode = %o, want 600", fi.Mode().Perm())
	}
	fi, err = os.Stat(p.runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("runtime directory mode = %o", fi.Mode().Perm())
	}
}

func TestOwnerSecretRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.secret")
	if err := os.WriteFile(path, []byte("NmFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readOwnerSecret(path); err == nil {
		t.Fatal("broadly-readable owner secret was accepted")
	}
}
