// SPDX-License-Identifier: Apache-2.0
// Native-messaging host registration (WO-020).
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
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const hostName = "com.keel.host"

// browserTarget is one browser's native-messaging host directory.
//
// detect is the directory whose existence means "this browser is installed".
// dir is where the host manifest goes. They differ because the manifest
// directory usually does not exist until something creates it.
type browserTarget struct {
	name    string
	detect  string
	dir     string
	firefox bool
}

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
	h, err := home()
	if err != nil {
		return nil, err
	}
	j := filepath.Join

	switch runtime.GOOS {
	case "linux":
		cfg := j(h, ".config")
		return []browserTarget{
			{"Chrome", j(cfg, "google-chrome"), j(cfg, "google-chrome", "NativeMessagingHosts"), false},
			{"Chromium", j(cfg, "chromium"), j(cfg, "chromium", "NativeMessagingHosts"), false},
			{"Brave", j(cfg, "BraveSoftware", "Brave-Browser"), j(cfg, "BraveSoftware", "Brave-Browser", "NativeMessagingHosts"), false},
			{"Edge", j(cfg, "microsoft-edge"), j(cfg, "microsoft-edge", "NativeMessagingHosts"), false},
			{"Firefox", j(h, ".mozilla"), j(h, ".mozilla", "native-messaging-hosts"), true},
		}, nil

	case "darwin":
		app := j(h, "Library", "Application Support")
		return []browserTarget{
			{"Chrome", j(app, "Google", "Chrome"), j(app, "Google", "Chrome", "NativeMessagingHosts"), false},
			{"Chromium", j(app, "Chromium"), j(app, "Chromium", "NativeMessagingHosts"), false},
			{"Brave", j(app, "BraveSoftware", "Brave-Browser"), j(app, "BraveSoftware", "Brave-Browser", "NativeMessagingHosts"), false},
			{"Edge", j(app, "Microsoft Edge"), j(app, "Microsoft Edge", "NativeMessagingHosts"), false},
			{"Firefox", j(app, "Mozilla"), j(app, "Mozilla", "NativeMessagingHosts"), true},
		}, nil

	case "windows":
		// Windows registers via the registry, not a well-known directory. The
		// manifests are written next to the binary and the exact reg commands
		// are printed — see installWindowsNote.
		base := j(h, "AppData", "Local", "Keel")
		return []browserTarget{
			{"Chrome", base, base, false},
			{"Chromium", base, base, false},
			{"Brave", base, base, false},
			{"Edge", base, base, false},
			{"Firefox", base, base, true},
		}, nil
	}
	return nil, fmt.Errorf("unsupported OS %q", runtime.GOOS)
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
		origins = append(origins, fmt.Sprintf("chrome-extension://%s/", id))
	}
	return json.MarshalIndent(chromiumManifest{
		Name: hostName, Description: desc, Path: exe, Type: "stdio",
		AllowedOrigins: origins,
	}, "", "  ")
}

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

// writeExtensionManifest copies the Chrome manifest template into place.
//
// extension/manifest.json is generated from manifest.chrome.json, so a fresh
// clone has no loadable manifest and "Load unpacked" fails saying nothing
// useful. The npm script that does this would make Node a prerequisite for
// people who are only trying to run the thing, so the installer does it
// instead — it already runs, and it already knows where it is.
//
// Best effort: a binary copied elsewhere still installs the host manifests
// correctly and simply reports that it could not find the extension folder.
func writeExtensionManifest() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	root := filepath.Dir(filepath.Dir(exe)) // daemon/keel-host -> repo root
	src := filepath.Join(root, "extension", "manifest.chrome.json")
	dst := filepath.Join(root, "extension", "manifest.json")
	data, err := os.ReadFile(src)
	if err != nil {
		fmt.Println("note: extension/manifest.chrome.json not found —",
			"run this from the daemon folder of the repository")
		return
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		fmt.Println("note: could not write extension/manifest.json:", err)
		return
	}
	fmt.Println("prepared", dst)
}

// runInstall writes host manifests for every browser detected on this machine.
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

	var chromeIDs []string
	for _, s := range strings.Split(*idCSV, ",") {
		if s = strings.TrimSpace(s); s != "" {
			chromeIDs = append(chromeIDs, s)
		}
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot determine own path: %v\n", err)
		return 1
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		fmt.Fprintf(os.Stderr, "cannot resolve own path: %v\n", err)
		return 1
	}

	ts, err := targets()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	var wrote, skipped []string
	for _, t := range ts {
		if !*all && !exists(t.detect) {
			skipped = append(skipped, t.name)
			continue
		}
		// Chromium targets are useless without an extension ID: allowed_origins
		// takes no wildcards, so an empty list matches nothing.
		if !t.firefox && len(chromeIDs) == 0 {
			skipped = append(skipped, t.name+" (no -extension-id)")
			continue
		}
		b, err := manifestBytes(t, exe, chromeIDs, *geckoID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", t.name, err)
			return 1
		}
		dest := filepath.Join(t.dir, hostName+".json")
		if *dry {
			fmt.Printf("would write %s\n", dest)
			continue
		}
		if err := os.MkdirAll(t.dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", t.name, err)
			return 1
		}
		if err := os.WriteFile(dest, append(b, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", t.name, err)
			return 1
		}
		wrote = append(wrote, fmt.Sprintf("%-9s %s", t.name, dest))
	}

	sort.Strings(wrote)
	if len(wrote) == 0 && !*dry {
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
	if runtime.GOOS == "windows" && !*dry {
		installWindowsRegistry(filepath.Join(ts[0].dir, hostName+".json"))
	}
	if !*dry {
		writeExtensionManifest()
		fmt.Println("\nReload the extension. The SidePanel should show \"Desktop app connected\".")
	}
	return 0
}

// installWindowsRegistry creates the registry values Windows uses to find the
// host.
//
// Registration on Windows is a registry value rather than a file in a known
// directory, so writing the manifests is only half the job. This used to print
// the commands for the user to run, which is a wall for anyone who is not a
// developer — the point of an installer is that nothing is left to do
// afterwards.
//
// Values go under HKCU, so no administrator rights are needed. A browser that
// is not installed still gets its key, which is harmless: only that browser
// ever reads it.
func installWindowsRegistry(manifest string) {
	keys := []struct{ name, key string }{
		{"Chrome", `HKCU\Software\Google\Chrome\NativeMessagingHosts\` + hostName},
		{"Chromium", `HKCU\Software\Chromium\NativeMessagingHosts\` + hostName},
		{"Brave", `HKCU\Software\BraveSoftware\Brave-Browser\NativeMessagingHosts\` + hostName},
		{"Edge", `HKCU\Software\Microsoft\Edge\NativeMessagingHosts\` + hostName},
		{"Firefox", `HKCU\Software\Mozilla\NativeMessagingHosts\` + hostName},
	}
	var failed bool
	for _, k := range keys {
		out, err := exec.Command("reg", "add", k.key,
			"/ve", "/t", "REG_SZ", "/d", manifest, "/f").CombinedOutput()
		if err != nil {
			failed = true
			fmt.Printf("could not register %s: %v %s\n", k.name, err, strings.TrimSpace(string(out)))
			continue
		}
		fmt.Println("registered", k.name)
	}
	if failed {
		fmt.Println("\nRun these in a Command Prompt for the browsers you use:")
		for _, k := range keys {
			fmt.Printf("  reg add \"%s\" /ve /t REG_SZ /d \"%s\" /f\n", k.key, manifest)
		}
	}
}

// runUninstall removes every host manifest this installer would have written.
func runUninstall(args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "print what would be removed, remove nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ts, err := targets()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var removed int
	for _, t := range ts {
		dest := filepath.Join(t.dir, hostName+".json")
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
	if runtime.GOOS == "windows" {
		fmt.Println("\nWindows: also delete the registry keys under")
		fmt.Println(`  HKCU\Software\...\NativeMessagingHosts\` + hostName)
	}
	return 0
}
