// SPDX-License-Identifier: Apache-2.0
// WO-091: the Windows install plan, proved without a Windows machine.
package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testLocalAppData = `C:\Users\qa\AppData\Local`

func windowsEnv(k string) string {
	if k == "LOCALAPPDATA" {
		return testLocalAppData
	}
	return ""
}

func windowsPlan(t *testing.T, exe string, all bool) installPlan {
	t.Helper()
	ts, err := targetsFor("windows", "", windowsEnv)
	if err != nil {
		t.Fatalf("targetsFor(windows): %v", err)
	}
	p, err := buildInstallPlan(ts, exe, all, []string{DefaultExtensionID}, "keel@local")
	if err != nil {
		t.Fatalf("buildInstallPlan: %v", err)
	}
	return p
}

func entryFor(t *testing.T, p installPlan, browser string) installEntry {
	t.Helper()
	for _, e := range p.entries {
		if e.browser == browser {
			return e
		}
	}
	t.Fatalf("no plan entry for %s", browser)
	return installEntry{}
}

// TestWindowsPlanSeparatesSchemas is the defect WO-091 was raised for. Every
// Windows browser used to get one directory and one filename, so the loop wrote
// the same path five times and Firefox — last in the list — left an
// allowed_extensions manifest behind the Brave registry key.
func TestWindowsPlanSeparatesSchemas(t *testing.T) {
	p := windowsPlan(t, `C:\keel\keel-host.exe`, false)

	if p.chromium == "" || p.firefox == "" {
		t.Fatalf("plan must name both manifests, got chromium=%q firefox=%q", p.chromium, p.firefox)
	}
	if samePath(p.chromium, p.firefox) {
		t.Fatalf("both schemas write to %s", p.chromium)
	}
	for _, want := range []string{p.chromium, p.firefox} {
		if !strings.HasSuffix(want, hostName+".json") {
			t.Errorf("manifest %q does not use the canonical filename", want)
		}
		if !strings.HasPrefix(normalizeWindowsPath(want), normalizeWindowsPath(testLocalAppData)) {
			t.Errorf("manifest %q is not under LOCALAPPDATA", want)
		}
	}

	for _, b := range []string{"Chrome", "Chromium", "Brave", "Edge"} {
		e := entryFor(t, p, b)
		if !samePath(e.path, p.chromium) {
			t.Errorf("%s writes %s, want the Chromium manifest %s", b, e.path, p.chromium)
		}
		if err := verifyManifestBytes(e.body, false, p.exe, []string{DefaultExtensionID}, "keel@local"); err != nil {
			t.Errorf("%s manifest: %v", b, err)
		}
	}
	ff := entryFor(t, p, "Firefox")
	if !samePath(ff.path, p.firefox) {
		t.Errorf("Firefox writes %s, want %s", ff.path, p.firefox)
	}
	if err := verifyManifestBytes(ff.body, true, p.exe, nil, "keel@local"); err != nil {
		t.Errorf("Firefox manifest: %v", err)
	}
}

// TestWindowsPlanAllCannotOverwrite is the regression guard: whatever order the
// entries are written in, no path is claimed by two schemas.
func TestWindowsPlanAllCannotOverwrite(t *testing.T) {
	p := windowsPlan(t, `C:\keel\keel-host.exe`, true)

	schemaOf := map[string]bool{}
	for _, e := range p.entries {
		if e.skip != "" {
			t.Fatalf("-all skipped %s (%s)", e.browser, e.skip)
		}
		key := normalizeWindowsPath(e.path)
		if prev, seen := schemaOf[key]; seen && prev != e.firefox {
			t.Fatalf("%s writes a %v-schema manifest over a %v-schema one at %s",
				e.browser, e.firefox, prev, e.path)
		}
		schemaOf[key] = e.firefox
	}

	// Replaying the writes in plan order must leave each file holding the schema
	// its registry key expects — the end state the browser actually sees.
	files := map[string][]byte{}
	for _, e := range p.entries {
		files[normalizeWindowsPath(e.path)] = e.body
	}
	if err := verifyManifestBytes(files[normalizeWindowsPath(p.chromium)], false,
		p.exe, []string{DefaultExtensionID}, "keel@local"); err != nil {
		t.Errorf("Chromium manifest after the full -all sweep: %v", err)
	}
	if err := verifyManifestBytes(files[normalizeWindowsPath(p.firefox)], true,
		p.exe, nil, "keel@local"); err != nil {
		t.Errorf("Firefox manifest after the full -all sweep: %v", err)
	}
}

// TestWindowsPlanRegistersEveryBrowserOnAFreshMachine: the old plan used the Keel
// output directory as the browser-presence test, so a first install — where that
// directory does not exist yet — skipped every browser.
func TestWindowsPlanRegistersEveryBrowserOnAFreshMachine(t *testing.T) {
	ts, err := targetsFor("windows", "", func(string) string {
		return filepath.Join(t.TempDir(), "no-such-appdata")
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := buildInstallPlan(ts, `C:\keel\keel-host.exe`, false, []string{DefaultExtensionID}, "keel@local")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.entries) != 5 {
		t.Fatalf("got %d Windows targets, want 5", len(p.entries))
	}
	for _, e := range p.entries {
		if e.skip != "" {
			t.Errorf("%s skipped on a fresh machine: %s", e.browser, e.skip)
		}
	}
}

func TestWindowsInstallBaseNeedsLocalAppData(t *testing.T) {
	if _, err := targetsFor("windows", `C:\Users\qa`, func(string) string { return "" }); err == nil {
		t.Fatal("want an error when LOCALAPPDATA is unset, got none")
	}
}

// TestUnixPlansStillDetect: only Windows registers unconditionally. Linux and
// macOS write into the browser's own directory, where a manifest for a browser
// that is not installed is litter.
func TestUnixPlansStillDetect(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		ts, err := targetsFor(goos, filepath.Join(t.TempDir(), "home"), func(string) string { return "" })
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		p, err := buildInstallPlan(ts, "/opt/keel/keel-host", false, []string{DefaultExtensionID}, "keel@local")
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		for _, e := range p.entries {
			if e.skip == "" {
				t.Errorf("%s: %s was not skipped although nothing is installed", goos, e.browser)
			}
		}
	}
}

func TestDispatch(t *testing.T) {
	cases := []struct {
		name string
		goos string
		args []string
		want string
	}{
		{"windows double-click installs", "windows", nil, "install"},
		{"windows browser launch proxies", "windows",
			[]string{"chrome-extension://" + DefaultExtensionID + "/"}, ""},
		{"windows chrome parent-window handle proxies", "windows",
			[]string{"chrome-extension://" + DefaultExtensionID + "/", "--parent-window=12345"}, ""},
		{"firefox launch proxies", "windows",
			[]string{`C:\Users\qa\AppData\Local\Keel\firefox\com.keel.host.json`, "keel@local"}, ""},
		{"linux no argument proxies", "linux", nil, ""},
		{"explicit install", "linux", []string{"install", "-all"}, "install"},
		{"explicit uninstall", "windows", []string{"uninstall"}, "uninstall"},
		{"dashed subcommand", "linux", []string{"--version"}, "version"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, rest := dispatch(c.goos, c.args)
			if got != c.want {
				t.Fatalf("dispatch(%q, %q) = %q, want %q", c.goos, c.args, got, c.want)
			}
			if c.want == "install" && len(c.args) > 0 && len(rest) != len(c.args)-1 {
				t.Errorf("subcommand arguments not forwarded: %q", rest)
			}
		})
	}
}

// TestManifestVerificationRejectsTheWrongSchema: os.Stat proves a file exists.
// Only decoding proves the browser reading it will find what it needs.
func TestManifestVerificationRejectsTheWrongSchema(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "keel-host.exe")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	ids := []string{DefaultExtensionID}

	chromium, err := manifestBytes(browserTarget{name: "Brave"}, exe, ids, "keel@local")
	if err != nil {
		t.Fatal(err)
	}
	firefox, err := manifestBytes(browserTarget{name: "Firefox", firefox: true}, exe, ids, "keel@local")
	if err != nil {
		t.Fatal(err)
	}

	if err := verifyManifestBytes(chromium, false, exe, ids, "keel@local"); err != nil {
		t.Errorf("valid Chromium manifest rejected: %v", err)
	}
	if err := verifyManifestBytes(firefox, true, exe, nil, "keel@local"); err != nil {
		t.Errorf("valid Firefox manifest rejected: %v", err)
	}
	// The exact WO-091 breakage: Brave's key resolving to a Firefox manifest.
	if err := verifyManifestBytes(firefox, false, exe, ids, "keel@local"); err == nil {
		t.Error("a Firefox manifest passed Chromium validation")
	}
	if err := verifyManifestBytes(chromium, true, exe, nil, "keel@local"); err == nil {
		t.Error("a Chromium manifest passed Firefox validation")
	}
	// Wrong extension, wrong host name, wrong type, empty origin list.
	if err := verifyManifestBytes(chromium, false, exe, []string{"someotherextensionid"}, ""); err == nil {
		t.Error("a manifest for a different extension passed validation")
	}
	for _, field := range []string{"name", "type", "path"} {
		var m map[string]any
		if err := json.Unmarshal(chromium, &m); err != nil {
			t.Fatal(err)
		}
		m[field] = "wrong"
		bad, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyManifestBytes(bad, false, exe, ids, "keel@local"); err == nil {
			t.Errorf("a manifest with a wrong %q passed validation", field)
		}
	}

	// A valid manifest naming an executable that is not there is still a failed
	// install: this is what "native messaging host not found" looks like.
	path := filepath.Join(dir, hostName+".json")
	if err := os.WriteFile(path, chromium, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyManifestFile(path, false, exe, ids, "keel@local"); err != nil {
		t.Errorf("verifyManifestFile: %v", err)
	}
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	if err := verifyManifestFile(path, false, exe, ids, "keel@local"); err == nil {
		t.Error("a manifest pointing at a missing executable passed validation")
	}
}

func TestPrepareExtensionFolder(t *testing.T) {
	const template = `{"manifest_version":3,"name":"Keel"}`

	// Both release layouts: the folder beside the binary and the folder beside
	// the folder the binary is in (repository: daemon/keel-host, ../extension).
	for _, c := range []struct {
		name, dirName string
		parent        bool
	}{
		{"extension beside the binary", "extension", false},
		{"keel-extension beside the binary", "keel-extension", false},
		{"extension one level up", "extension", true},
		{"keel-extension one level up", "keel-extension", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			exeDir := root
			if c.parent {
				exeDir = filepath.Join(root, "bin")
				if err := os.Mkdir(exeDir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			ext := filepath.Join(root, c.dirName)
			if err := os.Mkdir(ext, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ext, "manifest.chrome.json"), []byte(template), 0o644); err != nil {
				t.Fatal(err)
			}
			_, msg, err := prepareExtensionFolder(filepath.Join(exeDir, "keel-host.exe"))
			if err != nil {
				t.Fatalf("prepareExtensionFolder: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(ext, "manifest.json"))
			if err != nil {
				t.Fatalf("manifest.json not written: %v", err)
			}
			if string(got) != template {
				t.Errorf("manifest.json = %q, want the chrome template", got)
			}
			if !strings.Contains(msg, "prepared") {
				t.Errorf("outcome %q does not report preparation", msg)
			}
		})
	}

	t.Run("packaged extension is already prepared", func(t *testing.T) {
		root := t.TempDir()
		ext := filepath.Join(root, "keel-extension")
		if err := os.Mkdir(ext, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ext, "manifest.json"), []byte(template), 0o644); err != nil {
			t.Fatal(err)
		}
		_, msg, err := prepareExtensionFolder(filepath.Join(root, "keel-host.exe"))
		if err != nil {
			t.Fatalf("a packaged extension must not be an error: %v", err)
		}
		if !strings.Contains(msg, "already prepared") {
			t.Errorf("outcome %q, want it reported as already prepared", msg)
		}
	})

	// A standalone binary is a supported layout. Reporting it as a missing host
	// manifest is what sent this investigation after the wrong bug.
	t.Run("standalone binary is not an install failure", func(t *testing.T) {
		root := t.TempDir()
		if _, _, err := prepareExtensionFolder(filepath.Join(root, "keel-host.exe")); !errors.Is(err, errNoExtensionFolder) {
			t.Fatalf("err = %v, want errNoExtensionFolder", err)
		}
	})
}

// TestClearMarkOfTheWeb is the difference between a downloaded release and a
// locally built one.
//
// Windows records a file's origin in a Zone.Identifier alternate data stream.
// A marked executable is restricted, and a browser launching one as a
// native-messaging host gets nothing — the same "Specified native messaging
// host not found" a missing registry key produces. Cloning and building carries
// no mark, which is why that works instantly while an identical downloaded
// binary never does. Every file extracted from a marked .zip inherits the mark,
// so a downloaded extension folder fails to load for the same reason.
//
// The stream cannot be created on a non-NTFS filesystem, so the test writes a
// file whose name is literally the ADS path — which is what os.Remove is asked
// to delete either way.
func TestClearMarkOfTheWeb(t *testing.T) {
	dir := t.TempDir()

	// No mark is the normal case and must not be an error.
	plain := filepath.Join(dir, "keel-host.exe")
	if err := os.WriteFile(plain, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := clearMarkOfTheWeb(plain); err != nil {
		t.Errorf("an unmarked file reported an error: %v", err)
	}
	if _, err := os.Stat(plain); err != nil {
		t.Errorf("the file itself was removed: %v", err)
	}

	// A mark present is removed, and only the stream is removed.
	marked := filepath.Join(dir, "downloaded.exe")
	if err := os.WriteFile(marked, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	stream := marked + ":Zone.Identifier"
	if err := os.WriteFile(stream, []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o644); err != nil {
		t.Skipf("cannot create an alternate-data-stream stand-in here: %v", err)
	}
	if err := clearMarkOfTheWeb(marked); err != nil {
		t.Fatalf("clearing the mark: %v", err)
	}
	if _, err := os.Stat(stream); err == nil {
		t.Error("the mark is still there")
	}
	if _, err := os.Stat(marked); err != nil {
		t.Errorf("clearing the mark deleted the file: %v", err)
	}
}

// TestClearMarkOfTheWebTree: an extension folder is many files, and one marked
// file is enough to stop the browser loading it.
func TestClearMarkOfTheWebTree(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "lib")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	files := []string{
		filepath.Join(dir, "manifest.json"),
		filepath.Join(dir, "background.js"),
		filepath.Join(sub, "protocol.js"),
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f+":Zone.Identifier", []byte("[ZoneTransfer]"), 0o644); err != nil {
			t.Skipf("cannot create an alternate-data-stream stand-in here: %v", err)
		}
	}
	n, err := clearMarkOfTheWebTree(dir)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if n < len(files) {
		t.Errorf("cleared %d marks, want at least %d", n, len(files))
	}
	for _, f := range files {
		if _, err := os.Stat(f + ":Zone.Identifier"); err == nil {
			t.Errorf("%s is still marked", f)
		}
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s was deleted: %v", f, err)
		}
	}
}
