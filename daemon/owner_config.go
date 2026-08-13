// SPDX-License-Identifier: Apache-2.0
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ownerPaths contains the per-install resources used to elect and authenticate
// the one process that owns SQLite and libp2p. KEEL_DB/KEEL_DATA_DIR are kept
// distinct for tests and developer profiles; a normal install has one identity.
type ownerPaths struct {
	configDir  string
	runtimeDir string
	endpoint   string
	guard      string
	secret     string
	log        string
	// dbPath keeps the corpus with the credential that guards it. Chosen here
	// rather than by the store so a fallback cannot separate them.
	dbPath string
}

// ownerDirUsable reports whether a directory can hold the owner's state.
//
// Deliberately does NOT apply the owner-only permissions. chooseOwnerBase calls
// this on candidates it may well not use, and secureOwnerPath sets a PROTECTED
// DACL — probing with it would permanently alter directories Keel then walks
// away from. That is not hypothetical: applying it to %LOCALAPPDATA%\Keel,
// where the installer keeps the native-host manifests, locked the browser out
// of reading its own manifest and native messaging stopped working. A probe
// must not mutate anything it is only asking about.
//
// Permissions are applied by prepareOwnerPaths, to the one directory chosen,
// and chooseOwnerBase verifies that step separately on that directory alone.
func ownerDirUsable(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	probe := filepath.Join(dir, ".keel-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("write to %s: %w", dir, err)
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// fileOpenable reports whether this process can actually read and write a file
// that already exists. os.Stat only proves something is there; on Windows a
// file can be present, in a perfectly writable directory, and still deny its
// own owner.
func fileOpenable(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

// ownerBaseCandidates lists directories that could hold Keel's state, in
// preference order, for the case where the user named none.
//
// The config directory is where Keel belongs. The rest exist because it is not
// always openable — a denied ACL, a redirected or roaming profile, a read-only
// volume — and the daemon dying at startup over it is a far worse outcome than
// living somewhere else. LOCALAPPDATA is second because the Windows installer
// already writes and verifies the native-host manifests there, which makes it
// known-good on exactly the machines where the config directory fails.
func ownerBaseCandidates() []string {
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		for _, e := range out {
			if e == p {
				return
			}
		}
		out = append(out, p)
	}
	if base, err := os.UserConfigDir(); err == nil {
		add(filepath.Join(base, "keel"))
	}
	if v := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); v != "" {
		// A "data" subfolder, never %LOCALAPPDATA%\Keel itself: that directory
		// holds the native-host manifests the installer writes, and
		// ownerDirUsable applies a PROTECTED DACL — which strips inherited
		// permissions and locks the browser out of reading its own manifest.
		// The symptom is "Specified native messaging host not found", i.e. this
		// fallback silently uninstalls native messaging. Keel's private state
		// and the files a browser must read cannot share a directory.
		add(filepath.Join(v, "Keel", "data"))
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Join(filepath.Dir(exe), "keel-data"))
	}
	add(filepath.Join(os.TempDir(), "keel"))
	return out
}

// chooseOwnerBase picks the first candidate that already holds state, else the
// first that is usable. Continuity first: a corpus must not be stranded because
// a directory that failed once is writable again now.
func chooseOwnerBase() (string, error) {
	candidates := ownerBaseCandidates()
	for _, dir := range candidates {
		db := filepath.Join(dir, "keel.sqlite")
		// Size, not existence. preflightDatabasePath creates the file before
		// SQLite ever opens it, so a location that failed *after* the probe
		// leaves a 0-byte keel.sqlite behind — and treating that as "a corpus
		// lives here" would pin every future start to the location that just
		// failed. An empty file is a scar, not a database.
		fi, err := os.Stat(db)
		if err != nil || fi.Size() == 0 {
			continue
		}
		// And openable, not merely present. A writable directory can hold a file
		// this user cannot open — the exact state a denied ACL on keel.sqlite
		// produces. Adopting that directory pins the daemon to a database it can
		// never read: the path becomes explicit from here on, so the store's own
		// fallback is switched off, and every start fails with "Access is
		// denied" on a file the daemon will not move away from. Prove it opens.
		if err := fileOpenable(db); err != nil {
			log.Printf("state directory: %s holds an unopenable database (%v); looking elsewhere", dir, err)
			continue
		}
		if err := ownerDirUsable(dir); err != nil {
			continue
		}
		if err := secureOwnerPath(dir, true); err != nil {
			continue
		}
		return dir, nil
	}
	var problems []string
	for _, dir := range candidates {
		// A directory being writable is not enough if the database already in it
		// cannot be opened: adopting it makes the path explicit, which switches
		// off the store's fallback, and every start then fails on a file the
		// daemon will never move away from. Checked here as well as in the
		// continuity pass above — skipping it there only to adopt it here is
		// exactly the bug this is meant to fix.
		if db := filepath.Join(dir, "keel.sqlite"); fileExists(db) {
			if err := fileOpenable(db); err != nil {
				problems = append(problems, fmt.Sprintf("%s holds a database that cannot be opened: %v", dir, err))
				continue
			}
		}
		if err := ownerDirUsable(dir); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		// Only now, on the directory actually being adopted, is the protected
		// DACL applied — and if it will not take, this candidate is rejected
		// rather than left half-secured.
		if err := secureOwnerPath(dir, true); err != nil {
			problems = append(problems, fmt.Sprintf("secure %s: %v", dir, err))
			continue
		}
		if len(problems) > 0 {
			log.Printf("state directory: falling back to %s (%s)", dir, strings.Join(problems, "; "))
		}
		return dir, nil
	}
	return "", fmt.Errorf("no writable location for Keel's data: %s", strings.Join(problems, "; "))
}

func resolveOwnerPaths() (ownerPaths, error) {
	configDir := os.Getenv("KEEL_DATA_DIR")
	dbIdentity := os.Getenv("KEEL_DB")
	if configDir == "" {
		if dbIdentity != "" {
			abs, err := filepath.Abs(dbIdentity)
			if err != nil {
				return ownerPaths{}, err
			}
			dbIdentity = abs
			configDir = filepath.Dir(abs)
		} else {
			base, err := chooseOwnerBase()
			if err != nil {
				return ownerPaths{}, err
			}
			configDir = base
			dbIdentity = filepath.Join(configDir, "keel.sqlite")
		}
	} else if dbIdentity == "" {
		dbIdentity = filepath.Join(configDir, "keel.sqlite")
	}
	configDir, err := filepath.Abs(configDir)
	if err != nil {
		return ownerPaths{}, err
	}
	sum := sha256.Sum256([]byte(dbIdentity))
	id := fmt.Sprintf("%x", sum[:6])

	runtimeDir := os.Getenv("KEEL_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(configDir, "runtime")
	}
	runtimeDir, err = filepath.Abs(runtimeDir)
	if err != nil {
		return ownerPaths{}, err
	}
	endpoint, err := ownerEndpoint(runtimeDir, id)
	if err != nil {
		return ownerPaths{}, err
	}
	return ownerPaths{
		configDir:  configDir,
		runtimeDir: runtimeDir,
		endpoint:   endpoint,
		guard:      filepath.Join(runtimeDir, "owner-"+id+".election"),
		secret:     filepath.Join(configDir, "owner-"+id+".secret"),
		log:        filepath.Join(configDir, "owner-"+id+".log"),
		dbPath:     dbIdentity,
	}, nil
}

func prepareOwnerPaths(p ownerPaths) error {
	for _, dir := range []string{p.configDir, p.runtimeDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := secureOwnerPath(dir, true); err != nil {
			return err
		}
	}
	return nil
}

// ownerSecret returns the installation secret, creating it without a
// check-then-create race. A contender that sees the file during its very short
// initial write retries instead of accepting an empty/partial credential.
func ownerSecret(p ownerPaths) (string, error) {
	if err := prepareOwnerPaths(p); err != nil {
		return "", err
	}
	for attempt := 0; attempt < 20; attempt++ {
		secret, err := readOwnerSecret(p.secret)
		if err == nil {
			return secret, nil
		}
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errSecretIncomplete) {
			return "", err
		}
		if errors.Is(err, fs.ErrNotExist) {
			var raw [32]byte
			if _, err := rand.Read(raw[:]); err != nil {
				return "", err
			}
			value := base64.RawURLEncoding.EncodeToString(raw[:])
			tmp, err := os.CreateTemp(p.configDir, ".owner-secret-*")
			if err != nil {
				return "", err
			}
			tmpName := tmp.Name()
			_ = tmp.Chmod(0o600)
			_, writeErr := tmp.WriteString(value + "\n")
			if writeErr == nil {
				writeErr = tmp.Sync()
			}
			closeErr := tmp.Close()
			if writeErr == nil {
				writeErr = closeErr
			}
			if writeErr == nil {
				writeErr = secureOwnerPath(tmpName, false)
			}
			if writeErr == nil {
				// Linking a complete temporary file gives us O_EXCL semantics
				// without ever exposing a partially-written final credential.
				writeErr = os.Link(tmpName, p.secret)
			}
			_ = os.Remove(tmpName)
			if writeErr == nil {
				if err := secureOwnerPath(p.secret, false); err != nil {
					return "", err
				}
				return value, nil
			}
			if !errors.Is(writeErr, fs.ErrExist) {
				return "", writeErr
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return "", fmt.Errorf("owner secret did not become readable")
}

var errSecretIncomplete = errors.New("owner secret incomplete")

func readOwnerSecret(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("owner secret is not a regular file")
	}
	if err := validateOwnerSecretPermissions(path, fi); err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(b))
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return "", errSecretIncomplete
	}
	return value, nil
}

func removeOwnerCredentials(p ownerPaths) error {
	err := os.Remove(p.secret)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
