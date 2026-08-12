// SPDX-License-Identifier: Apache-2.0
package store

import (
	"path/filepath"
	"testing"

	"github.com/keel-app/keel/daemon/bridge"
)

func TestScrollHistoryTikTokFields(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "h.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sound := "7654326938776914701"
	ch := "@sheadurazzo"
	name := "SHEA"
	if _, err := st.PutImpressions([]bridge.Impression{
		{
			PageLoadID: "11111111-1111-4111-8111-111111111111",
			ObservedAt: 1_700_000_000_100,
			Surface:    "HOME",
			Platform:   "tt",
			SlotIndex:  0,
			VideoID:    "7654326932623887630",
			ChannelID:  &ch,
			ChannelName: &name,
			Title:      "clip one",
			Badges:     []string{},
			Hashtags:   []string{"arianagrande", "fyp"},
			SoundID:    &sound,
		},
		{
			PageLoadID: "11111111-1111-4111-8111-111111111112",
			ObservedAt: 1_700_000_000_200,
			Surface:    "HOME",
			Platform:   "tt",
			SlotIndex:  1,
			VideoID:    "7662233635839298847",
			Title:      "clip two",
			Badges:     []string{},
			Hashtags:   []string{"fyp", "farming"},
		},
		// YouTube row must not appear in a tt history.
		{
			PageLoadID: "11111111-1111-4111-8111-111111111113",
			ObservedAt: 1_700_000_000_300,
			Surface:    "HOME",
			Platform:   "yt",
			SlotIndex:  0,
			VideoID:    "dQw4w9WgXcQ",
			Title:      "yt only",
			Badges:     []string{},
		},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := st.ScrollHistory("tt", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items=%d want 2: %+v", len(res.Items), res.Items)
	}
	// Chronological: oldest first.
	if res.Items[0].VideoID != "7654326932623887630" {
		t.Fatalf("order first=%s", res.Items[0].VideoID)
	}
	if res.HashtagCounts["fyp"] != 2 {
		t.Fatalf("fyp count=%d", res.HashtagCounts["fyp"])
	}
	if res.SoundCounts[sound] != 1 {
		t.Fatalf("sound count=%d", res.SoundCounts[sound])
	}
	yt, err := st.ScrollHistory("yt", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(yt.Items) != 1 || yt.Items[0].VideoID != "dQw4w9WgXcQ" {
		t.Fatalf("yt history %+v", yt.Items)
	}
}
