// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"testing"
	"time"
)

// The stored times drop trailing zeros, so as strings they do not sort as
// times. Each pair here was ordered wrongly by the string comparison this
// replaced.
func TestAtOrAfterComparesTimes(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		stored, since string
		want          bool
	}{
		{"2026-10-17T10:00:07Z", "2026-10-17T10:00:07.1Z", false},       // 'Z' sorted after '.'
		{"2026-10-17T10:00:07.1Z", "2026-10-17T10:00:07.10001Z", false}, // and after '0'
		{"2026-10-17T10:00:07.10001Z", "2026-10-17T10:00:07.1Z", true},
		{"2026-10-17T10:00:07.5Z", "2026-10-17T10:00:07Z", true},
		{"2026-10-17T10:00:08Z", "2026-10-17T10:00:07.999999999Z", true},
		{"2026-10-17T10:00:07Z", "2026-10-17T10:00:07Z", true},
		{"not a time", "2026-10-17T10:00:07Z", false},
	} {
		if got := atOrAfter(c.stored, at(c.since)); got != c.want {
			t.Errorf("atOrAfter(%s, %s) = %v, want %v", c.stored, c.since, got, c.want)
		}
	}
}

func TestHasRecentEvent(t *testing.T) {
	s := testStore(t)
	before := time.Now().Add(-time.Second)
	s.Audit(EvFailoverPromoted, "watch", "", "", "test")
	if !s.HasRecentEvent(EvFailoverPromoted, before) {
		t.Error("an event after the time was not found")
	}
	if s.HasRecentEvent(EvFailoverPromoted, time.Now().Add(time.Second)) {
		t.Error("an event before the time was found")
	}
	if s.HasRecentEvent(EvFailoverRefused, before) {
		t.Error("an event of another kind was found")
	}
}
