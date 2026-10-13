// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"strings"
	"testing"
)

// Which roles this install may rewrite the password of, after promoting a copy
// taken from another machine.
//
// The cost of getting this wrong is asymmetric: a role treated as ours that is
// not — one somebody made by hand, or an extension's — would have its password
// replaced with one derived for a name it does not own, and whatever used it
// would stop working with no explanation. So anything unrecognised is left
// alone, and that is the default rather than the exception.
func TestClassifyRole(t *testing.T) {
	isBranch := func(n string) bool { return n == "agent-7" || n == "demo" }
	cases := []struct {
		name string
		want roleClass
	}{
		{pgUser, roleSuper},
		{ClientRole, roleClient},
		{ReplicationRole, roleReplica},
		{RealtimeRole, roleRealtime},
		{"alice@example.com", roleUser},
		{"agent-7", roleAgent},
		{"demo", roleAgent},
		// Not ours: a group role with no password, Postgres's own roles, and
		// anything else at all.
		{AdminRole, roleUnknown},
		{"postgres", roleUnknown},
		{"pg_monitor", roleUnknown},
		{"grafana_reader", roleUnknown},
		{"", roleUnknown},
	}
	for _, c := range cases {
		if got := classifyRole(c.name, isBranch); got != c.want {
			t.Errorf("classifyRole(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// An address is the only name here that can contain '@' — ErrBadName rejects it
// for a branch — so that is what tells a person's role from a branch's.
func TestAnAddressCannotBeMistakenForABranch(t *testing.T) {
	if ValidName("alice@example.com") {
		t.Error("an address is a valid branch name, so the two kinds of role cannot be told apart")
	}
	// And a role named for a branch is only ours while that branch exists:
	// otherwise a deleted branch's role would be rewritten with a password
	// derived from a name nothing uses.
	if got := classifyRole("gone", func(string) bool { return false }); got != roleUnknown {
		t.Errorf("a role named for a branch that does not exist is %v, want roleUnknown", got)
	}
}

// What the rewrite actually sends. The property worth asserting is the negative
// one: a role the engine does not manage appears nowhere in the script.
func TestRolePasswordSQLTouchesOnlyWhatItManages(t *testing.T) {
	isBranch := func(n string) bool { return n == "demo" }
	roles := []string{pgUser, ClientRole, "alice@example.com", "demo", "grafana_reader", AdminRole, "", "pg_monitor"}
	sql, touched := rolePasswordSQL(roles, isBranch)

	want := []string{pgUser, ClientRole, "alice@example.com", "demo"}
	if strings.Join(touched, ",") != strings.Join(want, ",") {
		t.Errorf("touched %v, want %v", touched, want)
	}
	for _, left := range []string{"grafana_reader", AdminRole, "pg_monitor"} {
		if strings.Contains(sql, left) {
			t.Errorf("the script mentions %q, which this install does not manage", left)
		}
	}
	// One statement per role, and the name and the password both go through
	// format() rather than being pasted into the statement.
	if n := strings.Count(sql, "ALTER ROLE"); n != len(want) {
		t.Errorf("%d statements for %d roles", n, len(want))
	}
	if !strings.Contains(sql, "format('ALTER ROLE %I WITH PASSWORD %L'") {
		t.Errorf("names and passwords are not quoted by Postgres:\n%s", sql)
	}
	// An empty list produces no script at all, so a cluster with nothing of
	// ours in it is left entirely alone.
	if s, tt := rolePasswordSQL([]string{"grafana_reader"}, isBranch); s != "" || len(tt) != 0 {
		t.Errorf("a cluster with no roles of ours produced %q", s)
	}
}

// Every class the classifier can return must have a password to derive, or a
// role would be reported as managed and then skipped in silence.
func TestEveryManagedClassHasAPassword(t *testing.T) {
	for _, c := range []roleClass{roleSuper, roleClient, roleReplica, roleRealtime, roleUser, roleAgent} {
		if pw, ok := rolePasswordFor(c, "alice@example.com"); !ok || pw == "" {
			t.Errorf("class %v has no password (%q, %v)", c, pw, ok)
		}
	}
	if _, ok := rolePasswordFor(roleUnknown, "whatever"); ok {
		t.Error("an unmanaged role was given a password")
	}
}

// The listing asks Postgres for roles that can log in, and leaves its own
// alone: a group role has no password to rewrite and pg_* roles are not ours.
func TestLoginRolesQueryExcludesWhatCannotBeRewritten(t *testing.T) {
	for _, want := range []string{"rolcanlogin", `rolname NOT LIKE 'pg\_%'`} {
		if !strings.Contains(loginRolesSQL, want) {
			t.Errorf("the listing does not restrict by %s: %s", want, loginRolesSQL)
		}
	}
}
