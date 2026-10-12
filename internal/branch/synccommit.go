// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thefoxbyte/foxbyte/internal/brand"
)

// Zero-data-loss mode: the primary does not report a commit until the standby
// has the write on disk.
//
// This is the one feature in the engine that deliberately breaks the rule the
// rest of it keeps — never slow the commit. Asynchronous replication costs a
// writer nothing and loses up to a second of writes if the machine dies;
// synchronous replication loses nothing and makes every commit wait for a
// second machine. Which of those is correct is a decision about the business,
// not about the database, so it is opt-in and says what it costs.
//
// Phase 5 of docs/FOX_HA_Plan.md.
//
// What the plan got wrong, and it matters: it described `ANY 1 (...)` as a
// quorum "so one slow standby cannot stall writes". A quorum does that only
// when there is more than one standby to choose from. With the single standby
// this engine builds today, `ANY 1 (fox_standby)` means every commit waits for
// that one standby, and if it stops, writes stop. The quorum form is still the
// right thing to write — it is what starts paying off when phase 1 puts
// standbys on other machines, with no change to this code — but it buys
// nothing yet, and a buyer told otherwise would be misled.

// syncMarker records that someone asked for zero-data-loss mode.
//
// The intent is kept separately from the setting on purpose. The setting is
// removed whenever there is no standby to wait for — a failover does that, and
// so does `ha disable` — and without a record of the intent, `ha status` could
// not tell "nobody asked for this" from "it was asked for and cannot be
// honoured right now". One of those is fine and the other needs attention.
//
// A variable for the same reason realtimeMarker is one: brand.StateDir resolves
// once per process, so a test cannot move it with HOME.
var syncMarker = func() string { return brand.StatePath("sync-commit") }

// SyncOn reports whether zero-data-loss mode has been asked for. It does not
// mean it is in effect: see SyncActive.
func SyncOn() bool {
	_, err := os.Stat(syncMarker())
	return err == nil
}

// SetSyncOn records the choice, and nothing else. The caller applies the
// setting, because a function that quietly made every commit wait for another
// machine would be the wrong place to decide that.
func SetSyncOn(on bool) error {
	if !on {
		if err := os.Remove(syncMarker()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(syncMarker()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(syncMarker(), []byte("on\n"), 0o600)
}

// syncStandbyNames is what synchronous_standby_names is set to.
//
// `ANY 1 (name)` rather than the bare `name`: the two behave identically with
// one standby, and the quorum form is what phase 1 extends by adding names
// rather than by rewriting this. The standby is named rather than `*` because
// `*` matches any walsender, and `pg_basebackup -X stream` is a walsender — a
// backup in progress would satisfy the quorum and a commit would be reported
// durable when only a backup had it. Checked: with the standby stopped and a
// base backup running, a commit still waits.
//
// Pure, so the string is tested without a pool.
func syncStandbyNames(name string) string { return "ANY 1 (" + name + ")" }

// SyncActive reports whether the primary is currently waiting for the standby,
// read from the server rather than from the marker.
func SyncActive() bool {
	out, err := capture("docker", "exec", "--env-file", pgEnvFile(), PrimaryContainer(),
		"psql", "-U", pgUser, "-d", pgDatabase, "-tAc", "SHOW synchronous_standby_names")
	return err == nil && strings.TrimSpace(out) != ""
}

// syncQuorumMet reports whether the standby is actually counted — streaming,
// and named by synchronous_standby_names.
//
// This is the check that makes the mode safe to switch on. The name comes from
// the standby's cluster_name, which is a postmaster setting, so a standby built
// before this feature existed reports as `walreceiver` and would not be counted
// at all: the setting would be in place, no standby would satisfy it, and every
// commit would wait for a server that can never answer. Asserted after applying
// and rolled back if it does not hold.
func syncQuorumMet() bool {
	out, err := capture("docker", "exec", "--env-file", pgEnvFile(), PrimaryContainer(),
		"psql", "-U", pgUser, "-d", pgDatabase, "-tAc",
		"SELECT count(*) FROM pg_stat_replication WHERE application_name = '"+StandbyName+
			"' AND state = 'streaming' AND sync_state = 'quorum'")
	return err == nil && strings.TrimSpace(out) == "1"
}

// SyncQuorumMet is syncQuorumMet for `fox check`, which has to tell "waiting
// and will complete" from "waiting and never will".
func SyncQuorumMet() bool { return syncQuorumMet() }

// standbyAppName is how the streaming standby identifies itself to the primary,
// which is "" when nothing is streaming.
func standbyAppName() string {
	out, err := capture("docker", "exec", "--env-file", pgEnvFile(), PrimaryContainer(),
		"psql", "-U", pgUser, "-d", pgDatabase, "-tAc",
		"SELECT application_name FROM pg_stat_replication WHERE state = 'streaming' LIMIT 1")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// applySync sets synchronous_standby_names on a server and reloads.
// sighup, so nothing restarts and no connection is dropped.
func applySync(server string) error {
	return syncSQL(server, "ALTER SYSTEM SET synchronous_standby_names = '"+
		syncStandbyNames(StandbyName)+"'")
}

// clearSync removes it, which is also the escape hatch from a stalled database.
//
// It works while every commit is waiting, which is the property that makes this
// mode recoverable rather than a trap: ALTER SYSTEM writes a file and
// pg_reload_conf signals the postmaster, and neither needs a commit. Checked
// against a primary with two backends stalled in SyncRep — both were released.
func clearSync(server string) error {
	return syncSQL(server, "ALTER SYSTEM RESET synchronous_standby_names")
}

// syncSQL runs one setting change and reloads, keeping psql's rendered table
// out of the caller's output and the error message in it.
func syncSQL(server, sql string) error {
	out, err := captureCombined("docker", "exec", "--env-file", pgEnvFile(), server,
		"psql", "-U", pgUser, "-d", pgDatabase, "-tAq", "-v", "ON_ERROR_STOP=1",
		"-c", sql, "-c", "SELECT pg_reload_conf()")
	if err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(out))
	}
	return nil
}

// clearSyncOnPrimary drops the setting from whichever container is serving
// `main`, if that container is up to be asked.
//
// Called on every path that leaves the install without a standby. A promoted
// standby is the case that makes this necessary rather than tidy: ALTER SYSTEM
// writes postgresql.auto.conf, pg_basebackup copies PGDATA, so a standby built
// while this mode was on carries the setting — checked, it is in the copy — and
// promoting it produces a primary that waits for a standby of its own that does
// not exist. Every write would stop at the moment of a failover, which is the
// moment it must not.
func clearSyncOnPrimary() error {
	primary := PrimaryContainer()
	if ContainerState(strings.TrimPrefix(primary, containerPrefix)) != "running" {
		return nil
	}
	if !SyncActive() {
		return nil
	}
	return clearSync(primary)
}

// SyncEnable turns zero-data-loss mode on.
//
// Refused unless a standby is actually streaming, because switching it on
// without one does not fail — it stops every write, immediately and silently,
// and the way out is a command the operator would then have to find.
func SyncEnable() error {
	if ContainerState("standby") == "absent" {
		return fmt.Errorf("zero-data-loss mode needs a standby to wait for, and this install has none.\n"+
			"Run `%s ha enable` first", brand.CLI)
	}
	if PrimaryContainer() == container("standby") {
		return fmt.Errorf("'main' is being served by the promoted standby since `%s ha failover`, "+
			"so there is no second copy to wait for.\nRun `%s ha failback` first", brand.CLI, brand.CLI)
	}
	if !HAInfo().Streaming {
		return fmt.Errorf("the standby is not streaming, so waiting for it would stop every write.\n"+
			"Check `%s ha status` and try again once it is caught up", brand.CLI)
	}
	// The standby has to identify itself by the name the setting will look for.
	// cluster_name is fixed when the container is made, so a standby built
	// before this feature reports as `walreceiver`; a restart is enough,
	// because the recovery settings live in PGDATA and it resumes where it was.
	if got := standbyAppName(); got != StandbyName {
		fmt.Printf("The standby reports itself as %q rather than %q, so the setting could not count it.\n"+
			"Restarting it to pick up its name — it resumes streaming from where it was…\n", got, StandbyName)
		if err := RestartStandby(); err != nil {
			return fmt.Errorf("restarting the standby: %w", err)
		}
		if err := pollUntil("the standby to stream again", 2*time.Minute, func() bool {
			return standbyAppName() == StandbyName
		}); err != nil {
			return err
		}
	}
	if err := applySync(PrimaryContainer()); err != nil {
		return err
	}
	// Applied, now proved. If the standby is not counted the database is
	// already refusing to finish a commit, so this rolls back rather than
	// reporting success and leaving somebody to discover it.
	if !syncQuorumMet() {
		if err := clearSync(PrimaryContainer()); err != nil {
			return fmt.Errorf("the standby is not counted by %s, and the setting could not be undone: %w\n"+
				"Undo it by hand: ALTER SYSTEM RESET synchronous_standby_names; SELECT pg_reload_conf();",
				syncStandbyNames(StandbyName), err)
		}
		return fmt.Errorf("the standby is not counted by %s, so every commit would have waited for nothing. "+
			"The setting has been undone and nothing was changed", syncStandbyNames(StandbyName))
	}
	if err := SetSyncOn(true); err != nil {
		return err
	}
	fmt.Printf("Zero-data-loss mode is on: a commit is not reported until the standby has it on disk.\n"+
		"  Writes now wait for a second machine, so they are slower by the round trip to it.\n"+
		"  There is one standby, so if it stops, writes stop. `%s ha sync off` releases them,\n"+
		"  and works while they are waiting.\n", brand.CLI)
	return nil
}

// SyncDisable turns it off, and is the thing to run when writes have stopped.
//
// The setting is cleared before the marker on purpose: clearing it is what
// releases a stalled database, and that must not be skipped because a file
// could not be written.
func SyncDisable() error {
	primary := PrimaryContainer()
	if ContainerState(strings.TrimPrefix(primary, containerPrefix)) == "running" {
		if err := clearSync(primary); err != nil {
			return fmt.Errorf("clearing synchronous_standby_names on %s: %w", primary, err)
		}
	}
	if err := SetSyncOn(false); err != nil {
		return err
	}
	fmt.Println("Zero-data-loss mode is off: commits no longer wait for the standby.")
	fmt.Println("  Replication carries on asynchronously, which is where it started.")
	return nil
}

// restoreSync puts the setting back after a new standby has been built, for an
// install that asked for this mode and lost it to a failover or a rebuild.
//
// Silent when it is not wanted, and never fatal: a standby that exists is worth
// more than a setting, so `ha enable` reports what happened and succeeds.
func restoreSync() {
	if !SyncOn() || SyncActive() {
		return
	}
	if standbyAppName() != StandbyName || !HAInfo().Streaming {
		fmt.Printf("Zero-data-loss mode was asked for but is not back on: the standby is not ready to be\n"+
			"counted yet. Run `%s ha sync on` once `%s ha status` shows it streaming.\n", brand.CLI, brand.CLI)
		return
	}
	if err := applySync(PrimaryContainer()); err != nil || !syncQuorumMet() {
		_ = clearSync(PrimaryContainer())
		fmt.Printf("Zero-data-loss mode was asked for but could not be put back (%v).\n"+
			"The standby is streaming and commits are not waiting for it; `%s ha sync on` retries.\n",
			err, brand.CLI)
		return
	}
	fmt.Println("Zero-data-loss mode is back on: commits wait for this standby.")
}

// guardSyncAtStartup makes sure nothing is waiting for a standby that is not
// there, before the engine writes anything.
//
// This closes a hole that `fox stop` opens. Down() removes every managed
// container, the standby among them, but ALTER SYSTEM wrote
// synchronous_standby_names into main's postgresql.auto.conf, which survives a
// restart. So `fox start` brought main up still waiting for a standby that no
// longer existed, and Init's own writes — the role sync, InstallLedger — were
// commits. They would have waited for ever: `fox start` never returning, no
// output saying why, and the database unwritable until somebody found
// `ALTER SYSTEM RESET` by hand.
//
// The invariant this keeps is the one the rest of the mode is built on: the
// setting may only be in effect while a standby is streaming. Every other path
// that leaves the install without one already clears it — failover, failback,
// disable — and startup is the one that was missing.
//
// Best effort and never fatal. A database that is writable matters more than a
// setting, and the marker is left alone so `ha enable` puts the mode back.
func guardSyncAtStartup() {
	if !SyncActive() {
		return
	}
	// Nothing to wait for, ever: there is no standby container to come back.
	if ContainerState("standby") == "absent" {
		clearSyncAtStartup("there is no standby")
		return
	}
	// There is one, and it may be seconds from connecting — clearing the mode
	// on a healthy install because a replica had not finished starting would be
	// its own bug. So wait, and only then give up.
	if err := pollUntil("the standby to stream", time.Minute, func() bool {
		return HAInfo().Streaming
	}); err != nil {
		clearSyncAtStartup("the standby is not streaming")
		return
	}
}

func clearSyncAtStartup(why string) {
	if err := clearSyncOnPrimary(); err != nil {
		fmt.Printf("\nWARNING: commits are waiting for a standby and %s, and the setting could not be\n"+
			"cleared (%v). Writes will not complete until it is:\n  %s ha sync off\n\n", why, err, brand.CLI)
		return
	}
	fmt.Printf("Zero-data-loss mode is off for now: %s, so commits would have waited for ever.\n"+
		"  `%s ha enable` builds a standby and puts the mode back.\n", why, brand.CLI)
}
