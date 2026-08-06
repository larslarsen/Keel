// SPDX-License-Identifier: Apache-2.0
// Remembering peers that worked (WO-052, DESIGN_v2 §7.4a).
//
// The public DHT can be censored — GO-2024-3218, no fix available — by flooding
// provider records so a key becomes undiscoverable. Only discovery breaks: a
// node that already knows who holds something can ask directly, and the block
// protocol needs no DHT at all.
//
// So a node keeps the peers that actually served it verified data, and falls
// back to them when discovery returns nothing. A censored lookup then costs
// latency rather than the data.
//
// Only successful peers are recorded. A list of everyone ever contacted would be
// mostly noise and a larger thing to hold; a list of who worked is small and is
// exactly what the fallback needs.
package store

import (
	"strings"
	"time"
)

// MaxKnownPeers bounds the table. Enough to survive a censored bucket, small
// enough that the fallback is a short loop rather than a scan of the network.
const MaxKnownPeers = 64

// KnownPeer is a peer that has served this node verified data.
type KnownPeer struct {
	ID    string
	Addrs []string
}

// RememberPeer records a peer that just served something that verified.
//
// Addresses are replaced rather than merged: a peer that moved should be
// reachable at where it is now, and stale multiaddrs only slow the fallback
// down.
func (s *Store) RememberPeer(id string, addrs []string) error {
	if id == "" || len(addrs) == 0 {
		return nil
	}
	if _, err := s.db.Exec(`
INSERT INTO known_peers(peer_id, addrs, last_ok, successes)
VALUES(?,?,?,1)
ON CONFLICT(peer_id) DO UPDATE SET
  addrs = excluded.addrs,
  last_ok = excluded.last_ok,
  successes = known_peers.successes + 1`,
		id, strings.Join(addrs, "\n"), time.Now().UnixMilli()); err != nil {
		return err
	}
	// Trim by usefulness, not age alone: a peer that has served many times is
	// worth keeping over one that answered once recently.
	_, err := s.db.Exec(`
DELETE FROM known_peers WHERE peer_id NOT IN (
  SELECT peer_id FROM known_peers ORDER BY successes DESC, last_ok DESC LIMIT ?
)`, MaxKnownPeers)
	return err
}

// KnownPeers returns remembered peers, most useful first.
func (s *Store) KnownPeers(limit int) ([]KnownPeer, error) {
	if limit <= 0 || limit > MaxKnownPeers {
		limit = MaxKnownPeers
	}
	rows, err := s.db.Query(`
SELECT peer_id, addrs FROM known_peers
ORDER BY successes DESC, last_ok DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KnownPeer{}
	for rows.Next() {
		var p KnownPeer
		var joined string
		if err := rows.Scan(&p.ID, &joined); err != nil {
			return nil, err
		}
		for _, a := range strings.Split(joined, "\n") {
			if a != "" {
				p.Addrs = append(p.Addrs, a)
			}
		}
		if len(p.Addrs) > 0 {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}
