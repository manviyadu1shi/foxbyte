// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"regexp"
	"strings"
	"testing"
)

// The role that copies the database must not be the one that owns it, and must
// not be able to read anything. Verified against a live install as well
// (integration section 6), but the shape of the SQL is worth holding here.
func TestReplicationRoleIsNarrow(t *testing.T) {
	if ReplicationRole == pgUser {
		t.Fatalf("the replication role is the superuser %q", pgUser)
	}
	// Its password is its own, and not the install secret.
	for what, pw := range map[string]string{
		"the install secret": pgPass(),
		"db_client":          ClientRolePassword(),
		"db_realtime":        RealtimeRolePassword(),
	} {
		if ReplicationRolePassword() == pw {
			t.Errorf("the replication role shares a password with %s", what)
		}
	}
	if ReplicationRolePassword() != ReplicationRolePassword() {
		t.Error("the replication role's password is not stable")
	}
}

// pg_hba is first-match-wins, so a wide rule anywhere in the file still admits
// everyone it names. Leaving the old one below the narrow one would look like a
// narrowing and be none.
func TestTheWideRuleIsRemovedNotOutranked(t *testing.T) {
	sh := hbaScript()
	if !strings.Contains(sh, "sed -i") || !strings.Contains(sh, "/d") {
		t.Errorf("the wide rule is not deleted:\n%s", sh)
	}
	// The narrow rule names the role and an address, and is appended only when
	// it is not already there.
	if !strings.Contains(sh, replicationHBALine) {
		t.Errorf("the script does not write the narrow rule:\n%s", sh)
	}
	if !strings.Contains(sh, "grep -qF") {
		t.Errorf("the script is not idempotent; `ha enable` runs more than once:\n%s", sh)
	}
	// `set -e`, or a failed sed would be followed by an append and the file
	// would carry both rules.
	if !strings.HasPrefix(sh, "set -e") {
		t.Errorf("the script continues after a failed edit:\n%s", sh)
	}
}

// What the narrow rule says, written out. `all` in the address column was the
// old behaviour and is the thing being fixed; `samenet` is this install's own
// network, which is as narrow as it gets until a standby on another machine has
// an address to name.
func TestTheNarrowRuleNamesARoleAndANetwork(t *testing.T) {
	f := strings.Fields(replicationHBALine)
	if len(f) != 5 {
		t.Fatalf("pg_hba rules have five fields: %q", replicationHBALine)
	}
	if f[0] != "host" || f[1] != "replication" {
		t.Errorf("not a replication rule: %q", replicationHBALine)
	}
	if f[2] != ReplicationRole {
		t.Errorf("the rule admits %q rather than the replication role", f[2])
	}
	if f[3] == "all" {
		t.Error("the rule still admits any address, which is what it replaced")
	}
	if f[4] != "scram-sha-256" {
		t.Errorf("the rule does not require a password: %q", f[4])
	}
}

// The pattern that finds the old rule has to match what older versions wrote,
// whatever spacing they used.
func TestTheLegacyPatternMatchesWhatWasWritten(t *testing.T) {
	// What `ha enable` appended, and what Postgres's own formatting looks like.
	for _, line := range []string{
		"host replication all all scram-sha-256",
		"host    replication     all             all             scram-sha-256",
		"host\treplication\tall\tall\tscram-sha-256",
	} {
		if !basicREMatches(legacyHBAPattern, line) {
			t.Errorf("the legacy pattern does not match %q", line)
		}
	}
	// And must not take the narrow rule with it.
	if basicREMatches(legacyHBAPattern, replicationHBALine) {
		t.Errorf("the legacy pattern also deletes the narrow rule %q", replicationHBALine)
	}
	// Nor the image's own localhost rules, which are how a container talks to
	// itself.
	for _, keep := range []string{
		"local   replication     all                                     trust",
		"host    replication     all             127.0.0.1/32            trust",
	} {
		if basicREMatches(legacyHBAPattern, keep) {
			t.Errorf("the legacy pattern deletes the image's own rule %q", keep)
		}
	}
}

// basicREMatches applies a POSIX basic regular expression the way sed does,
// which Go's regexp does not speak: `\+` is one-or-more there and a literal
// plus in Go's syntax. Translated rather than approximated, so what the test
// checks is what sed will run.
func basicREMatches(bre, line string) bool {
	ere := strings.ReplaceAll(bre, `\+`, `+`)
	return mustMatch(ere, line)
}

func mustMatch(ere, line string) bool {
	re, err := regexp.Compile(ere)
	if err != nil {
		panic(err)
	}
	return re.MatchString(line)
}
