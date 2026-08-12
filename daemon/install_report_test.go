// SPDX-License-Identifier: Apache-2.0
// WO-091: the install report is the only diagnostic available on a machine
// where nothing can be typed, so it has to survive an early failure and it has
// to be safe to hand to a stranger.
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestReport(t *testing.T) (*installReport, func() string) {
	t.Helper()
	dir := t.TempDir()
	r, err := openInstallReport(dir)
	if err != nil {
		t.Fatalf("openInstallReport: %v", err)
	}
	return r, func() string {
		b, err := os.ReadFile(filepath.Join(dir, installReportName))
		if err != nil {
			t.Fatalf("read report: %v", err)
		}
		return string(b)
	}
}

func TestInstallReportRecordsTheWholeChain(t *testing.T) {
	r, read := newTestReport(t)
	r.line("Keel install report")
	r.line("host version   %s (built %s)", version, "2026-08-12 10:00:00")
	r.line("executable     %s", `C:\keel\keel-host.exe`)
	r.line("manifest %-9s OK       %s", "Brave", chromiumManifestPath)
	r.line("manifest %-9s OK       %s", "Firefox", firefoxManifestPath)
	f := newFakeRegistry()
	for _, res := range installWindowsRegistry(f.run, chromiumManifestPath, firefoxManifestPath) {
		r.registry(res)
	}
	r.finish(true)
	r.close()

	got := read()
	for _, want := range []string{
		"RESULT: SUCCESS",
		version,
		`C:\keel\keel-host.exe`,
		chromiumManifestPath,
		firefoxManifestPath,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	for _, target := range windowsRegistryTargets() {
		if !strings.Contains(got, target.key) {
			t.Errorf("report is missing the %s registry key:\n%s", target.browser, got)
		}
	}
	if strings.Contains(got, "FAILED") {
		t.Errorf("a clean install reported a failure:\n%s", got)
	}
}

// TestInstallReportSurvivesAnEarlyFailure: the report is opened before anything
// can go wrong and closed by defer, so a run that returns on line one still
// leaves a readable verdict.
func TestInstallReportSurvivesAnEarlyFailure(t *testing.T) {
	r, read := newTestReport(t)
	r.line("Keel install report")
	r.fail("owner credential: %v", errors.New("permission denied"))
	r.close() // the deferred close, with no finish() — an early return

	got := read()
	for _, want := range []string{"RESULT: FAILED", "First error: owner credential: permission denied"} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "SUCCESS") {
		t.Errorf("a failed install reported success:\n%s", got)
	}
}

// TestInstallReportKeepsTheFirstError: the first thing that broke is the one
// worth acting on. Later failures are usually consequences of it.
func TestInstallReportKeepsTheFirstError(t *testing.T) {
	r, read := newTestReport(t)
	r.fail("registry Brave: key holds a stale path")
	r.fail("extension: not found")
	r.close()

	got := read()
	if !strings.Contains(got, "First error: registry Brave: key holds a stale path") {
		t.Errorf("wrong first error:\n%s", got)
	}
}

func TestRegistryFailureIsRecordedWithBothValues(t *testing.T) {
	f := newFakeRegistry()
	f.stored[braveKey()] = `C:\stale\com.keel.host.json`
	r, read := newTestReport(t)
	for _, res := range installWindowsRegistry(f.run, chromiumManifestPath, firefoxManifestPath) {
		r.registry(res)
	}
	r.close()

	got := read()
	for _, want := range []string{"RESULT: FAILED", chromiumManifestPath, `C:\stale\com.keel.host.json`, braveKey()} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
}

// TestInstallReportCarriesNothingPrivate. The report exists to be pasted into a
// public issue by someone who cannot inspect it first, so it may only ever hold
// paths, browser names and registry keys.
func TestInstallReportCarriesNothingPrivate(t *testing.T) {
	r, read := newTestReport(t)
	r.line("Keel install report")
	r.line("host version   %s (built %s)", version, "2026-08-12 10:00:00")
	r.line("executable     %s", `C:\keel\keel-host.exe`)
	f := newFakeRegistry()
	f.stored[braveKey()] = `C:\stale\com.keel.host.json`
	for _, res := range installWindowsRegistry(f.run, chromiumManifestPath, firefoxManifestPath) {
		r.registry(res)
	}
	r.line("extension      extension folder not found; host registration completed")
	r.close()

	got := strings.ToLower(read())
	// Names of things the report must never learn how to reach.
	for _, forbidden := range []string{
		"keel.sqlite", "secret", "credential", "token", "peer", "node-", "query",
		"video", "watch?v=", "impression", "corpus", "sketch", "bundle",
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("report mentions %q:\n%s", forbidden, got)
		}
	}
}

func TestDiscardedReportIsSafeToUse(t *testing.T) {
	r := discardedReport()
	r.line("anything")
	r.fail("and a failure")
	r.finish(false)
	r.close() // must not panic without a file behind it
}
