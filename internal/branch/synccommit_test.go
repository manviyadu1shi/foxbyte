// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"path/filepath"
	"strings"
	"testing"
)

// syncMarkerIn points the marker at a directory of this test's own, for the
// same reason realtimeMarkerIn does: brand.StateDir resolves once per process
// and caches, so setting HOME is not enough and the first test to ask would
// otherwise win for the whole binary.
func syncMarkerIn(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := syncMarker
	syncMarker = func() string { return filepath.Join(dir, "sync-commit") }
	t.Cleanup(func() { syncMarker = old })
}

// Off unless somebody asked, and asking is a file so it survives a restart.
func TestZeroDataLossIsOptIn(t *testing.T) {
	syncMarkerIn(t)
	if SyncOn() {
		t.Fatal("a fresh install makes commits wait for a standby")
	}
	if err := SetSyncOn(true); err != nil || !SyncOn() {
		t.Fatalf("asking for it: %v", err)
	}
	if err := SetSyncOn(false); err != nil || SyncOn() {
		t.Fatalf("turning it off: %v", err)
	}
	// Twice is not an error: the end state is what was asked for.
	if err := SetSyncOn(false); err != nil {
		t.Errorf("turning it off when already off: %v", err)
	}
}

// The setting Postgres is given. `ANY 1 (...)` rather than a bare name, and a
// name rather than `*`.
func TestSyncStandbyNames(t *testing.T) {
	got := syncStandbyNames(StandbyName)
	if got != "ANY 1 (fox_standby)" {
		t.Errorf("synchronous_standby_names = %q", got)
	}
	// `*` matches any walsender, and `pg_basebackup -X stream` is one — a
	// backup in progress would satisfy the quorum and a commit would be
	// reported durable when only a backup had it.
	if strings.Contains(got, "*") {
		t.Errorf("%q counts any walsender, including a running base backup", got)
	}
	// The quorum form is what phase 1 extends by adding names rather than by
	// rewriting this, so the shape matters as much as the name.
	if !strings.HasPrefix(got, "ANY 1 (") {
		t.Errorf("%q is not the quorum form", got)
	}
}

// One word names this standby everywhere Postgres mentions it: its slot on the
// primary, its cluster_name, and the application_name the primary counts. A
// test rather than a comment because they live in different namespaces and
// nothing in the compiler keeps them equal.
func TestOneNameForTheStandby(t *testing.T) {
	if StandbySlot != StandbyName {
		t.Errorf("the slot is %q but the standby is %q", StandbySlot, StandbyName)
	}
	if !strings.Contains(syncStandbyNames(StandbyName), StandbyName) {
		t.Error("synchronous_standby_names does not name the standby")
	}
	args := strings.Join(standbyRunArgs("/data/standby"), " ")
	if !strings.Contains(args, "cluster_name="+StandbyName) {
		t.Errorf("the standby does not call itself %q: %s", StandbyName, args)
	}
}

// The standby is named unconditionally, because that is how the primary
// identifies it at all — pg_basebackup -R writes no application_name, so
// without cluster_name the walreceiver reports as `walreceiver` and
// zero-data-loss mode could not name it. Costs nothing when the mode is off.
func TestTheStandbyAlwaysNamesItself(t *testing.T) {
	for _, on := range []bool{false, true} {
		realtimeMarkerIn(t)
		if on {
			if err := SetRealtimeOn(true); err != nil {
				t.Fatal(err)
			}
		}
		args := strings.Join(standbyRunArgs("/data/standby"), " ")
		if !strings.Contains(args, "cluster_name="+StandbyName) {
			t.Errorf("realtime=%v: the standby was not named: %s", on, args)
		}
	}
}
