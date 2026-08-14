// SPDX-License-Identifier: Apache-2.0
package bridge

import (
	"strings"
	"testing"
	"time"
)

func testLiveSighting(surface, title string) LiveSighting {
	return LiveSighting{
		PageLoadID:  "11111111-1111-4111-8111-111111111111",
		ObservedAt:  time.Now().UnixMilli(),
		Surface:     surface,
		SlotIndex:   0,
		Platform:    "tt",
		LiveLocator: "@creator/live",
		ChannelID:   "@creator",
		ChannelName: "Creator",
		Title:       title,
		Badges:      []string{},
	}
}

func TestValidateLiveSightingRevisionTitleRules(t *testing.T) {
	room := testLiveSighting("LIVE_ROOM", "")
	if err := ValidateLiveSighting(&room, 1); err == nil {
		t.Fatal("revision 1 must reject a titleless LIVE_ROOM")
	}
	if err := ValidateLiveSighting(&room, LiveSightingsRevTitlelessRoom); err != nil {
		t.Fatalf("revision 2 must accept a titleless LIVE_ROOM: %v", err)
	}

	wall := testLiveSighting("LIVE", "")
	if err := ValidateLiveSighting(&wall, LiveSightingsRevTitlelessRoom); err == nil {
		t.Fatal("revision 2 must still require a title on LIVE wall cards")
	}
	wall.Title = "On air"
	if err := ValidateLiveSighting(&wall, 1); err != nil {
		t.Fatalf("revision 1 must still accept a titled LIVE card: %v", err)
	}
}

func TestNegotiateLiveSightingsTitlelessRoom(t *testing.T) {
	if got := DaemonCaps()[CapLiveSightings]; got != LiveSightingsRevTitlelessRoom {
		t.Fatalf("daemon offers live_sightings:%d, want %d", got, LiveSightingsRevTitlelessRoom)
	}

	current := NegotiateHello(HelloPayload{
		ClientVersion: "0.1.0",
		API:           &APIRange{Min: 1, Max: 1},
		Required:      map[string]int{CapCore: 1},
		Optional:      map[string]int{CapLiveSightings: LiveSightingsRevTitlelessRoom},
	}, "0.1.0")
	if got := current.Capabilities[CapLiveSightings]; got != LiveSightingsRevTitlelessRoom {
		t.Fatalf("current pair negotiated live_sightings:%d", got)
	}

	old := NegotiateHello(HelloPayload{
		ClientVersion: "0.1.0",
		API:           &APIRange{Min: 1, Max: 1},
		Required:      map[string]int{CapCore: 1},
		Optional:      map[string]int{CapLiveSightings: 1},
	}, "0.1.0")
	if !old.Compatible {
		t.Fatalf("a revision-1 Live client must still connect: %+v", old)
	}
	if got := old.Capabilities[CapLiveSightings]; got != 1 {
		t.Fatalf("revision-1 client negotiated live_sightings:%d, want 1", got)
	}
	if strings.Contains(old.Reason, "live_sightings") && !old.Compatible {
		t.Fatal("live_sightings is optional and must not fail HELLO")
	}
}
