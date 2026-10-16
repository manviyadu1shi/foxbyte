// SPDX-License-Identifier: AGPL-3.0-or-later

package ledger

import (
	"regexp"
	"strings"
	"testing"
)

// actor_kind must be derived from the login role, not read from the session.
// bb.actor_kind is an ordinary session setting, so while _ctx believed it a
// client could `SET bb.actor_kind = 'human'` and have an agent's changes
// recorded as a person's — the one thing this record exists to prevent. The
// behaviour itself is proved in integration-v2 §1c (a human claiming to be an
// agent and an agent claiming to be human are both recorded truthfully); this
// test guards the derivation against being quietly replaced by the setting
// again.
func TestActorKindDerivedFromRole(t *testing.T) {
	ctx := ctxFunction(t)

	for _, want := range []string{
		// An agent branch's own role is always an agent…
		"WHEN session_user LIKE 'agent-%'",
		// …unless the role is named for an email address (a person whose
		// address happens to start with "agent-").
		"position('@' in session_user) = 0",
		// The engine's agent role, and an agent key's role, which is a member
		// of it, are agents by login…
		"WHEN session_user = 'db_agent' OR EXISTS",
		// …read from the catalogue, because pg_has_role answers yes for every
		// superuser and would make dbadmin an agent.
		"pg_auth_members",
		// A per-user role is a person.
		"WHEN session_user NOT IN ('dbadmin','db_client') THEN 'human'",
		// db_agent has no identity of its own, so its actor is the one the
		// engine names, like the other shared roles.
		"NOT IN ('dbadmin','db_client','db_agent')",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("_ctx no longer derives the kind from the role: missing %q", want)
		}
	}

	// The injected setting may only be consulted for the shared roles, which
	// have no identity of their own — never as the first answer.
	kindLine := regexp.MustCompile(`(?s)OUT actor_kind.*?current_setting\('bb\.actor_kind'`)
	if !kindLine.MatchString(ctx) {
		t.Fatal("_ctx does not mention bb.actor_kind at all; expected it as the shared-role fallback")
	}
	before, _, _ := strings.Cut(ctx, "current_setting('bb.actor_kind'")
	if !strings.Contains(before, "session_user LIKE 'agent-%'") {
		t.Error("the session setting is consulted before the role is examined")
	}
	// And the agent roles are examined before anything can call the session a
	// person: otherwise db_agent would fall through to the setting, which its
	// own SQL can change.
	kindCase := before[strings.Index(before, "OUT actor_kind"):]
	if a, h := strings.Index(kindCase, "session_user = 'db_agent'"), strings.Index(kindCase, "THEN 'human'"); a < 0 || h < 0 || a > h {
		t.Error("a session can be classed as a person before db_agent is recognised")
	}
}

// The engine's agent role is created where db_client is, with db_client's
// access and nothing more.
func TestAgentClientRoleCreated(t *testing.T) {
	for _, want := range []string{
		"CREATE ROLE db_agent LOGIN NOSUPERUSER NOCREATEROLE NOCREATEDB NOBYPASSRLS INHERIT IN ROLE db_client;",
		"ALTER ROLE db_agent SET role = db_client;",
	} {
		if !strings.Contains(Schema, want) {
			t.Errorf("ledger.sql does not contain %q", want)
		}
	}
	if strings.Index(Schema, "CREATE ROLE db_agent") < strings.Index(Schema, "CREATE ROLE db_client") {
		t.Error("db_agent is created before db_client, which it is a member of")
	}
}

// ctxFunction returns the body of bb._ctx from the embedded schema.
func ctxFunction(t *testing.T) string {
	t.Helper()
	const marker = "CREATE OR REPLACE FUNCTION bb._ctx("
	i := strings.Index(Schema, marker)
	if i < 0 {
		t.Fatal("bb._ctx is not in the schema")
	}
	rest := Schema[i:]
	end := strings.Index(rest, "$$;")
	if end < 0 {
		t.Fatal("bb._ctx has no end")
	}
	return rest[:end]
}

// SchemaName is what the shell library and the uninstaller are generated from,
// so it must be what the SQL actually creates. If the two drift, tools go
// looking for a schema that is not there.
func TestSchemaNameMatchesSQL(t *testing.T) {
	if !strings.Contains(Schema, "CREATE SCHEMA IF NOT EXISTS "+SchemaName) {
		t.Errorf("ledger.sql does not create schema %q", SchemaName)
	}
	// And every object in it is qualified with the same name.
	if !strings.Contains(Schema, SchemaName+".schema_ledger") {
		t.Errorf("ledger.sql does not put schema_ledger in %q", SchemaName)
	}
}
