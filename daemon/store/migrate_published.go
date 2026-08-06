// SPDX-License-Identifier: Apache-2.0
// One-time repair of published_at values.
//
// The extractor used to store whole metadata lines, so rows carry things like
// "Liberal Hivemind 22K 1h ago" and "Streamed 2h ago" where an age belongs —
// 27% of a live corpus. The extractor now takes only the age, but rows already
// written keep the old value, and the panel displays this field, so they would
// look broken forever.
package store

import "regexp"

// ageTail matches a trailing age, the same shape the extractor now keeps.
var ageTail = regexp.MustCompile(`(?i)(\d+\s*[a-z]{1,6}\s+ago)\s*$`)

const metaPublishedRepair = "published_at_repaired_v1"

// repairPublishedAt rewrites malformed values in place, once.
//
// Rows whose age cannot be recovered are left alone rather than blanked: a
// wrong-looking value the user can see beats silently discarding data, and the
// row is still a valid observation in every other respect.
func (s *Store) repairPublishedAt() error {
	var done string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`,
		metaPublishedRepair).Scan(&done); err == nil && done == "1" {
		return nil
	}

	rows, err := s.db.Query(`
SELECT DISTINCT published_at FROM impressions
WHERE published_at IS NOT NULL AND published_at != ''`)
	if err != nil {
		return err
	}
	fixes := map[string]string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		m := ageTail.FindStringSubmatch(v)
		if m == nil || m[1] == v {
			continue // unrecoverable, or already clean
		}
		fixes[v] = m[1]
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for from, to := range fixes {
		if _, err := tx.Exec(
			`UPDATE impressions SET published_at = ? WHERE published_at = ?`, to, from); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO meta(key, value) VALUES(?, '1')
		 ON CONFLICT(key) DO UPDATE SET value = '1'`, metaPublishedRepair); err != nil {
		return err
	}
	return tx.Commit()
}
