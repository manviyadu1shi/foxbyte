// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Agent row limits: how many rows one agent UPDATE or DELETE may change in a
// table before bb.guard_impact refuses it (internal/ledger/datachanges.sql).
// They live in each branch's bb.dml_limits, so a branch made later inherits
// main's, and every change is recorded in bb.policy_history.

// DefaultLimitTable is the bb.dml_limits key for every table without its own.
const DefaultLimitTable = "*"

// DMLLimit is one row of a branch's bb.dml_limits.
type DMLLimit struct {
	Table   string // DefaultLimitTable, or schema.table
	MaxRows int64
}

// ErrNoLimitsTable means the branch's Blackbox predates agent row limits.
var ErrNoLimitsTable = errors.New("this branch's Blackbox predates agent row limits")

// ErrNoSuchTable means the table named for a limit is not on the branch.
var ErrNoSuchTable = errors.New("no such table")

// queryRows runs a query as the superuser and returns its rows, fields
// separated by '|'.
func queryRows(name, sql string) ([][]string, error) {
	out, err := capture("docker", "exec", "--env-file", pgEnvFile(), container(name),
		"psql", "-U", pgUser, "-d", pgDatabase, "-v", "ON_ERROR_STOP=1", "-tA", "-F", "|", "-c", sql)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	var rows [][]string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			rows = append(rows, strings.Split(l, "|"))
		}
	}
	return rows, nil
}

func requireLimitsTable(name string) error {
	rows, err := queryRows(name, `SELECT to_regclass('bb.dml_limits') IS NOT NULL`)
	if err != nil {
		return err
	}
	if len(rows) == 0 || rows[0][0] != "t" {
		return ErrNoLimitsTable
	}
	return nil
}

// DMLLimits lists a branch's limits, the default first.
func DMLLimits(name string) ([]DMLLimit, error) {
	if err := requireLimitsTable(name); err != nil {
		return nil, err
	}
	rows, err := queryRows(name, `SELECT rel, max_rows FROM bb.dml_limits ORDER BY rel <> '*', rel`)
	if err != nil {
		return nil, err
	}
	var out []DMLLimit
	for _, r := range rows {
		if len(r) != 2 {
			continue
		}
		n, err := strconv.ParseInt(r[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("reading the limit for %s: %w", r[0], err)
		}
		out = append(out, DMLLimit{Table: r[0], MaxRows: n})
	}
	return out, nil
}

// resolveLimitTable turns what a person typed ("orders", "public.orders",
// `"Mixed Case"`) into the key bb.guard_impact looks up: schema.table, quoted
// as bb._table_identity quotes it. The table must exist on the branch.
func resolveLimitTable(name, table string) (string, error) {
	if table == "" || table == DefaultLimitTable {
		return DefaultLimitTable, nil
	}
	rows, err := queryRows(name, fmt.Sprintf(`SELECT bb._table_identity(n.nspname, c.relname)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.oid = to_regclass(%s) AND c.relkind IN ('r','p')`, quoteLiteral(table)))
	if err != nil {
		return "", err
	}
	if len(rows) == 0 || rows[0][0] == "" {
		return "", fmt.Errorf("%w %q on %q", ErrNoSuchTable, table, name)
	}
	return rows[0][0], nil
}

// limitSQL is the script that sets (rows >= 0) or removes (rows < 0) one
// limit, naming actor for bb.policy_history.
func limitSQL(key string, rows int64, actor string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SET bb.actor = %s;\n", quoteLiteral(actor))
	if rows < 0 {
		fmt.Fprintf(&b, "DELETE FROM bb.dml_limits WHERE rel = %s;\n", quoteLiteral(key))
	} else {
		fmt.Fprintf(&b, "INSERT INTO bb.dml_limits (rel, max_rows) VALUES (%s, %d)\n"+
			"  ON CONFLICT (rel) DO UPDATE SET max_rows = EXCLUDED.max_rows;\n", quoteLiteral(key), rows)
	}
	return b.String()
}

// SetDMLLimit sets a table's limit on a branch (table "" or "*" is the
// default), or removes it when rows is negative: a table then falls back to the
// default, and without a default no limit applies. It returns the key it set.
func SetDMLLimit(name, table string, rows int64, actor string) (string, error) {
	if err := requireLimitsTable(name); err != nil {
		return "", err
	}
	key, err := resolveLimitTable(name, table)
	if err != nil {
		return "", err
	}
	return key, psqlStdin(name, limitSQL(key, rows, actor))
}

// DMLBudget is one row of a branch's bb.dml_budgets: how many rows one agent
// may change in a table over a window, across all its statements.
type DMLBudget struct {
	Table   string // DefaultLimitTable, or schema.table
	MaxRows int64
	PerSecs int
}

// Budget windows run from a minute to a week, as bb.dml_budgets allows.
const (
	MinBudgetWindow = 60
	MaxBudgetWindow = 7 * 24 * 3600
)

func requireBudgetsTable(name string) error {
	rows, err := queryRows(name, `SELECT to_regclass('bb.dml_budgets') IS NOT NULL`)
	if err != nil {
		return err
	}
	if len(rows) == 0 || rows[0][0] != "t" {
		return ErrNoLimitsTable
	}
	return nil
}

// DMLBudgets lists a branch's budgets, the default first.
func DMLBudgets(name string) ([]DMLBudget, error) {
	if err := requireBudgetsTable(name); err != nil {
		return nil, err
	}
	rows, err := queryRows(name, `SELECT rel, max_rows, per_secs FROM bb.dml_budgets ORDER BY rel <> '*', rel`)
	if err != nil {
		return nil, err
	}
	var out []DMLBudget
	for _, r := range rows {
		if len(r) != 3 {
			continue
		}
		n, err1 := strconv.ParseInt(r[1], 10, 64)
		s, err2 := strconv.Atoi(r[2])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("reading the budget for %s: %q", r[0], strings.Join(r, "|"))
		}
		out = append(out, DMLBudget{Table: r[0], MaxRows: n, PerSecs: s})
	}
	return out, nil
}

// budgetSQL sets (rows >= 0) or removes (rows < 0) one budget, naming actor
// for bb.policy_history.
func budgetSQL(key string, rows int64, perSecs int, actor string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SET bb.actor = %s;\n", quoteLiteral(actor))
	if rows < 0 {
		fmt.Fprintf(&b, "DELETE FROM bb.dml_budgets WHERE rel = %s;\n", quoteLiteral(key))
	} else {
		fmt.Fprintf(&b, "INSERT INTO bb.dml_budgets (rel, max_rows, per_secs) VALUES (%s, %d, %d)\n"+
			"  ON CONFLICT (rel) DO UPDATE SET max_rows = EXCLUDED.max_rows, per_secs = EXCLUDED.per_secs;\n",
			quoteLiteral(key), rows, perSecs)
	}
	return b.String()
}

// SetDMLBudget sets a table's budget on a branch (table "" or "*" is the
// default), or removes it when rows is negative. It returns the key it set.
func SetDMLBudget(name, table string, rows int64, perSecs int, actor string) (string, error) {
	if rows >= 0 && (perSecs < MinBudgetWindow || perSecs > MaxBudgetWindow) {
		return "", fmt.Errorf("a budget's window is from a minute to a week, not %ds", perSecs)
	}
	if err := requireBudgetsTable(name); err != nil {
		return "", err
	}
	key, err := resolveLimitTable(name, table)
	if err != nil {
		return "", err
	}
	return key, psqlStdin(name, budgetSQL(key, rows, perSecs, actor))
}
