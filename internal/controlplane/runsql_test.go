// SPDX-License-Identifier: AGPL-3.0-or-later

package controlplane

import "testing"

// runQuery's three shapes, read into one: a single statement, a script, and a
// failure before any statement ran.
func TestSQLOutput(t *testing.T) {
	one := sqlOutput(map[string]any{
		"columns": []string{"a"}, "rows": [][]any{{1}}, "command": "SELECT 1",
		"results": []map[string]any{{"columns": []string{"a"}, "rows": [][]any{{1}}, "command": "SELECT 1"}},
	})
	if len(one.Results) != 1 || one.Results[0].Command != "SELECT 1" || len(one.Results[0].Rows) != 1 || one.Error != "" {
		t.Errorf("one statement: %+v", one)
	}
	failed := sqlOutput(map[string]any{"error": "boom", "hint": "h", "position": 3})
	if failed.Error != "boom" || failed.Hint != "h" || len(failed.Results) != 0 {
		t.Errorf("a failure: %+v", failed)
	}
	script := sqlOutput(map[string]any{
		"error": "no such table", "note": "nothing was applied",
		"results": []map[string]any{{"columns": []string{}, "rows": [][]any{}, "command": "INSERT 0 1"}},
	})
	if script.Error != "no such table" || script.Note == "" || len(script.Results) != 1 {
		t.Errorf("a failed script: %+v", script)
	}
}
