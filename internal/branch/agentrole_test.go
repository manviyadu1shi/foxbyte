// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"strings"
	"testing"
)

// An agent key's role is named so that nothing can take it for anything else:
// not an agent branch's role ("agent-<id>"), which the Blackbox matches by that
// prefix, and not a branch at all.
func TestAgentKeyRoleName(t *testing.T) {
	r := AgentKeyRole("alice@example.com")
	if r != "agent:alice@example.com" {
		t.Fatalf("AgentKeyRole = %q", r)
	}
	if strings.HasPrefix(r, "agent-") {
		t.Error("an agent key's role could be mistaken for an agent branch's")
	}
	if ValidName(r) {
		t.Error("an agent key's role is a valid branch name")
	}
}

// A failover onto another machine rewrites every role's password to the one
// this install derives for it (rolepasswords.go). An agent key's role must come
// out with the password the Gateway will log in with, or every agent key stops
// working after a failover.
func TestAgentRolePasswordsSurviveAFailover(t *testing.T) {
	email := "alice@example.com"
	role := AgentKeyRole(email)
	got, ok := rolePasswordFor(classifyRole(role, nil), role)
	if !ok || got != AgentKeyRolePassword(email) {
		t.Errorf("after a failover %q would get a password the Gateway does not use", role)
	}
	got, ok = rolePasswordFor(classifyRole(AgentClientRole, nil), AgentClientRole)
	if !ok || got != AgentClientRolePassword() {
		t.Errorf("after a failover %s would get a password the engine does not use", AgentClientRole)
	}
}

// The roles' shape is what makes them agents and keeps them no more powerful
// than a person's: db_client's access, membership of db_agent (which the
// Blackbox reads), and never db_admin, so an agent holding an admin's key still
// cannot override a guardrail.
func TestAgentRoleSQL(t *testing.T) {
	key := agentKeyRoleSQL(AgentKeyRole("alice@example.com"), "pw")
	for _, want := range []string{
		"NOSUPERUSER", "NOBYPASSRLS", "INHERIT IN ROLE db_client, db_agent",
		"'agent:alice@example.com'", "SET role = db_client",
	} {
		if !strings.Contains(key, want) {
			t.Errorf("an agent key's role is missing %q:\n%s", want, key)
		}
	}
	shared := agentClientRoleSQL("pw")
	for _, want := range []string{"NOSUPERUSER", "NOBYPASSRLS", "INHERIT IN ROLE db_client", "'db_agent'", "SET role = db_client"} {
		if !strings.Contains(shared, want) {
			t.Errorf("db_agent is missing %q:\n%s", want, shared)
		}
	}
	for what, sql := range map[string]string{"an agent key's role": key, "db_agent": shared} {
		if strings.Contains(sql, "db_admin") {
			t.Errorf("%s is given the admin role", what)
		}
		if !strings.Contains(sql, "'pw'") {
			t.Errorf("%s does not set its password as a literal", what)
		}
	}
}

// A branch made again under the same name is a new clone, and may be one of a
// parent whose Blackbox would record an agent role as a person. So what was
// provisioned is remembered per container, never per name, and nothing is
// remembered when the container cannot be identified.
func TestAgentCacheIsPerContainer(t *testing.T) {
	a, ok := agentCacheKey("b", "c1", AgentClientRole)
	if !ok {
		t.Fatal("a known container is not cacheable")
	}
	if b, _ := agentCacheKey("b", "c2", AgentClientRole); a == b {
		t.Error("two containers of the same branch share a cache entry")
	}
	if _, ok := agentCacheKey("b", "", AgentClientRole); ok {
		t.Error("an unidentified container may be cached")
	}
	if x, _ := agentCacheKey("b", "c1", AgentKeyRole("a@x.com")); x == a {
		t.Error("db_agent and an agent key's role share a cache entry")
	}
}
