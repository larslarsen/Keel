// SPDX-License-Identifier: Apache-2.0
// Extraction selectors, served to the extension (WO-056).
//
// DESIGN_BOOTSTRAP's Option B: the extension keeps the parsing engine and holds
// no selectors of its own, so when YouTube renames a class the daemon ships a
// new config and the extension binary does not change. "The rot moves to the
// daemon, which is exactly where Lars wants it."
//
// **The line:** the extension may download data, never logic. What travels here
// is CSS selector strings and nothing else — no regexes, no expressions, no
// branching. The extension validates before use and rejects the whole config on
// any violation, so a daemon that ships something unexpected cannot make the
// extension behave in a way a store reviewer could not predict from its source.
//
// **The honest limit.** This covers selector-level change, which is most of it.
// It does not cover structural change: if cards move into shadow roots, or stop
// being elements the compiled behaviours can walk, no config fixes that and the
// extension has to be republished. The claim is "most breaks are config-only",
// not "never again".
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/keel-app/keel/daemon/bridge"
)

//go:embed selectors_yt.json
var youtubeSelectors []byte

// selectorOverridePath is where an operator can drop a replacement without
// rebuilding — the point of moving the rot here is that fixing YouTube should
// not need a compiler.
func selectorOverridePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "keel", "selectors_yt.json")
}

// selectorConfig returns the config to serve.
//
// An override file wins if it parses as JSON; a broken one is ignored rather
// than served, because shipping a malformed config would stop extraction
// everywhere it reached. The extension validates it again on arrival — this is
// a sanity check, not the security boundary.
func selectorConfig() json.RawMessage {
	if p := selectorOverridePath(); p != "" {
		if raw, err := os.ReadFile(p); err == nil {
			if json.Valid(raw) {
				return json.RawMessage(raw)
			}
			fmt.Fprintf(os.Stderr, "keel: ignoring malformed %s\n", p)
		}
	}
	return json.RawMessage(youtubeSelectors)
}

// handleGetSelectors answers GET_SELECTORS.
func handleGetSelectors(env *bridge.Envelope, out io.Writer) error {
	return reply(out, env.ID, "SELECTORS_RESULT", map[string]any{
		"platform":  "yt",
		"selectors": selectorConfig(),
	})
}
