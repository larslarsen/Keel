// SPDX-License-Identifier: Apache-2.0
// WO-091: registry writes are verified, and a failed verification fails the
// install. Exercised through the injected runner, so no registry is needed.
package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeRegistry is a registry the tests can break on purpose.
type fakeRegistry struct {
	values  map[string]string // key -> default value
	addErr  map[string]error  // keys whose `reg add` fails
	dropAdd map[string]bool   // keys whose `reg add` reports success but stores nothing
	stored  map[string]string // keys whose stored value differs from what was asked
	calls   []string
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		values:  map[string]string{},
		addErr:  map[string]error{},
		dropAdd: map[string]bool{},
		stored:  map[string]string{},
	}
}

func (f *fakeRegistry) run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name != "reg" || len(args) < 2 {
		return nil, fmt.Errorf("unexpected command %q", name)
	}
	key := args[1]
	switch args[0] {
	case "add":
		if err := f.addErr[key]; err != nil {
			return []byte("ERROR: Access is denied."), err
		}
		if f.dropAdd[key] {
			return []byte("The operation completed successfully."), nil
		}
		v := args[len(args)-2] // ... /d <value> /f
		if alt, ok := f.stored[key]; ok {
			v = alt
		}
		f.values[key] = v
		return []byte("The operation completed successfully."), nil
	case "query":
		v, ok := f.values[key]
		if !ok {
			return []byte("ERROR: The system was unable to find the specified registry key or value."),
				errors.New("exit status 1")
		}
		// Windows prints the key, then the value under a localized name.
		return []byte("\r\n" + strings.ToUpper(key) + "\r\n    (Default)    REG_SZ    " + v + "\r\n\r\n"), nil
	case "delete":
		if _, ok := f.values[key]; !ok {
			return []byte("ERROR: The system was unable to find the specified registry key or value."),
				errors.New("exit status 1")
		}
		delete(f.values, key)
		return []byte("The operation completed successfully."), nil
	}
	return nil, fmt.Errorf("unexpected reg verb %q", args[0])
}

const (
	chromiumManifestPath = `C:\Users\qa\AppData\Local\Keel\chromium\com.keel.host.json`
	firefoxManifestPath  = `C:\Users\qa\AppData\Local\Keel\firefox\com.keel.host.json`
)

func braveKey() string {
	for _, t := range windowsRegistryTargets() {
		if t.browser == "Brave" {
			return t.key
		}
	}
	panic("no Brave registry target")
}

func TestRegistryInstallPointsEachFamilyAtItsOwnManifest(t *testing.T) {
	f := newFakeRegistry()
	results := installWindowsRegistry(f.run, chromiumManifestPath, firefoxManifestPath)

	if len(results) != len(windowsRegistryTargets()) {
		t.Fatalf("got %d results, want one per supported browser key", len(results))
	}
	for _, r := range results {
		if r.err != nil {
			t.Errorf("%s: %v", r.browser, r.err)
			continue
		}
		want := chromiumManifestPath
		if r.browser == "Firefox" {
			want = firefoxManifestPath
		}
		if normalizeWindowsPath(r.observed) != normalizeWindowsPath(want) {
			t.Errorf("%s key holds %q, want %q", r.browser, r.observed, want)
		}
		if r.expected != want {
			t.Errorf("%s expected %q, want %q", r.browser, r.expected, want)
		}
	}
	if got := f.values[braveKey()]; got != chromiumManifestPath {
		t.Errorf("Brave key = %q, want the Chromium manifest", got)
	}
}

// TestRegistryInstallFailsClosed covers every way the chain can be broken:
// the write fails, the key is missing afterwards, or it holds something else.
// None of them may be reported as a successful install.
func TestRegistryInstallFailsClosed(t *testing.T) {
	t.Run("reg add fails", func(t *testing.T) {
		f := newFakeRegistry()
		f.addErr[braveKey()] = errors.New("exit status 1")
		assertOnlyBraveFails(t, installWindowsRegistry(f.run, chromiumManifestPath, firefoxManifestPath))
	})

	t.Run("key missing after a successful-looking add", func(t *testing.T) {
		f := newFakeRegistry()
		f.dropAdd[braveKey()] = true
		assertOnlyBraveFails(t, installWindowsRegistry(f.run, chromiumManifestPath, firefoxManifestPath))
	})

	t.Run("key holds the wrong path", func(t *testing.T) {
		f := newFakeRegistry()
		f.stored[braveKey()] = `C:\stale\com.keel.host.json`
		results := installWindowsRegistry(f.run, chromiumManifestPath, firefoxManifestPath)
		assertOnlyBraveFails(t, results)
		for _, r := range results {
			if r.browser == "Brave" && r.observed != `C:\stale\com.keel.host.json` {
				t.Errorf("observed value not reported: %q", r.observed)
			}
		}
	})

	t.Run("no manifest was written for a family", func(t *testing.T) {
		results := installWindowsRegistry(newFakeRegistry().run, chromiumManifestPath, "")
		for _, r := range results {
			if r.browser == "Firefox" && r.err == nil {
				t.Error("Firefox registered although no Firefox manifest exists")
			}
			if r.browser != "Firefox" && r.err != nil {
				t.Errorf("%s: %v", r.browser, r.err)
			}
		}
	})
}

func assertOnlyBraveFails(t *testing.T, results []registryResult) {
	t.Helper()
	for _, r := range results {
		if r.browser == "Brave" {
			if r.err == nil {
				t.Error("Brave registration reported success")
			}
			continue
		}
		if r.err != nil {
			t.Errorf("%s: %v", r.browser, r.err)
		}
	}
}

// TestRegQueryDefaultToleratesLocalizedOutput: the value name is translated on a
// localized Windows, and the path can contain spaces, so neither the name nor
// whitespace splitting can be used to find the value.
func TestRegQueryDefaultToleratesLocalizedOutput(t *testing.T) {
	cases := map[string]struct {
		out  string
		want string
	}{
		"english": {"\r\nHKEY_CURRENT_USER\\Software\\X\r\n    (Default)    REG_SZ    C:\\Keel\\h.json\r\n", `C:\Keel\h.json`},
		"german":  {"\r\n    (Standard)    REG_SZ    C:\\Keel\\h.json\r\n", `C:\Keel\h.json`},
		"french":  {"\r\n    (par défaut)    REG_SZ    C:\\Keel\\h.json\r\n", `C:\Keel\h.json`},
		"spaces":  {"    (Default)    REG_SZ    C:\\Program Files\\Keel App\\h.json\r\n", `C:\Program Files\Keel App\h.json`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := regQueryDefault([]byte(c.out))
			if !ok || got != c.want {
				t.Fatalf("regQueryDefault = %q, %v; want %q, true", got, ok, c.want)
			}
		})
	}
	if _, ok := regQueryDefault([]byte("ERROR: The system was unable to find the specified registry key")); ok {
		t.Error("an error message was parsed as a value")
	}
}

func TestNormalizeWindowsPath(t *testing.T) {
	same := [][2]string{
		{`C:\Keel\h.json`, `c:\keel\H.JSON`},
		{`C:\Keel\h.json`, `C:/Keel/h.json`},
		{`C:\Keel\h.json`, `"C:\Keel\h.json"`},
		{`C:\Keel\h.json`, `C:\\Keel\\h.json`},
		{`C:\Keel`, `C:\Keel\`},
	}
	for _, c := range same {
		if normalizeWindowsPath(c[0]) != normalizeWindowsPath(c[1]) {
			t.Errorf("%q and %q should compare equal", c[0], c[1])
		}
	}
	if normalizeWindowsPath(`C:\Keel\chromium\h.json`) == normalizeWindowsPath(`C:\Keel\firefox\h.json`) {
		t.Error("the two schema directories must not compare equal")
	}
	if got := normalizeWindowsPath(`\\server\share\h.json`); got != `\\server\share\h.json` {
		t.Errorf("UNC prefix lost: %q", got)
	}
}

func TestUninstallRemovesEveryOwnedKey(t *testing.T) {
	f := newFakeRegistry()
	installWindowsRegistry(f.run, chromiumManifestPath, firefoxManifestPath)
	if len(f.values) != len(windowsRegistryTargets()) {
		t.Fatalf("setup: %d keys, want %d", len(f.values), len(windowsRegistryTargets()))
	}
	for _, r := range uninstallWindowsRegistry(f.run) {
		if r.err != nil {
			t.Errorf("%s: %v", r.browser, r.err)
		}
	}
	if len(f.values) != 0 {
		t.Errorf("keys left behind: %v", f.values)
	}
	// Uninstalling twice is not an error: absent is the desired state.
	for _, r := range uninstallWindowsRegistry(f.run) {
		if r.err != nil {
			t.Errorf("second uninstall, %s: %v", r.browser, r.err)
		}
	}
}

func TestUninstallReportsAKeyItCannotRemove(t *testing.T) {
	f := newFakeRegistry()
	installWindowsRegistry(f.run, chromiumManifestPath, firefoxManifestPath)
	stubborn := braveKey()
	run := func(name string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "delete" && args[1] == stubborn {
			return []byte("ERROR: Access is denied."), errors.New("exit status 1")
		}
		return f.run(name, args...)
	}
	assertOnlyBraveFails(t, uninstallWindowsRegistry(run))
}

// TestRepairManifestAccess: a browser reads com.keel.host.json itself, so
// anything that strips inherited permissions from that directory uninstalls
// native messaging without touching the registry — and reports exactly what a
// missing key reports. An earlier Keel build did this to its own manifest
// directory, so the installer tries to repair it on every run.
func TestRepairManifestAccess(t *testing.T) {
	const dir = `C:\Users\qa\AppData\Local\Keel`

	t.Run("resets inherited permissions", func(t *testing.T) {
		var calls []string
		run := func(name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return []byte("Successfully processed 3 files."), nil
		}
		if err := repairManifestAccess(run, dir); err != nil {
			t.Fatalf("repair: %v", err)
		}
		if len(calls) != 1 {
			t.Fatalf("want one command, got %v", calls)
		}
		for _, want := range []string{"icacls", dir, "/reset", "/t"} {
			if !strings.Contains(calls[0], want) {
				t.Errorf("command %q is missing %q", calls[0], want)
			}
		}
	})

	// /reset rewrites the ACL wholesale and needs WRITE_DAC on every child. A
	// narrower grant to the current user often succeeds where that does not, and
	// read access for this user is all that is actually required.
	t.Run("falls back to a narrower grant", func(t *testing.T) {
		t.Setenv("USERNAME", "qa")
		t.Setenv("USERDOMAIN", "PC")
		var calls []string
		run := func(name string, args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(args, " "))
			if len(args) > 1 && args[1] == "/reset" {
				return []byte("Access is denied."), errors.New("exit status 5")
			}
			return []byte("Successfully processed 3 files."), nil
		}
		if err := repairManifestAccess(run, dir); err != nil {
			t.Fatalf("the grant fallback did not run or did not succeed: %v", err)
		}
		if len(calls) != 2 {
			t.Fatalf("want reset then grant, got %v", calls)
		}
		if !strings.Contains(calls[1], "/grant") || !strings.Contains(calls[1], `PC\qa`) {
			t.Errorf("second attempt is not a grant to the current user: %q", calls[1])
		}
	})

	// A DACL Keel itself applied can leave the directory in a state where even
	// its owner cannot rewrite the ACL. Taking ownership back is the only way
	// out, and it only ever runs against a directory Keel created and broke.
	t.Run("takes ownership when both icacls attempts are denied", func(t *testing.T) {
		t.Setenv("USERNAME", "qa")
		t.Setenv("USERDOMAIN", "PC")
		var calls []string
		run := func(name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if name == "icacls" && len(calls) < 3 {
				return []byte("Access is denied."), errors.New("exit status 5")
			}
			return []byte("SUCCESS"), nil
		}
		if err := repairManifestAccess(run, dir); err != nil {
			t.Fatalf("the takeown path did not recover: %v", err)
		}
		if len(calls) != 4 {
			t.Fatalf("want reset, grant, takeown, reset; got %v", calls)
		}
		if !strings.HasPrefix(calls[2], "takeown ") {
			t.Errorf("third attempt is not takeown: %q", calls[2])
		}
	})

	t.Run("reports every failure without inventing success", func(t *testing.T) {
		t.Setenv("USERNAME", "qa")
		t.Setenv("USERDOMAIN", "")
		failing := func(string, ...string) ([]byte, error) {
			return []byte("Access is denied."), errors.New("exit status 5")
		}
		err := repairManifestAccess(failing, dir)
		if err == nil {
			t.Fatal("a failed repair was reported as success")
		}
		if !strings.Contains(err.Error(), dir) {
			t.Errorf("error does not name the directory: %v", err)
		}
	})

	t.Run("invents no command for an empty path", func(t *testing.T) {
		var calls []string
		run := func(name string, args ...string) ([]byte, error) {
			calls = append(calls, name)
			return nil, nil
		}
		if err := repairManifestAccess(run, ""); err != nil {
			t.Errorf("empty dir: %v", err)
		}
		if len(calls) != 0 {
			t.Errorf("ran %v for an empty directory", calls)
		}
	})
}

// TestEveryChannelRootIsRegistered.
//
// Each release channel is a separate product with its own registry root, and a
// browser reads only its own. Registering just the stable roots lets an install
// verify perfectly while the browser in front of the user looks elsewhere and
// reports "Specified native messaging host not found" — a failure that names
// neither the channel nor the key.
func TestEveryChannelRootIsRegistered(t *testing.T) {
	targets := windowsRegistryTargets()
	want := []string{
		`Google\Chrome\`, `Google\Chrome Beta\`, `Google\Chrome Dev\`, `Google\Chrome SxS\`,
		`Chromium\`,
		`BraveSoftware\Brave-Browser\`, `BraveSoftware\Brave-Browser-Beta\`,
		`BraveSoftware\Brave-Browser-Nightly\`, `BraveSoftware\Brave-Browser-Dev\`,
		`Microsoft\Edge\`, `Microsoft\Edge Beta\`, `Microsoft\Edge Dev\`, `Microsoft\Edge SxS\`,
		`Mozilla\`,
	}
	for _, w := range want {
		found := false
		for _, tg := range targets {
			if strings.Contains(tg.key, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no registry key under %s", w)
		}
	}
	// Every key must be HKCU (no admin rights) and name the host exactly once.
	seen := map[string]bool{}
	for _, tg := range targets {
		if !strings.HasPrefix(tg.key, `HKCU\Software\`) {
			t.Errorf("%s is not under HKCU\\Software: %s", tg.browser, tg.key)
		}
		if !strings.HasSuffix(tg.key, `\NativeMessagingHosts\`+hostName) {
			t.Errorf("%s key has the wrong shape: %s", tg.browser, tg.key)
		}
		if seen[tg.key] {
			t.Errorf("duplicate key %s", tg.key)
		}
		seen[tg.key] = true
	}
	// Exactly one Firefox-schema target: the rest are Chromium.
	ff := 0
	for _, tg := range targets {
		if tg.firefox {
			ff++
		}
	}
	if ff != 1 {
		t.Errorf("got %d Firefox targets, want 1", ff)
	}
}
