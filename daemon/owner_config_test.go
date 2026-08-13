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

// TestChooseOwnerBaseFallsBack: the owner's state directory is chosen before
// the database is opened, so an unusable config directory killed the daemon at
// ownerSecret and no database fallback could ever be reached. The browser was
// told only that the desktop app was not running.
func TestChooseOwnerBaseFallsBack(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocker, []byte("a file where a folder must be"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", blocker)
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "local"))

	dir, err := chooseOwnerBase()
	if err != nil {
		t.Fatalf("no base chosen: %v", err)
	}
	if err := ownerDirUsable(dir); err != nil {
		t.Errorf("chosen base %s is not usable: %v", dir, err)
	}
	if dir == filepath.Join(blocker, "keel") {
		t.Errorf("chose the unusable directory %s", dir)
	}
}

// TestOwnerPathsKeepTheCorpusWithItsCredential: a fallback must move the
// database and the secret together, or the owner authenticates against one
// install while reading another's corpus.
func TestOwnerPathsKeepTheCorpusWithItsCredential(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KEEL_DATA_DIR", "")
	t.Setenv("KEEL_DB", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	// A Unix socket path is length-limited and the test temp root is long;
	// relocate the runtime dir so this exercises path *placement*, not sun_path.
	short, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(short)
	t.Setenv("KEEL_RUNTIME_DIR", short)

	p, err := resolveOwnerPaths()
	if err != nil {
		t.Fatal(err)
	}
	if p.dbPath == "" {
		t.Fatal("ownerPaths has no database path")
	}
	if filepath.Dir(p.dbPath) != p.configDir {
		t.Errorf("database %s is not beside the credential in %s", p.dbPath, p.configDir)
	}
	if filepath.Dir(p.secret) != p.configDir {
		t.Errorf("secret %s is not in the chosen directory %s", p.secret, p.configDir)
	}
}

// TestStateDirNeverCollidesWithTheManifestDir.
//
// ownerDirUsable applies a protected DACL, which strips inherited permissions.
// Pointed at %LOCALAPPDATA%\Keel — where the installer writes the native-host
// manifests — that locks the browser out of reading its own manifest, and the
// browser reports "Specified native messaging host not found". A fallback for
// Keel's private state must never select a directory a browser has to read.
func TestStateDirNeverCollidesWithTheManifestDir(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)

	manifestDir := filepath.Join(local, "Keel")
	for _, c := range ownerBaseCandidates() {
		if c == manifestDir {
			t.Fatalf("candidate %s is the native-host manifest directory", c)
		}
	}
}
