// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"strings"
	"testing"
)

// What `fox guardrail limit` sends: the actor for bb.policy_history first, then
// one change, with the table and the actor as literals whatever they contain.
func TestLimitSQL(t *testing.T) {
	set := limitSQL(`public."O'Brien"`, 500, "cli:o'neil")
	for _, want := range []string{
		"SET bb.actor = 'cli:o''neil';",
		`VALUES ('public."O''Brien"', 500)`,
		"ON CONFLICT (rel) DO UPDATE SET max_rows = EXCLUDED.max_rows;",
	} {
		if !strings.Contains(set, want) {
			t.Errorf("setting a limit: missing %q in\n%s", want, set)
		}
	}
	if strings.Index(set, "SET bb.actor") > strings.Index(set, "INSERT") {
		t.Error("the actor is set after the change, so the history would not name them")
	}
	rm := limitSQL(DefaultLimitTable, -1, "cli:a")
	if !strings.Contains(rm, "DELETE FROM bb.dml_limits WHERE rel = '*';") || strings.Contains(rm, "INSERT") {
		t.Errorf("removing the default:\n%s", rm)
	}
}
