// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/thefoxbyte/foxbyte/internal/license"
)

// What a machine that has just become a standby is told about its licence.
//
// The state that matters is an *absent* licence, and it is the default: a
// licence is per-install state (~/.fox/license.json) and `node join` copies the
// primary's database, not its state directory. Measured on a real build, an
// unlicensed enterprise edition refuses a paid feature outright — "this build
// can run it, but no licence covers realtime" — so promotion would stop the
// paid features with no grace at all.
func TestStandbyLicenceWarning(t *testing.T) {
	t.Run("no licence on an enterprise build is the warning this exists for", func(t *testing.T) {
		got := standbyLicenceWarning(true, license.Status{}) // the zero value is Invalid
		if got == "" {
			t.Fatal("a standby with no licence was told nothing")
		}
		for _, want := range []string{
			"no licence that unlocks",
			"promotion do not need one", // replication still works: say so first
			"per-install state",         // why joining did not bring one
			"features stop with it",     // wraps after "the paid", so match the second line
			"Not a grace period",        // the correction to the plan's assumption
			"license activate",          // what to do
			"site licence",              // and what to ask for when buying HA
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the warning does not say %q:\n%s", want, got)
			}
		}
		// It must not read as a failure of the join: the standby is fine.
		if strings.Contains(got, "failed") || strings.Contains(got, "error") {
			t.Errorf("the warning reads as a failed join:\n%s", got)
		}
	})

	t.Run("a Standard build has nothing to lose, so says nothing", func(t *testing.T) {
		if got := standbyLicenceWarning(false, license.Status{}); got != "" {
			t.Errorf("a Standard build was warned about paid features:\n%s", got)
		}
	})

	t.Run("an active licence says nothing", func(t *testing.T) {
		if got := standbyLicenceWarning(true, license.Status{State: license.Active}); got != "" {
			t.Errorf("a licensed machine was warned:\n%s", got)
		}
	})

	t.Run("a warning licence still unlocks, and says so", func(t *testing.T) {
		// This is the case the plan assumed was fatal after 14 days. It is not:
		// Warning unlocks, and the grace applies to expiry alone — so a licence
		// copied from the other machine keeps working after a promotion.
		st := license.Status{State: license.Warning, Reason: "this licence was activated on a different machine"}
		got := standbyLicenceWarning(true, st)
		if !strings.Contains(got, "still unlocks") {
			t.Errorf("a warning licence is not described as working:\n%s", got)
		}
		if strings.Contains(got, "stop with it") {
			t.Errorf("a working licence is described as if it would stop:\n%s", got)
		}
		if !strings.Contains(got, "different machine") {
			t.Errorf("the warning does not pass on why it warns:\n%s", got)
		}
	})

	t.Run("a lapsed licence is warned about like an absent one", func(t *testing.T) {
		// Lapsed does not unlock, so promotion costs the paid features just as
		// an absent licence does.
		got := standbyLicenceWarning(true, license.Status{State: license.Lapsed, Reason: "expired"})
		if !strings.Contains(got, "no licence that unlocks") {
			t.Errorf("a lapsed licence was not warned about:\n%s", got)
		}
	})
}
