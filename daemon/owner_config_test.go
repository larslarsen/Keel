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

// TestProbeDoesNotSecureDirectoriesItRejects.
//
// chooseOwnerBase probes candidates it may not use. secureOwnerPath sets a
// PROTECTED DACL, which strips inherited permissions — so probing with it
// permanently alters directories Keel then walks away from. That is how the
// native-host manifest folder got locked and the browser lost the ability to
// read its own manifest. A probe must not mutate what it is only asking about.
func TestProbeDoesNotSecureDirectoriesItRejects(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "candidate")
	if err := ownerDirUsable(dir); err != nil {
		t.Fatalf("a writable candidate was rejected: %v", err)
	}
	// The probe may create the directory; it must not leave a probe file, and
	// on Unix it must not have tightened the mode beyond what MkdirAll set.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("probe left files behind: %v", entries)
	}
}

// TestUnopenableDatabaseIsNotAdopted is the live-QA dead end.
//
// The directory is writable and a keel.sqlite is sitting in it, so it looked
// like the corpus lived there and was adopted. But the file itself denies this
// user. From that point the database path is explicit, which switches off the
// store's own fallback, so every start failed on a file the daemon would never
// move away from — "Access is denied", forever, on a machine with three other
// perfectly good locations available.
func TestUnopenableDatabaseIsNotAdopted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open anything")
	}
	root := t.TempDir()

	// A writable directory holding a database this user cannot open. It has to
	// be the directory the candidate list actually names — <config>/keel —
	// or the test proves nothing.
	cfg := filepath.Join(root, "cfg")
	poisoned := filepath.Join(cfg, "keel")
	if err := os.MkdirAll(poisoned, 0o700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(poisoned, "keel.sqlite")
	if err := os.WriteFile(db, []byte("SQLite format 3\x00 pretend contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(db, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(db, 0o600) })

	good := filepath.Join(root, "good")
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("LOCALAPPDATA", good)
	t.Setenv("KEEL_DATA_DIR", "")
	t.Setenv("KEEL_DB", "")

	// The poisoned directory must not be adopted just because a file is there.
	if err := fileOpenable(db); err == nil {
		t.Fatal("setup: the database is still openable")
	}
	dir, err := chooseOwnerBase()
	if err != nil {
		t.Fatalf("no base chosen at all: %v", err)
	}
	if dir == poisoned {
		t.Fatalf("adopted %s, whose database cannot be opened", dir)
	}
	if err := ownerDirUsable(dir); err != nil {
		t.Errorf("chosen base %s is not usable: %v", dir, err)
	}
}

// TestDeniedSecretIsAlsoADeadEnd. Checking only keel.sqlite was too narrow:
// the same directory holds owner-<id>.secret, which is read BEFORE the store is
// ever opened, and a denied secret fails at the same point with the same
// message. Any of Keel's own files being unopenable makes the directory a dead
// end.
func TestDeniedSecretIsAlsoADeadEnd(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open anything")
	}
	root := t.TempDir()
	cfg := filepath.Join(root, "cfg")
	poisoned := filepath.Join(cfg, "keel")
	if err := os.MkdirAll(poisoned, 0o700); err != nil {
		t.Fatal(err)
	}
	// No database at all — only a credential this user cannot read.
	secret := filepath.Join(poisoned, "owner-abc123.secret")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secret, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(secret, 0o600) })

	if err := ownerFilesOpenable(poisoned); err == nil {
		t.Fatal("a denied secret was reported as fine")
	}

	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "local"))
	t.Setenv("KEEL_DATA_DIR", "")
	t.Setenv("KEEL_DB", "")

	dir, err := chooseOwnerBase()
	if err != nil {
		t.Fatalf("no base chosen: %v", err)
	}
	if dir == poisoned {
		t.Fatal("adopted a directory whose credential cannot be read")
	}
}

// A directory with no Keel files in it is fine, not suspicious.
func TestEmptyDirectoryIsOpenable(t *testing.T) {
	if err := ownerFilesOpenable(t.TempDir()); err != nil {
		t.Errorf("an empty directory was rejected: %v", err)
	}
}
