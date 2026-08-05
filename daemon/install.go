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

// runInstall writes host manifests for every browser detected on this machine.
func runInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	idCSV := fs.String("extension-id", "", "Chromium extension ID(s), comma separated (from chrome://extensions)")
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
		installWindowsNote(filepath.Join(ts[0].dir, hostName+".json"))
	}
	if !*dry {
		fmt.Println("\nReload the extension. The SidePanel should show \"Desktop app connected\".")
	}
	return 0
}

// installWindowsNote prints the registry commands. Registration on Windows is
// a registry value, not a file drop; the manifests are written but the keys
// must still be created.
func installWindowsNote(manifest string) {
	keys := []struct{ name, key string }{
		{"Chrome", `HKCU\Software\Google\Chrome\NativeMessagingHosts\` + hostName},
		{"Chromium", `HKCU\Software\Chromium\NativeMessagingHosts\` + hostName},
		{"Brave", `HKCU\Software\BraveSoftware\Brave-Browser\NativeMessagingHosts\` + hostName},
		{"Edge", `HKCU\Software\Microsoft\Edge\NativeMessagingHosts\` + hostName},
		{"Firefox", `HKCU\Software\Mozilla\NativeMessagingHosts\` + hostName},
	}
	fmt.Println("\nWindows: manifests written, but registration is a registry value.")
	fmt.Println("Run these for the browsers you use:")
	for _, k := range keys {
		fmt.Printf("  reg add \"%s\" /ve /t REG_SZ /d \"%s\" /f\n", k.key, manifest)
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
