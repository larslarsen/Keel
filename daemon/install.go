// SPDX-License-Identifier: Apache-2.0
// Native-messaging host registration (WO-020, WO-091).
//
// The daemon installs itself: it knows its own absolute path, so nothing has to
// be edited by hand. Registration is per-user only — no admin rights, no
// system-wide paths, nothing outside the user's own config directories.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const hostName = "com.keel.host"

// browserTarget is one browser's native-messaging host directory.
//
// detect is the directory whose existence means "this browser is installed"; an
// empty detect means "always register" (Windows, where registration is a
// registry value and an unread key is harmless). dir is where the host manifest
// goes. They differ because the manifest directory usually does not exist until
// something creates it.
type browserTarget struct {
	name    string
	detect  string
	dir     string
	firefox bool
}

func (t browserTarget) manifestPath() string { return filepath.Join(t.dir, hostName+".json") }

// chromiumManifest is the Chromium-family host manifest.
// allowed_origins accepts no wildcards, so each extension ID is listed exactly.
type chromiumManifest struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	AllowedOrigins []string `json:"allowed_origins"`
}

// firefoxManifest differs from Chromium: extensions are identified by gecko ID,
// not by an origin URL.
type firefoxManifest struct {
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Path              string   `json:"path"`
	Type              string   `json:"type"`
	AllowedExtensions []string `json:"allowed_extensions"`
}

func home() (string, error) { return os.UserHomeDir() }

// targets lists every supported browser for the current OS.
func targets() ([]browserTarget, error) {
	h, _ := home() // Windows does not need it; targetsFor reports the failure.
	return targetsFor(runtime.GOOS, h, os.Getenv)
}

// targetsFor is targets() with the machine passed in, so the plan for one OS can
// be built and tested from any other.
func targetsFor(goos, h string, env func(string) string) ([]browserTarget, error) {
	j := filepath.Join

	switch goos {
	case "linux":
		if h == "" {
			return nil, errors.New("cannot determine home directory")
		}
		cfg := j(h, ".config")
		return []browserTarget{
			{"Chrome", j(cfg, "google-chrome"), j(cfg, "google-chrome", "NativeMessagingHosts"), false},
			{"Chromium", j(cfg, "chromium"), j(cfg, "chromium", "NativeMessagingHosts"), false},
			{"Brave", j(cfg, "BraveSoftware", "Brave-Browser"), j(cfg, "BraveSoftware", "Brave-Browser", "NativeMessagingHosts"), false},
			{"Edge", j(cfg, "microsoft-edge"), j(cfg, "microsoft-edge", "NativeMessagingHosts"), false},
			{"Firefox", j(h, ".mozilla"), j(h, ".mozilla", "native-messaging-hosts"), true},
		}, nil

	case "darwin":
		if h == "" {
			return nil, errors.New("cannot determine home directory")
		}
		app := j(h, "Library", "Application Support")
		return []browserTarget{
			{"Chrome", j(app, "Google", "Chrome"), j(app, "Google", "Chrome", "NativeMessagingHosts"), false},
			{"Chromium", j(app, "Chromium"), j(app, "Chromium", "NativeMessagingHosts"), false},
			{"Brave", j(app, "BraveSoftware", "Brave-Browser"), j(app, "BraveSoftware", "Brave-Browser", "NativeMessagingHosts"), false},
			{"Edge", j(app, "Microsoft Edge"), j(app, "Microsoft Edge", "NativeMessagingHosts"), false},
			{"Firefox", j(app, "Mozilla"), j(app, "Mozilla", "NativeMessagingHosts"), true},
		}, nil

	case "windows":
		// Windows registers through the registry, so Keel chooses the manifest
		// location itself. The two browser families need *different* files: a
		// Chromium manifest has allowed_origins and a Firefox manifest has
		// allowed_extensions, and one path serving both means whichever browser
		// is written last silently breaks every other one (WO-091).
		base, err := windowsInstallBase(env)
		if err != nil {
			return nil, err
		}
		chromium := j(base, "chromium")
		firefox := j(base, "firefox")
		return []browserTarget{
			{name: "Chrome", dir: chromium},
			{name: "Chromium", dir: chromium},
			{name: "Brave", dir: chromium},
			{name: "Edge", dir: chromium},
			{name: "Firefox", dir: firefox, firefox: true},
		}, nil
	}
	return nil, fmt.Errorf("unsupported OS %q", goos)
}

// windowsInstallBase is the per-user directory the host manifests live in.
//
// Taken from LOCALAPPDATA rather than assembled from the home directory and a
// hard-coded "AppData\Local": on a redirected or roaming profile those are not
// the same place, and a manifest written to the wrong one is a registry key
// pointing at a file that does not exist.
func windowsInstallBase(env func(string) string) (string, error) {
	if v := strings.TrimSpace(env("LOCALAPPDATA")); v != "" {
		return filepath.Join(v, "Keel"), nil
	}
	return "", errors.New("LOCALAPPDATA is not set; cannot choose a per-user install directory")
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

// manifestBytes renders the host manifest for one target.
func manifestBytes(t browserTarget, exe string, chromeIDs []string, geckoID string) ([]byte, error) {
	const desc = "Keel desktop host"
	if t.firefox {
		return json.MarshalIndent(firefoxManifest{
			Name: hostName, Description: desc, Path: exe, Type: "stdio",
			AllowedExtensions: []string{geckoID},
		}, "", "  ")
	}
	origins := make([]string, 0, len(chromeIDs))
	for _, id := range chromeIDs {
		origins = append(origins, chromeOrigin(id))
	}
	return json.MarshalIndent(chromiumManifest{
		Name: hostName, Description: desc, Path: exe, Type: "stdio",
		AllowedOrigins: origins,
	}, "", "  ")
}

func chromeOrigin(id string) string { return "chrome-extension://" + id + "/" }

// DefaultExtensionID is Keel's extension ID, and it is the same on every
// machine.
//
// An unpacked extension normally gets a different ID per install, which would
// force every tester to copy theirs out of chrome://extensions and pass it
// here — the step most likely to defeat someone who is not a developer. A "key"
// in manifest.json pins the ID instead: it is derived from that public key, so
// it is identical everywhere and will match the Chrome Web Store listing when
// the same key is used there.
const DefaultExtensionID = "agipaaiffkeeomfeialpkgnegndefgan"

// errNoExtensionFolder means the binary is standing on its own with no
// extracted extension beside it. That is a supported layout, not a failure.
var errNoExtensionFolder = errors.New("extension folder not found")

// prepareExtensionFolder makes the extracted extension loadable.
//
// extension/manifest.json is generated from manifest.chrome.json, so a fresh
// clone has no loadable manifest and "Load unpacked" fails saying nothing
// useful. The npm script that does this would make Node a prerequisite for
// people who are only trying to run the thing, so the installer does it
// instead — it already runs, and it already knows where it is.
//
// Both the executable's own directory and its parent are probed, under both
// names the release layouts use: the repository has daemon/keel-host beside
// ../extension, and the release zip extracts to keel-extension beside the .exe.
func prepareExtensionFolder(exe string) (string, error) {
	dir := filepath.Dir(exe)
	for _, base := range []string{dir, filepath.Dir(dir)} {
		for _, name := range []string{"extension", "keel-extension"} {
			cand := filepath.Join(base, name)
			if fi, err := os.Stat(cand); err != nil || !fi.IsDir() {
				continue
			}
			dst := filepath.Join(cand, "manifest.json")
			// A packaged extension already carries its manifest. Rewriting it
			// from a template that is not shipped would be the only failure.
			if _, err := os.Stat(dst); err == nil {
				return "already prepared: " + dst, nil
			}
			data, err := os.ReadFile(filepath.Join(cand, "manifest.chrome.json"))
			if err != nil {
				continue
			}
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				return "", fmt.Errorf("write %s: %w", dst, err)
			}
			return "prepared " + dst, nil
		}
	}
	return "", errNoExtensionFolder
}

// installEntry is one browser's share of an install: which file, which schema,
// and — when skip is set — why it is not being written.
type installEntry struct {
	browser string
	path    string
	firefox bool
	body    []byte
	skip    string
}

// installPlan is everything an install would write, decided before anything on
// disk is touched. Windows registration needs the two schema-specific manifest
// paths, and every acceptance question about the plan can be asked of this
// struct without a Windows machine.
type installPlan struct {
	exe      string
	entries  []installEntry
	chromium string // manifest the Chromium-family registry keys point at
	firefox  string // manifest the Firefox registry key points at
}

func buildInstallPlan(ts []browserTarget, exe string, all bool, chromeIDs []string, geckoID string) (installPlan, error) {
	p := installPlan{exe: exe}
	for _, t := range ts {
		e := installEntry{browser: t.name, path: t.manifestPath(), firefox: t.firefox}
		switch {
		case t.detect != "" && !all && !exists(t.detect):
			e.skip = "not detected"
		// Chromium targets are useless without an extension ID: allowed_origins
		// takes no wildcards, so an empty list matches nothing.
		case !t.firefox && len(chromeIDs) == 0:
			e.skip = "no -extension-id"
		}
		if e.skip == "" {
			b, err := manifestBytes(t, exe, chromeIDs, geckoID)
			if err != nil {
				return installPlan{}, fmt.Errorf("%s: %w", t.name, err)
			}
			e.body = b
			if t.firefox {
				p.firefox = e.path
			} else {
				p.chromium = e.path
			}
		}
		p.entries = append(p.entries, e)
	}
	// The WO-091 defect in one assertion: two schemas may never share a path.
	// The loop writes in order, so a shared path means the last browser written
	// silently replaced every earlier family's manifest with the wrong schema.
	if p.chromium != "" && p.firefox != "" && samePath(p.chromium, p.firefox) {
		return installPlan{}, fmt.Errorf(
			"install plan writes both schemas to %s; Chromium and Firefox manifests must be separate files", p.chromium)
	}
	return p, nil
}

// samePath compares two manifest destinations. Windows paths are compared
// case-insensitively because its filesystem is.
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" || strings.Contains(a, `\`) || strings.Contains(b, `\`) {
		return normalizeWindowsPath(a) == normalizeWindowsPath(b)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// verifyManifestFile re-reads and decodes a manifest that was just written.
//
// os.Stat proves a file exists; it does not prove the browser reading it will
// find its own schema, the right extension, or an executable that is still
// there. Every one of those has been the actual failure at least once.
func verifyManifestFile(path string, firefox bool, exe string, chromeIDs []string, geckoID string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := verifyManifestBytes(data, firefox, exe, chromeIDs, geckoID); err != nil {
		return err
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("manifest path %s: %w", exe, err)
	}
	return nil
}

func verifyManifestBytes(data []byte, firefox bool, exe string, chromeIDs []string, geckoID string) error {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}
	if got, _ := m["name"].(string); got != hostName {
		return fmt.Errorf("name is %q, want %q", got, hostName)
	}
	if got, _ := m["path"].(string); got != exe {
		return fmt.Errorf("path is %q, want %q", got, exe)
	}
	if got, _ := m["type"].(string); got != "stdio" {
		return fmt.Errorf("type is %q, want \"stdio\"", got)
	}
	if firefox {
		if _, ok := m["allowed_origins"]; ok {
			return errors.New("Firefox manifest carries Chromium-only allowed_origins")
		}
		return requireStrings(m["allowed_extensions"], "allowed_extensions", geckoID)
	}
	if _, ok := m["allowed_extensions"]; ok {
		return errors.New("Chromium manifest carries Firefox-only allowed_extensions")
	}
	want := make([]string, 0, len(chromeIDs))
	for _, id := range chromeIDs {
		want = append(want, chromeOrigin(id))
	}
	return requireStrings(m["allowed_origins"], "allowed_origins", want...)
}

func requireStrings(v any, field string, want ...string) error {
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("%s is missing", field)
	}
	have := map[string]bool{}
	for _, x := range list {
		if s, ok := x.(string); ok {
			have[s] = true
		}
	}
	for _, w := range want {
		if !have[w] {
			return fmt.Errorf("%s does not list %q", field, w)
		}
	}
	if len(want) == 0 {
		return fmt.Errorf("%s is empty", field)
	}
	return nil
}

// runInstall writes host manifests for every browser this OS registers.
func runInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	idCSV := fs.String("extension-id", DefaultExtensionID,
		"Chromium extension ID(s), comma separated. Defaults to Keel's own, fixed by the key in manifest.json.")
	geckoID := fs.String("firefox-id", "keel@local", "Firefox extension ID (browser_specific_settings.gecko.id)")
	all := fs.Bool("all", false, "write for every supported browser, not only detected ones")
	dry := fs.Bool("dry-run", false, "print what would be written, write nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot determine own path: %v\n", err)
		return 1
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	} else {
		fmt.Fprintf(os.Stderr, "cannot resolve own path: %v\n", err)
		return 1
	}

	// Windows QA happens by double-clicking, where the console vanishes with the
	// process. The report is the only thing left to read afterwards, so it is
	// opened before anything can fail and written as the install proceeds.
	rep := discardedReport()
	if runtime.GOOS == "windows" && !*dry {
		r, err := openInstallReport(filepath.Dir(exe))
		if err != nil {
			fmt.Fprintln(os.Stderr, "install report:", err)
			return 1
		}
		defer r.close()
		rep = r
	}
	rep.line("Keel install report")
	rep.line("host version   %s (built %s)", version, builtAt())
	rep.line("executable     %s", exe)
	rep.line("")

	if !*dry {
		p, err := resolveOwnerPaths()
		if err != nil {
			rep.fail("owner paths: %v", err)
			fmt.Fprintln(os.Stderr, "owner paths:", err)
			return 1
		}
		if _, err := ownerSecret(p); err != nil {
			rep.fail("owner credential: %v", err)
			fmt.Fprintln(os.Stderr, "owner credential:", err)
			return 1
		}
	}

	var chromeIDs []string
	for _, s := range strings.Split(*idCSV, ",") {
		if s = strings.TrimSpace(s); s != "" {
			chromeIDs = append(chromeIDs, s)
		}
	}

	ts, err := targets()
	if err != nil {
		rep.fail("%v", err)
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	plan, err := buildInstallPlan(ts, exe, *all, chromeIDs, *geckoID)
	if err != nil {
		rep.fail("%v", err)
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	var wrote, skipped []string
	for _, e := range plan.entries {
		if e.skip != "" {
			skipped = append(skipped, e.browser+" ("+e.skip+")")
			rep.line("manifest %-9s SKIPPED  %s", e.browser, e.skip)
			continue
		}
		if *dry {
			fmt.Printf("would write %s\n", e.path)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(e.path), 0o755); err != nil {
			rep.fail("%s: %v", e.browser, err)
			fmt.Fprintf(os.Stderr, "%s: %v\n", e.browser, err)
			return 1
		}
		if err := os.WriteFile(e.path, append(e.body, '\n'), 0o644); err != nil {
			rep.fail("%s: %v", e.browser, err)
			fmt.Fprintf(os.Stderr, "%s: %v\n", e.browser, err)
			return 1
		}
		if err := verifyManifestFile(e.path, e.firefox, exe, chromeIDs, *geckoID); err != nil {
			rep.fail("manifest %s is invalid: %v", e.path, err)
			fmt.Fprintf(os.Stderr, "%s: %v\n", e.browser, err)
			return 1
		}
		rep.line("manifest %-9s OK       %s", e.browser, e.path)
		wrote = append(wrote, fmt.Sprintf("%-9s %s", e.browser, e.path))
	}

	sort.Strings(wrote)
	if len(wrote) == 0 && !*dry {
		rep.fail("no browser was registered")
		fmt.Println("Nothing installed.")
		if len(chromeIDs) == 0 {
			fmt.Println("Chromium browsers need -extension-id (see chrome://extensions).")
		}
		fmt.Println("Use -all to write for browsers that are not detected.")
		return 1
	}
	for _, w := range wrote {
		fmt.Println("installed", w)
	}
	if len(skipped) > 0 {
		fmt.Println("skipped:  " + strings.Join(skipped, ", "))
	}

	failed := false
	if runtime.GOOS == "windows" && !*dry {
		rep.line("")
		// Before registering: make sure the browser can still read what was
		// just written. An earlier Keel build applied a protected DACL to this
		// directory and locked browsers out of their own manifest, and no
		// amount of correct registration fixes a directory they cannot read.
		//
		// Best effort, deliberately: a directory whose permissions cannot be
		// rewritten may still be perfectly readable, and refusing to register
		// there converts a maybe-working install into a certainly-broken one.
		// The first version of this failed the install on a denied icacls,
		// which is the same mistake as making a diagnostic fatal.
		if base, err := windowsInstallBase(os.Getenv); err == nil {
			if err := repairManifestAccess(execRunner, base); err != nil {
				rep.line("access   %-9s WARNING  %v", "manifests", err)
				rep.line("               registration continues; the folder may already be readable")
				fmt.Fprintln(os.Stderr, "note: could not adjust permissions on", base, "—", err)
			} else {
				rep.line("access   %-9s OK       permissions restored on %s", "manifests", base)
			}
		}
		results := installWindowsRegistry(execRunner, plan.chromium, plan.firefox)
		for _, r := range results {
			rep.registry(r)
			if r.err != nil {
				failed = true
				fmt.Fprintf(os.Stderr, "registry %s: %v\n", r.browser, r.err)
				continue
			}
			fmt.Println("registered", r.browser)
		}
	}

	if !*dry {
		rep.line("")
		switch msg, err := prepareExtensionFolder(exe); {
		case errors.Is(err, errNoExtensionFolder):
			// A standalone host binary is a supported layout. Saying the host
			// manifest is missing here is what sent WO-091 chasing the wrong bug.
			rep.line("extension      extension folder not found; host registration completed")
			fmt.Println("note: no extension folder beside the binary; host registration completed")
		case err != nil:
			rep.fail("extension: %v", err)
			fmt.Fprintln(os.Stderr, "extension:", err)
			failed = true
		default:
			rep.line("extension      %s", msg)
			fmt.Println(msg)
		}
	}

	// Installing a new binary does not replace the owner that is already
	// running. The owner outlives the browser by design, so after an upgrade the
	// *previous* build stays resident and keeps answering — the new proxy simply
	// connects to it. HELLO is then negotiated against old code, and an extension
	// that requires a capability the running owner predates is refused with
	// "desktop app update required" even though the update is sitting on disk.
	//
	// Best effort: "not running" is the normal case on a first install, and
	// requestOwnerControl never starts one.
	if !*dry {
		if err := requestOwnerControl("shutdown"); err != nil {
			rep.line("owner          not running; nothing to replace")
		} else {
			rep.line("owner          stopped; the next browser connection starts this binary")
			fmt.Println("stopped the previously running desktop app so this one takes over")
		}
	}

	if failed {
		rep.finish(false)
		fmt.Fprintln(os.Stderr, "\nInstallation is incomplete. See the report for the first error.")
		return 1
	}
	rep.finish(true)
	if !*dry {
		fmt.Println("\nReload the extension. The SidePanel should show \"Desktop app connected\".")
	}
	return 0
}

// runUninstall removes every host manifest and registry key this installer
// would have written. It never touches the corpus.
func runUninstall(args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "print what would be removed, remove nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*dry {
		// Best effort: an install can be partially removed or the owner can
		// already be down. Never start an owner during uninstall.
		_ = requestOwnerControl("shutdown")
	}
	ts, err := targets()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var removed int
	seen := map[string]bool{}
	for _, t := range ts {
		dest := t.manifestPath()
		if seen[dest] {
			continue // Windows: one manifest serves the whole Chromium family.
		}
		seen[dest] = true
		if _, err := os.Stat(dest); err != nil {
			continue
		}
		if *dry {
			fmt.Println("would remove", dest)
			removed++
			continue
		}
		if err := os.Remove(dest); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", t.name, err)
			continue
		}
		fmt.Println("removed", dest)
		removed++
	}
	if removed == 0 {
		fmt.Println("Nothing to remove.")
	}
	failed := false
	if runtime.GOOS == "windows" && *dry {
		for _, t := range windowsRegistryTargets() {
			fmt.Println("would unregister", t.key)
		}
	}
	if runtime.GOOS == "windows" && !*dry {
		for _, r := range uninstallWindowsRegistry(execRunner) {
			if r.err != nil {
				failed = true
				fmt.Fprintf(os.Stderr, "registry %s: %v\n", r.browser, r.err)
				continue
			}
			fmt.Println("unregistered", r.browser)
		}
	}
	if !*dry {
		if p, err := resolveOwnerPaths(); err == nil {
			if err := removeOwnerCredentials(p); err != nil {
				fmt.Fprintln(os.Stderr, "owner credential:", err)
			} else {
				fmt.Println("removed local owner credential")
			}
		}
	}
	if failed {
		return 1
	}
	return 0
}
