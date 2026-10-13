// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/thefoxbyte/foxbyte/internal/branch"
)

// Where the backups go, as `fox check` reports it.
//
// The default sends the write-ahead log archive and every base backup to the
// object store beside main, on the same disk — fine for a laptop, and wrong
// for anything holding data somebody would miss. This line is the only place
// the product says so unprompted, so what it says matters.
func TestBackupTargetLine(t *testing.T) {
	t.Run("the local store warns, and says what it costs", func(t *testing.T) {
		got := backupTargetLine(branch.Target{Kind: "local"}, nil)
		if got.state != stateWarn {
			t.Fatalf("state %d, want a warning", got.state)
		}
		// The consequence, not just the fact. "backups are local" is a
		// configuration note; "one disk failure takes the database and every
		// backup of it" is a reason to act.
		for _, want := range []string{"same disk", "every backup of it"} {
			if !strings.Contains(got.detail, want) {
				t.Errorf("detail %q does not mention %q", got.detail, want)
			}
		}
		// And the way out, which needs no new code.
		if !strings.Contains(got.fix, "backup target set") {
			t.Errorf("fix %q does not name the command", got.fix)
		}
	})

	t.Run("a remote target is fine, and is described", func(t *testing.T) {
		got := backupTargetLine(branch.Target{
			Kind: "s3", Bucket: "acme-fox-backups", Endpoint: "https://s3.eu-west-1.amazonaws.com",
			Region: "eu-west-1", AccessKey: "AKIA…",
		}, nil)
		if got.state != 0 {
			t.Fatalf("state %d, want ok: %s", got.state, got.detail)
		}
		if !strings.Contains(got.detail, "acme-fox-backups") {
			t.Errorf("detail %q does not name the bucket", got.detail)
		}
	})

	// A target file that cannot be read is not "assume local": backups would
	// go somewhere the owner did not choose, which is the one outcome this
	// check exists to prevent.
	t.Run("an unreadable target fails rather than guessing", func(t *testing.T) {
		got := backupTargetLine(branch.Target{}, errTargetForTest{})
		if got.state != stateFail {
			t.Fatalf("state %d, want a failure", got.state)
		}
		if !strings.Contains(got.detail, "backup-target.json") {
			t.Errorf("detail %q does not say which file", got.detail)
		}
	})
}

type errTargetForTest struct{}

func (errTargetForTest) Error() string {
	return "/home/a/.fox/backup-target.json: unexpected end of JSON input"
}

// What a commit means, and the one case where this line has to shout.
func TestDurabilityLine(t *testing.T) {
	never := func() bool { return false }
	met := func() bool { return true }

	t.Run("waiting for a standby nothing counts is a failure, not a warning", func(t *testing.T) {
		// The database looks hung from outside: every commit is waiting and
		// none will complete. This is the line somebody reads when that
		// happens, so it has to say so and carry the way out.
		got := durabilityLine(branch.HAState{Enabled: true, Sync: true, SyncActive: true}, never)
		if got.state != stateFail {
			t.Fatalf("state %d, want a failure", got.state)
		}
		for _, want := range []string{"every commit is waiting", "will not complete"} {
			if !strings.Contains(got.detail, want) {
				t.Errorf("detail %q does not mention %q", got.detail, want)
			}
		}
		if !strings.Contains(got.fix, "ha sync off") {
			t.Errorf("fix %q does not name the command that releases them", got.fix)
		}
		// And it says the escape hatch works *now*, which is the question
		// somebody staring at a stalled database actually has.
		if !strings.Contains(got.fix, "while they are waiting") {
			t.Errorf("fix %q does not say it works while commits are stalled", got.fix)
		}
	})

	t.Run("in effect and counted is fine, and names what it bought", func(t *testing.T) {
		got := durabilityLine(branch.HAState{Enabled: true, Sync: true, SyncActive: true}, met)
		if got.state != stateOK {
			t.Fatalf("state %d, want ok", got.state)
		}
		if !strings.Contains(got.detail, "zero data loss") {
			t.Errorf("detail %q does not say what it bought", got.detail)
		}
	})

	t.Run("asked for but not in effect warns, because the buyer thinks they have it", func(t *testing.T) {
		// The dangerous silence: somebody bought RPO zero, a failover took it
		// away, and nothing said so. Not a failure — the database is healthy.
		got := durabilityLine(branch.HAState{Enabled: false, Sync: true}, never)
		if got.state != stateWarn {
			t.Fatalf("state %d, want a warning", got.state)
		}
		if !strings.Contains(got.detail, "not in effect") {
			t.Errorf("detail %q does not say it is not in effect", got.detail)
		}
		if !strings.Contains(got.fix, "ha enable") {
			t.Errorf("fix %q does not name the way back", got.fix)
		}
	})

	t.Run("nobody asked, so say what the default costs", func(t *testing.T) {
		for _, c := range []struct {
			name string
			ha   branch.HAState
		}{
			{"with a standby", branch.HAState{Enabled: true}},
			{"without one", branch.HAState{}},
		} {
			got := durabilityLine(c.ha, never)
			if got.state != stateOK {
				t.Errorf("%s: state %d, want ok", c.name, got.state)
			}
			if !strings.Contains(got.detail, "never wait") {
				t.Errorf("%s: detail %q does not say commits never wait", c.name, got.detail)
			}
		}
	})

	t.Run("the quorum is only asked about when it decides the answer", func(t *testing.T) {
		// Asking Postgres costs a `docker exec psql`, and three of the four
		// cases do not need it.
		asked := 0
		counting := func() bool { asked++; return true }
		for _, ha := range []branch.HAState{{}, {Enabled: true}, {Sync: true}} {
			durabilityLine(ha, counting)
		}
		if asked != 0 {
			t.Errorf("Postgres was asked %d time(s) for a case that does not need it", asked)
		}
	})
}

// Who may copy the database. The warning case is the upgrade path: `ha enable`
// used to write a rule admitting any role from any address, that rule lives in
// the data directory, and a routine `fox update` has no reason to rewrite it.
func TestReplicationAccessLine(t *testing.T) {
	wide := func(string) bool { return true }
	narrow := func(string) bool { return false }

	t.Run("an older install's wide rule warns, and names what it costs", func(t *testing.T) {
		got := replicationAccessLine(branch.HAState{Enabled: true}, wide)
		if got.state != stateWarn {
			t.Fatalf("state %d, want a warning", got.state)
		}
		// The consequence, not the configuration: "pg_hba is wide" is a note,
		// "the credential that copies the database is the one that owns it" is
		// a reason to act.
		for _, want := range []string{"any role at any address", "the one that owns it"} {
			if !strings.Contains(got.detail, want) {
				t.Errorf("detail %q does not mention %q", got.detail, want)
			}
		}
		if !strings.Contains(got.fix, "ha enable") {
			t.Errorf("fix %q does not name what narrows it", got.fix)
		}
	})

	t.Run("a narrowed install says who may copy it and what they cannot do", func(t *testing.T) {
		got := replicationAccessLine(branch.HAState{Enabled: true}, narrow)
		if got.state != stateOK {
			t.Fatalf("state %d, want ok", got.state)
		}
		for _, want := range []string{branch.ReplicationRole, "can read none of it"} {
			if !strings.Contains(got.detail, want) {
				t.Errorf("detail %q does not mention %q", got.detail, want)
			}
		}
	})

	t.Run("no standby, so nothing may copy it, and pg_hba is not read", func(t *testing.T) {
		// Reading it costs a `docker exec`, and with no standby there is no
		// rule to judge.
		asked := 0
		counting := func(string) bool { asked++; return true }
		got := replicationAccessLine(branch.HAState{}, counting)
		if got.state != stateOK {
			t.Errorf("state %d, want ok", got.state)
		}
		if asked != 0 {
			t.Errorf("pg_hba was read %d time(s) with no standby to admit", asked)
		}
	})
}
