// SPDX-License-Identifier: Apache-2.0
// WO-106: explicit platform requests select the tagged embedded config.
package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/keel-app/keel/daemon/bridge"
)

func callGetSelectors(t *testing.T, payload any) (platform string, selectors json.RawMessage, errp *bridge.ErrorPayload) {
	t.Helper()
	env, err := bridge.NewEnvelope("sel-1", "GET_SELECTORS", payload)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := handleGetSelectors(env, &buf); err != nil {
		t.Fatal(err)
	}
	framed, err := bridge.ReadMessage(&buf)
	if err != nil {
		t.Fatalf("response is not a validly framed message: %v", err)
	}
	got, err := bridge.ParseEnvelope(framed)
	if err != nil {
		t.Fatalf("response is not a valid envelope: %v", err)
	}
	switch got.Type {
	case "SELECTORS_RESULT":
		var p struct {
			Platform  string          `json:"platform"`
			Selectors json.RawMessage `json:"selectors"`
		}
		if err := json.Unmarshal(got.Payload, &p); err != nil {
			t.Fatalf("SELECTORS_RESULT payload did not decode: %v", err)
		}
		return p.Platform, p.Selectors, nil
	case "ERROR":
		var e bridge.ErrorPayload
		if err := json.Unmarshal(got.Payload, &e); err != nil {
			t.Fatalf("ERROR payload did not decode: %v", err)
		}
		return "", nil, &e
	default:
		t.Fatalf("got envelope type %q, want SELECTORS_RESULT or ERROR", got.Type)
		return "", nil, nil
	}
}

func sameJSON(t *testing.T, got, want []byte) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	gb, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	wb, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(gb, wb)
}

func TestHandleGetSelectorsSelectsEmbeddedPlatform(t *testing.T) {
	for _, platform := range []string{"yt", "tt"} {
		t.Run(platform, func(t *testing.T) {
			gotPlatform, gotSelectors, errp := callGetSelectors(t, map[string]any{"platform": platform})
			if errp != nil {
				t.Fatalf("explicit %s request failed: %+v", platform, errp)
			}
			if gotPlatform != platform {
				t.Fatalf("envelope platform = %q, want %q", gotPlatform, platform)
			}
			want := embeddedSelectors[platform]
			if !sameJSON(t, gotSelectors, want) {
				t.Fatalf("selectors for %s are not the embedded configuration", platform)
			}
			var inner struct {
				Platform string `json:"platform"`
			}
			if err := json.Unmarshal(gotSelectors, &inner); err != nil {
				t.Fatal(err)
			}
			if inner.Platform != platform {
				t.Fatalf("embedded config platform = %q, want %q", inner.Platform, platform)
			}
		})
	}
}

func TestHandleGetSelectorsEmptyPlatformKeepsYouTubeFallback(t *testing.T) {
	gotPlatform, gotSelectors, errp := callGetSelectors(t, map[string]any{})
	if errp != nil {
		t.Fatalf("empty-platform fallback failed: %+v", errp)
	}
	if gotPlatform != bridge.PlatformYouTube {
		t.Fatalf("empty platform = %q, want yt", gotPlatform)
	}
	if !sameJSON(t, gotSelectors, embeddedSelectors["yt"]) {
		t.Fatal("empty-platform fallback did not serve the YouTube embed")
	}
}
