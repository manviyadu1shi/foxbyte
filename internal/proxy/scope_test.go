// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"strings"
	"testing"
)

// A branch-scoped key is an agent's only credential, so the check that confines
// it to its own branch is the whole of that confinement.
func TestScopeAllows(t *testing.T) {
	cases := []struct {
		scope, target string
		want          bool
		why           string
	}{
		{"", "main", true, "an account key opens any branch"},
		{"", "agent-alice", true, "an account key opens an agent branch too"},
		{"agent-alice", "agent-alice", true, "a scoped key opens its own branch"},
		{"agent-alice", "main", false, "a scoped key must not reach main"},
		{"agent-alice", "agent-bob", false, "a scoped key must not reach another agent"},
		{"agent-alice", "", false, "an empty target must not pass the scope check"},
		{"agent-alice", "agent-alice2", false, "prefix matches are not the same branch"},
	}
	for _, c := range cases {
		if got := scopeAllows(c.scope, c.target); got != c.want {
			t.Errorf("scopeAllows(%q, %q) = %v, want %v — %s", c.scope, c.target, got, c.want, c.why)
		}
	}
}

// An agent connecting with its branch-scoped key must not get a session from
// the Gateway: its branch holds the session it was created with as a database
// default, and a startup option would override that default, so the agent's
// changes would stop carrying its session (integration-v2 §7 caught this).
func TestLedgerOptionsLeavesAgentSessionToTheBranch(t *testing.T) {
	human := ledgerOptions("", "ada@example.com", "main", true, false)
	if !strings.Contains(human, "bb.session=") {
		t.Errorf("a person's connection should get its own session: %q", human)
	}
	agent := ledgerOptions("", "agent-alice", "agent-alice", false, false)
	if strings.Contains(agent, "bb.session") {
		t.Errorf("an agent key's connection must not override the branch's session: %q", agent)
	}
	for _, want := range []string{"bb.actor=agent-alice", "bb.actor_kind=agent", "bb.branch=agent-alice"} {
		if !strings.Contains(agent, want) {
			t.Errorf("agent options %q lack %s", agent, want)
		}
	}
}

// An agent key's session is labelled an agent too, on any branch. The label is
// not what makes it one (its role is, branch/agentrole.go); it only keeps the
// setting from saying otherwise to anything that reads it.
func TestLedgerOptionsLabelsAnAgentKey(t *testing.T) {
	o := ledgerOptions("", "ada@example.com", "main", true, true)
	if !strings.Contains(o, "bb.actor_kind=agent") {
		t.Errorf("an agent key's session is not labelled an agent: %q", o)
	}
	if strings.Contains(ledgerOptions("", "ada@example.com", "main", true, false), "bb.actor_kind=agent") {
		t.Error("a person's session is labelled an agent")
	}
}
