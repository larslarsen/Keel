// SPDX-License-Identifier: Apache-2.0
package store

import (
	"path/filepath"
	"testing"
)

func openConsentStore(t *testing.T, name string) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestNetworkConsentStartsUnaccepted(t *testing.T) {
	st := openConsentStore(t, "fresh.sqlite")
	c := st.NetworkConsent()
	if c.Current || c.Revision != 0 {
		t.Fatalf("fresh store consent = %+v, want unaccepted", c)
	}
	if c.Required != NetworkConsentRevision {
		t.Errorf("required = %d, want %d", c.Required, NetworkConsentRevision)
	}
}

func TestGrantAndWithdrawNetworkConsent(t *testing.T) {
	st := openConsentStore(t, "roundtrip.sqlite")
	got, err := st.GrantNetworkConsent(NetworkConsentRevision)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Current || got.Revision != NetworkConsentRevision || got.AcceptedAt == 0 {
		t.Fatalf("granted consent = %+v", got)
	}
	if err := st.WithdrawNetworkConsent(); err != nil {
		t.Fatal(err)
	}
	if st.NetworkConsent().Current {
		t.Fatal("consent survived withdrawal")
	}
}

func TestGrantNetworkConsentRejectsUnknownRevision(t *testing.T) {
	st := openConsentStore(t, "future.sqlite")
	if _, err := st.GrantNetworkConsent(NetworkConsentRevision + 1); err == nil {
		t.Fatal("accepted a revision this build does not know")
	}
	if st.NetworkConsent().Current {
		t.Fatal("a refused grant opened the gate")
	}
}

func TestWipeDoesNotClearNetworkConsent(t *testing.T) {
	st := openConsentStore(t, "wipe.sqlite")
	if _, err := st.GrantNetworkConsent(NetworkConsentRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Wipe(); err != nil {
		t.Fatal(err)
	}
	if !st.NetworkConsent().Current {
		t.Fatal("wipe cleared network consent; the corpus and the gate are different permissions")
	}
}
