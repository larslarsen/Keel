// SPDX-License-Identifier: Apache-2.0
// The daemon's own record of what the user was told before the network started
// (WO-089).
//
// # Why the daemon holds this at all
//
// Recording consent already existed, in `chrome.storage`, where the content
// observer reads it to fail closed before sending anything. That is the right
// place for *that* decision and it stays there. What it cannot do is govern the
// daemon: the daemon is a separate long-lived process with its own lifecycle,
// it starts without any browser attached, and it is the process that actually
// opens network connections. A permission that lives only in a browser profile
// cannot gate a program that runs when no browser is open.
//
// So the boundary is here. No swarm node is constructed until this record says
// the user has seen and accepted the current network disclosure. Missing or
// stale means network-off, whatever the stored contribution level says.
//
// # Why it is revisioned rather than a boolean
//
// The disclosure itself changes. WO-089 is the case in point: builds before it
// told users the default level published live sightings and answered the word
// protocol, and the corrected default does neither. Someone who accepted the
// old sentence has not accepted the new one, and a boolean cannot tell those
// apart — it would silently carry an acceptance forward across a change in what
// was being accepted.
//
// Bumping NetworkConsentRevision therefore re-asks. That is the intended cost:
// a disclosure change should be visible to the people it describes, and the
// alternative is a consent record that means "agreed to something, once".
//
// The corpus is never touched by any of this. Declining, or being re-asked,
// takes the network away — it does not delete what the user recorded.
package store

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NetworkConsentRevision is the current network-data disclosure.
//
// Revision 1 (WO-089) is: Keel records locally, and at the default level it
// downloads the starter dataset, broad groups of shared recommendation data and
// the global word statistic, sending nothing derived from what the user was
// shown. Raise this only when that sentence stops being true, and update the
// consent screen in the same change — the number and the wording are one fact
// stored in two places, and the test suite checks they agree.
const NetworkConsentRevision = 2

const (
	// metaNetworkConsentKey holds the accepted revision.
	metaNetworkConsentKey = "network_consent_revision"
	// metaNetworkConsentAtKey holds when it was accepted, in unix millis.
	//
	// Kept for the interface and for support questions ("when did I agree to
	// this?"). It is not part of the gate: an acceptance does not expire on a
	// timer, only on a revision change.
	metaNetworkConsentAtKey = "network_consent_at"
)

// NetworkConsent is what the daemon knows about the user's network decision.
type NetworkConsent struct {
	// Revision accepted, or 0 when nothing has been accepted.
	Revision int `json:"revision"`
	// Required is the revision this build asks for.
	Required int `json:"required"`
	// AcceptedAt is unix millis, or 0.
	AcceptedAt int64 `json:"accepted_at"`
	// Current is the gate: Revision >= Required.
	//
	// A record *ahead* of this build satisfies it deliberately. That is a
	// downgraded daemon reading a newer database, and the user has by then
	// seen a disclosure at least as complete as this build's. Refusing there
	// would take the network away from someone who has agreed to more, not
	// less.
	Current bool `json:"current"`
}

// NetworkConsent reads the stored decision.
//
// Every unreadable or unparseable state answers "not accepted". An
// unreadable consent record must never be interpreted as permission — the
// same rule ContributionLevel follows for the same reason.
func (s *Store) NetworkConsent() NetworkConsent {
	c := NetworkConsent{Required: NetworkConsentRevision}
	var v string
	if err := s.db.QueryRow(
		`SELECT value FROM meta WHERE key = ?`, metaNetworkConsentKey).Scan(&v); err != nil {
		return c
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return c
	}
	c.Revision = n
	c.Current = n >= NetworkConsentRevision

	var at string
	if err := s.db.QueryRow(
		`SELECT value FROM meta WHERE key = ?`, metaNetworkConsentAtKey).Scan(&at); err == nil {
		if ms, err := strconv.ParseInt(strings.TrimSpace(at), 10, 64); err == nil {
			c.AcceptedAt = ms
		}
	}
	return c
}

// GrantNetworkConsent records acceptance of a disclosure revision.
//
// The revision is an argument rather than assumed, so the caller states which
// disclosure it actually showed. A client that has not been updated cannot
// accidentally accept a revision it never rendered — it would have to name a
// number it does not know.
//
// Revisions ahead of this build are refused: a browser claiming to have shown
// disclosure 7 to a daemon that only knows 1 is a version mismatch, and
// storing it would permanently satisfy every future gate on this database.
//
// Revisions behind this build's requirement are refused too (WO-110). A
// browser still rendering an old disclosure sending {accepted:true,
// revision:1} against a daemon that requires 2 is the same mixed-version case
// in the other direction, and accepting it would record agreement to words the
// browser never displayed, then report success while the gate stays closed —
// the defect this fixes. The caller must not write the meta rows, touch
// network_consent_at, or treat this as anything but a refusal.
func (s *Store) GrantNetworkConsent(revision int) (NetworkConsent, error) {
	if revision < 1 {
		return s.NetworkConsent(), fmt.Errorf("consent revision must be positive")
	}
	if revision > NetworkConsentRevision {
		return s.NetworkConsent(), fmt.Errorf(
			"consent revision %d is newer than this desktop app understands (%d); update it",
			revision, NetworkConsentRevision)
	}
	if revision < NetworkConsentRevision {
		return s.NetworkConsent(), fmt.Errorf(
			"consent revision %d predates the current disclosure (%d); update the browser extension",
			revision, NetworkConsentRevision)
	}
	// Never lower an existing acceptance. This is now only reachable when the
	// stored revision is *ahead* of this build's own NetworkConsentRevision — a
	// downgraded daemon reading a newer database (see NetworkConsent.Current) —
	// since any revision behind the requirement was already refused above.
	if cur := s.NetworkConsent(); cur.Revision > revision {
		return cur, nil
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return s.NetworkConsent(), err
	}
	defer tx.Rollback()
	for _, kv := range [][2]string{
		{metaNetworkConsentKey, strconv.Itoa(revision)},
		{metaNetworkConsentAtKey, strconv.FormatInt(now, 10)},
	} {
		if _, err := tx.Exec(
			`INSERT INTO meta(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			kv[0], kv[1]); err != nil {
			return s.NetworkConsent(), err
		}
	}
	if err := tx.Commit(); err != nil {
		return s.NetworkConsent(), err
	}
	return s.NetworkConsent(), nil
}

// WithdrawNetworkConsent clears the record, which stops the network.
//
// Deliberately does not touch the corpus or the contribution level. Withdrawing
// permission to use the network is not a request to delete what was recorded
// locally, and conflating them would make the safer choice the more destructive
// one. `WIPE` remains the way to delete data.
func (s *Store) WithdrawNetworkConsent() error {
	_, err := s.db.Exec(
		`DELETE FROM meta WHERE key IN (?, ?)`,
		metaNetworkConsentKey, metaNetworkConsentAtKey)
	return err
}
