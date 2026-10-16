// SPDX-License-Identifier: AGPL-3.0-or-later

package ledger

import (
	"regexp"
	"strings"
	"testing"
)

// The data-change triggers are refused being switched off in any way an
// ordinary session would notice, not only by DISABLE. A trigger made to fire
// only in replica mode (tgenabled 'R') never fires in an ordinary session, and
// a table's owner may do that without being a superuser; it used to pass. The
// behaviour is proved in integration-v2 §10; this guards the check itself.
func TestGuardTriggersCannotBeMadeReplicaOnly(t *testing.T) {
	if !strings.Contains(SchemaData, "t.tgenabled IN ('D','R')") {
		t.Error("the trigger check no longer refuses both disabled and replica-only triggers")
	}
	// The refusal keeps the wording scripts and people already match on.
	if !strings.Contains(SchemaData, "cannot be disabled or renamed, nor made replica-only") {
		t.Error("the refusal's message changed")
	}
}

// Policy by impact (the expert review plan's G2): an agent's UPDATE or DELETE
// is refused once it changes more rows of one table than its limit. The
// behaviour is proved in integration-v2 §10b; this guards what it rests on.
func TestImpactGuard(t *testing.T) {
	for _, want := range []string{
		// On by default.
		"VALUES ('AGENT DML ROWS', 'block')",
		// Counted per row, so a mass change stops at the first row over.
		"BEFORE UPDATE OR DELETE ON %s FOR EACH ROW WHEN %s EXECUTE FUNCTION bb.guard_impact()",
		// Where the session cannot write: a setting could be reset row by row
		// from the statement's own WHERE.
		"CREATE UNLOGGED TABLE IF NOT EXISTS bb.dml_counter",
		// A partition counts towards its partitioned table.
		"pg_partition_root(TG_RELID)",
		// The refusal says "is blocked by policy", which the console and
		// errhint already recognise as a guardrail.
		"is blocked by policy for agents (set bb.allow_destructive=on to override)",
		// Protected like the other three.
		"'bb.guard_impact()'::regprocedure)",
		"guard_dml|guard_impact) on ",
	} {
		if !strings.Contains(SchemaData, want) {
			t.Errorf("datachanges.sql no longer contains %q", want)
		}
	}
}

// The WHEN clause runs for every row of every UPDATE and DELETE, people's
// included. Each mention of session_user is a catalogue lookup per row, and
// a cast or a regex costs more, which the benchmark measured: keep it to one
// LIKE ANY.
func TestImpactGuardFilterStaysCheap(t *testing.T) {
	i := strings.Index(SchemaData, "CREATE OR REPLACE FUNCTION bb._impact_when()")
	if i < 0 {
		t.Fatal("bb._impact_when is gone")
	}
	body := SchemaData[i:]
	body = body[:strings.Index(body, "$$;")]
	if n := strings.Count(body, "session_user"); n != 1 {
		t.Errorf("the WHEN clause names session_user %d times; once is what was measured", n)
	}
	for _, slow := range []string{"::text", " ~ ", "left(", "regexp"} {
		if strings.Contains(body, slow) {
			t.Errorf("the WHEN clause uses %q, which the benchmark found slow", slow)
		}
	}
}

// Attaching the Blackbox's own triggers is DDL, and must not be recorded as a
// change to the table: it happens on every start and to every new table, and
// would fill the record, a diff and every promotion request with it. The
// recorder skips them by name, so every trigger datachanges.sql creates has to
// be in that list. (bb_guard_impact once was not: integration-v2 caught 22
// failures from it.)
func TestRecorderSkipsEveryBlackboxTrigger(t *testing.T) {
	names := regexp.MustCompile(`CREATE TRIGGER (bb_[a-z_]+) `).FindAllStringSubmatch(SchemaData, -1)
	if len(names) < 4 {
		t.Fatalf("found %d trigger names in datachanges.sql", len(names))
	}
	skip := regexp.MustCompile(`object_identity ~ '\^bb_\(([a-z_|]+)\) on '`).FindStringSubmatch(Schema)
	if skip == nil {
		t.Fatal("ledger.sql no longer skips the Blackbox's own triggers by name")
	}
	listed := map[string]bool{}
	for _, n := range strings.Split(skip[1], "|") {
		listed["bb_"+n] = true
	}
	for _, n := range names {
		if !listed[n[1]] {
			t.Errorf("creating %s would be recorded in the Blackbox: add it to log_ddl_end's skip list", n[1])
		}
	}
}

// datachanges.sql runs after the event triggers are on, so a GRANT or REVOKE
// in it is recorded as an unattributed change on every start (integration-v2
// §9). The counter and the limits are protected by having nothing granted on
// them, which needs no statement at all.
func TestDataChangesGrantsNothing(t *testing.T) {
	for _, line := range strings.Split(SchemaData, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "--") {
			continue
		}
		if strings.HasPrefix(l, "GRANT ") || strings.HasPrefix(l, "REVOKE ") {
			t.Errorf("datachanges.sql changes a privilege, which every start would record: %s", l)
		}
	}
}

// The limits live in ledger.sql, beside bb.policy, at 100 rows by default, and
// every change to one is in the policy history.
func TestDMLLimitsInTheBaseLedger(t *testing.T) {
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS bb.dml_limits",
		"VALUES ('*', 100)",
		"AFTER INSERT OR UPDATE OR DELETE ON bb.dml_limits",
	} {
		if !strings.Contains(Schema, want) {
			t.Errorf("ledger.sql no longer contains %q", want)
		}
	}
	// Before the event triggers go on at the end of ledger.sql, or creating
	// the history triggers is recorded as a change on every start.
	if strings.Index(Schema, "ON bb.dml_limits") > strings.Index(Schema, "CREATE EVENT TRIGGER bb_guard_start") {
		t.Error("bb.dml_limits' triggers are created after the event triggers are on")
	}
}

// A trigger has no schema of its own, so the recorder's "skip anything in bb"
// does not cover one created on a bb table. datachanges.sql runs with the event
// triggers on, so it must create none (the limits' history triggers did, and
// were recorded on every start).
func TestDataChangesCreatesNoTriggerOnBlackboxTables(t *testing.T) {
	if m := regexp.MustCompile(`(?i)CREATE (OR REPLACE )?TRIGGER \w+[^;]*\bON bb\.`).FindString(SchemaData); m != "" {
		t.Errorf("datachanges.sql creates a trigger on a Blackbox table, which every start would record: %q", m)
	}
}

// Budgets across statements (the expert review plan's F2): a per-statement
// limit alone is evaded by changing 100 rows at a time. The behaviour is proved
// in integration-v2 §10c; this guards what it rests on.
func TestAgentBudget(t *testing.T) {
	// The budgets and their history live in ledger.sql, before the event
	// triggers go on, at 1,000 rows per hour by default.
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS bb.dml_budgets",
		"VALUES ('*', 1000, 3600)",
		"AFTER INSERT OR UPDATE OR DELETE ON bb.dml_budgets",
	} {
		if !strings.Contains(Schema, want) {
			t.Errorf("ledger.sql no longer contains %q", want)
		}
	}
	if strings.Index(Schema, "ON bb.dml_budgets") > strings.Index(Schema, "CREATE EVENT TRIGGER bb_guard_start") {
		t.Error("bb.dml_budgets' triggers are created after the event triggers are on")
	}
	for _, want := range []string{
		"VALUES ('AGENT DML BUDGET', 'block')",
		// Spending is logged in the statement's own transaction, so a refused
		// or rolled-back statement spends nothing.
		"INSERT INTO bb.dml_spent (actor, rel, n) VALUES (k.actor_key, root, k.d);",
		// Who spends is something the session cannot change: its login role,
		// or for the shared roles the value it logged in with.
		"RESET bb.actor;",
		"IF session_user NOT IN ('db_agent','db_client','dbadmin') THEN\n    RETURN session_user;",
		// An admin's override does not spend the agent's budget.
		"does not spend\n      -- the agent's budget",
		// The refusal is a guardrail to the console and errhint.
		"is blocked by policy for agents: it would pass the budget of",
	} {
		if !strings.Contains(SchemaData, want) {
			t.Errorf("datachanges.sql no longer contains %q", want)
		}
	}
}
