// SPDX-License-Identifier: Apache-2.0
// The file a Windows user opens instead of typing commands (WO-091).
//
// The machine this order came from cannot be diagnosed through a terminal: the
// install is a double-click, and the console window goes away with the process.
// So every real Windows install leaves install-report.txt beside the executable,
// written as it goes, holding the whole registry → manifest → executable chain
// and the first thing that broke it.
//
// The report carries paths, browser names and registry keys only. No corpus, no
// observations, no peers, no queries, no credentials — it is written to be
// pasted into an issue by someone who cannot check it first.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const installReportName = "install-report.txt"

type installReport struct {
	w        io.Writer
	c        io.Closer
	path     string
	first    string // first actionable error
	failed   bool
	finished bool
}

// discardedReport is the no-op report used off Windows and during -dry-run, so
// the install path has no "if there is a report" branches in it.
func discardedReport() *installReport { return &installReport{w: io.Discard} }

// openInstallReport creates the report before anything else can fail, so an
// early failure is captured too. A report that cannot be created is itself an
// install failure: the user would have no way to see any other one.
func openInstallReport(dir string) (*installReport, error) {
	path := filepath.Join(dir, installReportName)
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("cannot write %s: %w", path, err)
	}
	return &installReport{w: f, c: f, path: path}, nil
}

func (r *installReport) line(format string, a ...any) {
	fmt.Fprintf(r.w, format+"\n", a...)
}

// fail records the first actionable error and prints it in place.
func (r *installReport) fail(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	r.line("FAILED         %s", msg)
	if !r.failed {
		r.failed, r.first = true, msg
	}
}

// registry records one browser key: what it was meant to hold, what it holds.
func (r *installReport) registry(res registryResult) {
	if res.err != nil {
		r.line("registry %-9s FAILED   %s", res.browser, res.key)
		r.line("               expected %s", res.expected)
		if res.observed != "" {
			r.line("               observed %s", res.observed)
		}
		r.fail("%s: %v", res.browser, res.err)
		return
	}
	r.line("registry %-9s OK       %s", res.browser, res.key)
	r.line("               value    %s", res.observed)
}

// finish states the verdict. Called explicitly at the end of a complete run;
// close() supplies it for a run that returned early.
func (r *installReport) finish(ok bool) {
	if r.finished {
		return
	}
	r.finished = true
	if !ok || r.failed {
		r.line("")
		r.line("RESULT: FAILED")
		if r.first != "" {
			r.line("First error: %s", r.first)
		}
		r.line("")
		r.line("Send this file to https://github.com/larslarsen/Keel/issues — it contains")
		r.line("paths and browser registry keys only, nothing you have watched or searched.")
		return
	}
	r.line("")
	r.line("RESULT: SUCCESS")
	r.line("Reload the extension; the Keel panel should say \"Desktop app connected\".")
}

func (r *installReport) close() {
	r.finish(false) // no-op once finish has already run
	if r.c != nil {
		_ = r.c.Close()
	}
}
