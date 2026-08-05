// SPDX-License-Identifier: Apache-2.0
// Cohort storage (WO-029).
//
// DESIGN_v2 §6.3: country plus interface language, and nothing else. v1's
// "region + interest drift" must not be built — interest drift is a
// behavioural fingerprint and attaching it to funnel edges would undo the
// privacy design.
//
// The value is supplied by the browser's own locale. We never IP-geolocate,
// and the daemon never infers it from anything it observes.
package store

import (
	"fmt"
	"regexp"
	"strings"
)

// cohortRe accepts "US-en" style values only: two-letter region, dash, two or
// three letter language. Anything richer is rejected rather than stored,
// because the whole guarantee is that this field stays low-dimensional.
var cohortRe = regexp.MustCompile(`^[A-Z]{2}-[a-z]{2,3}$`)

// NormalizeCohort turns a BCP-47 tag such as "en-GB" into "GB-en".
//
// Returns "unknown" rather than guessing when the tag carries no region — an
// honest placeholder is better than a fabricated country.
func NormalizeCohort(tag string) string {
	t := strings.TrimSpace(tag)
	if t == "" {
		return "unknown"
	}
	parts := strings.Split(strings.ReplaceAll(t, "_", "-"), "-")
	lang := strings.ToLower(parts[0])
	if len(lang) < 2 || len(lang) > 3 {
		return "unknown"
	}
	for _, p := range parts[1:] {
		if len(p) == 2 && strings.ToUpper(p) == p || len(p) == 2 && isAlpha(p) {
			region := strings.ToUpper(p)
			c := fmt.Sprintf("%s-%s", region, lang)
			if cohortRe.MatchString(c) {
				return c
			}
		}
	}
	return "unknown"
}

func isAlpha(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

// SetCohort stores the browser-reported cohort.
func (s *Store) SetCohort(tag string) (string, error) {
	c := NormalizeCohort(tag)
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES('cohort', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, c); err != nil {
		return "", err
	}
	return c, nil
}

// Cohort returns the stored cohort, or "unknown".
func (s *Store) Cohort() string {
	var c string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'cohort'`).Scan(&c); err != nil || c == "" {
		return "unknown"
	}
	return c
}
