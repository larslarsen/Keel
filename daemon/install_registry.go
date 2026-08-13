// SPDX-License-Identifier: Apache-2.0
// Windows native-messaging registration (WO-091).
//
// Windows finds a native-messaging host through a registry value, not through a
// file in a well-known directory, so writing the manifests is only half the job.
// This file does the other half and then proves it: after every write the same
// key is read back and compared with the path that was meant to be there. A
// registration that was not verified is not a registration — the reported
// failure ("Specified native messaging host not found") looks identical whether
// the key is missing, points somewhere else, or points at the wrong schema.
//
// No build tag: the plan and its verification are ordinary Go and are tested on
// every platform through an injected command runner.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// cmdRunner runs one external command and returns its combined output. The
// installer holds this rather than calling exec directly so registry success,
// a missing key, a wrong value and a failing `reg` can all be exercised on a CI
// host with no registry.
type cmdRunner func(name string, args ...string) ([]byte, error)

func execRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// registryTarget is one browser's HKCU key. HKCU, so no administrator rights are
// needed; a browser that is not installed still gets its key, which is harmless
// because only that browser ever reads it.
type registryTarget struct {
	browser string
	key     string
	firefox bool
}

// windowsRegistryTargets lists every key a supported browser might read.
//
// Each release channel is a separate product with its own registry root:
// Brave Beta reads Brave-Browser-Beta, Chrome Canary reads "Chrome SxS", and so
// on. Registering only the stable roots means an install can verify perfectly —
// key present, value correct, manifest decoding, executable there — while the
// browser in front of the user looks somewhere else entirely and reports
// "Specified native messaging host not found". That failure is indistinguishable
// from every other cause, and nothing on the machine points at the channel.
//
// Writing the extra keys costs nothing: they live under HKCU, only the browser
// that owns one ever reads it, and an unread key is inert.
func windowsRegistryTargets() []registryTarget {
	chromium := []struct{ browser, root string }{
		{"Chrome", `Google\Chrome`},
		{"Chrome Beta", `Google\Chrome Beta`},
		{"Chrome Dev", `Google\Chrome Dev`},
		{"Chrome Canary", `Google\Chrome SxS`},
		{"Chromium", `Chromium`},
		{"Brave", `BraveSoftware\Brave-Browser`},
		{"Brave Beta", `BraveSoftware\Brave-Browser-Beta`},
		{"Brave Nightly", `BraveSoftware\Brave-Browser-Nightly`},
		{"Brave Dev", `BraveSoftware\Brave-Browser-Dev`},
		{"Edge", `Microsoft\Edge`},
		{"Edge Beta", `Microsoft\Edge Beta`},
		{"Edge Dev", `Microsoft\Edge Dev`},
		{"Edge Canary", `Microsoft\Edge SxS`},
	}
	out := make([]registryTarget, 0, len(chromium)+1)
	for _, c := range chromium {
		out = append(out, registryTarget{c.browser, `HKCU\Software\` + c.root + `\NativeMessagingHosts\` + hostName, false})
	}
	// Firefox and its forks all read the one Mozilla root.
	out = append(out, registryTarget{"Firefox", `HKCU\Software\Mozilla\NativeMessagingHosts\` + hostName, true})
	return out
}

// registryResult is what one key ended up holding, and why that is or is not
// what was intended.
type registryResult struct {
	browser  string
	key      string
	expected string
	observed string
	err      error
}

// installWindowsRegistry points every supported browser at the manifest for its
// own schema, then reads each key back.
//
// chromium and firefox are the two manifest paths the install actually wrote. An
// empty one means nothing was written for that family, which is a failure rather
// than something to register anyway: a key pointing at a file that does not
// exist is the exact symptom this order was raised for.
func installWindowsRegistry(run cmdRunner, chromium, firefox string) []registryResult {
	out := make([]registryResult, 0, 5)
	for _, t := range windowsRegistryTargets() {
		want := chromium
		if t.firefox {
			want = firefox
		}
		r := registryResult{browser: t.browser, key: t.key, expected: want}
		if want == "" {
			r.err = fmt.Errorf("no host manifest was written for %s", t.browser)
			out = append(out, r)
			continue
		}
		if o, err := run("reg", "add", t.key, "/ve", "/t", "REG_SZ", "/d", want, "/f"); err != nil {
			r.err = fmt.Errorf("reg add failed: %v: %s", err, firstLine(o))
			out = append(out, r)
			continue
		}
		observed, err := readRegistryDefault(run, t.key)
		if err != nil {
			r.err = err
			out = append(out, r)
			continue
		}
		r.observed = observed
		if normalizeWindowsPath(observed) != normalizeWindowsPath(want) {
			r.err = fmt.Errorf("key holds %q, want %q", observed, want)
		}
		out = append(out, r)
	}
	return out
}

// uninstallWindowsRegistry removes the keys this installer owns and confirms
// each one is gone. A key that never existed is already in the desired state.
func uninstallWindowsRegistry(run cmdRunner) []registryResult {
	out := make([]registryResult, 0, 5)
	for _, t := range windowsRegistryTargets() {
		r := registryResult{browser: t.browser, key: t.key}
		delOut, delErr := run("reg", "delete", t.key, "/f")
		if _, err := readRegistryDefault(run, t.key); err == nil {
			r.err = fmt.Errorf("key still present after delete: %v: %s", delErr, firstLine(delOut))
		}
		out = append(out, r)
	}
	return out
}

// readRegistryDefault returns the key's default value.
func readRegistryDefault(run cmdRunner, key string) (string, error) {
	out, err := run("reg", "query", key, "/ve")
	if err != nil {
		return "", fmt.Errorf("key not found: %v: %s", err, firstLine(out))
	}
	v, ok := regQueryDefault(out)
	if !ok {
		return "", fmt.Errorf("key has no REG_SZ default value: %s", firstLine(out))
	}
	return v, nil
}

// regQueryDefault extracts the default value out of `reg query <key> /ve`.
//
// The value's *name* is localized — "(Default)", "(Standard)", "(Par défaut)" —
// and the header line repeats the key, so the type marker is the only stable
// anchor in that output. Everything after it on the line is the value; the path
// itself may contain spaces, so it is not split on whitespace.
func regQueryDefault(out []byte) (string, bool) {
	for _, line := range strings.Split(string(out), "\n") {
		i := strings.Index(line, "REG_SZ")
		if i < 0 {
			continue
		}
		if v := strings.TrimSpace(line[i+len("REG_SZ"):]); v != "" {
			return v, true
		}
	}
	return "", false
}

// normalizeWindowsPath makes two spellings of the same Windows path comparable:
// case is insignificant, separators may be either slash, and repeated
// separators collapse. A leading UNC "\\" is significant and is kept.
func normalizeWindowsPath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), `"`)
	p = strings.ReplaceAll(p, "/", `\`)
	prefix := ""
	if strings.HasPrefix(p, `\\`) {
		prefix, p = `\\`, strings.TrimLeft(p, `\`)
	}
	for strings.Contains(p, `\\`) {
		p = strings.ReplaceAll(p, `\\`, `\`)
	}
	return strings.ToLower(prefix + strings.TrimSuffix(p, `\`))
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// repairManifestAccess restores inherited permissions on the directory holding
// the native-host manifests.
//
// A browser reads com.keel.host.json itself. Anything that removes inherited
// access from that directory therefore uninstalls native messaging without
// touching the registry, and the browser reports exactly what a missing key
// reports: "Specified native messaging host not found".
//
// Keel did this to itself. A state-directory fallback briefly treated
// %LOCALAPPDATA%\Keel as a place for private data and applied a PROTECTED DACL
// to it, which strips inheritance. Machines that ran that build still have the
// locked directory; nothing about installing a corrected binary would fix it,
// because the damage is on disk rather than in the code.
//
// So the installer repairs it every time: icacls /reset restores the inherited
// ACLs from the parent, which is what an ordinary per-user directory should
// have had all along. It is idempotent and harmless on a healthy machine.
// Best effort by contract: see the call site. A machine where neither attempt
// works may still have a perfectly readable directory, and refusing to register
// there would turn a maybe into a certainly-broken install.
func repairManifestAccess(run cmdRunner, dir string) error {
	if dir == "" {
		return nil
	}
	out, err := run("icacls", dir, "/reset", "/t", "/q")
	if err == nil {
		return nil
	}
	first := fmt.Errorf("%v: %s", err, firstLine(out))

	// /reset rewrites the ACL wholesale and needs WRITE_DAC on every child. A
	// narrower grant to the current user often succeeds where that does not, and
	// read access for this user is the only thing actually required.
	user := os.Getenv("USERNAME")
	if domain := os.Getenv("USERDOMAIN"); domain != "" && user != "" {
		user = domain + "\\" + user
	}
	var second error
	if user != "" {
		if _, err2 := run("icacls", dir, "/grant", user+":(OI)(CI)F", "/t", "/q"); err2 == nil {
			return nil
		} else {
			second = err2
		}
	}

	// Last resort: a DACL Keel itself applied can leave the directory in a state
	// where its own owner cannot rewrite the ACL. Taking ownership back is what
	// the Security tab's "Change owner" does, and it only ever runs against a
	// directory Keel created and broke.
	if _, err3 := run("takeown", "/f", dir, "/r", "/d", "y"); err3 == nil {
		if _, err4 := run("icacls", dir, "/reset", "/t", "/q"); err4 == nil {
			return nil
		}
	}
	return fmt.Errorf("could not restore access on %s (reset: %v; grant: %v)", dir, first, second)
}
