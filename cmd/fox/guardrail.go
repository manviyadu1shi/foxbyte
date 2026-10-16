// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thefoxbyte/foxbyte/internal/branch"
	"github.com/thefoxbyte/foxbyte/internal/brand"
)

// guardrailCmd handles `fox guardrail limit`: how many rows one agent UPDATE or
// DELETE may change in a table before it is refused (bb.guard_impact).
//
//	fox guardrail limit [--branch <name>]                          show the limits
//	fox guardrail limit [--table <t>] [--branch <name>] <rows>     set one
//	fox guardrail limit [--table <t>] [--branch <name>] none       remove one
//
// Without --table it is the default for every table without its own. Like
// `fox admin`, without --branch a change goes to main, which branches made
// later inherit, and to every running branch.
func guardrailCmd(args []string) {
	if len(args) > 0 && args[0] == "budget" {
		guardrailBudgetCmd(args[1:])
		return
	}
	if len(args) == 0 || args[0] != "limit" {
		guardrailUsage()
	}
	rest := args[1:]
	value := firstPositional(rest, "--branch", "--table")
	table := optValue(rest, "--table")
	branches, err := adminScope(optValue(rest, "--branch"))
	must(err)

	if value == "" {
		failed := false
		for _, b := range branches {
			limits, err := branch.DMLLimits(b)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s: %v\n", b, limitHint(b, err))
				failed = true
				continue
			}
			fmt.Printf("  %s:\n", b)
			hasDefault := false
			for _, l := range limits {
				name := l.Table
				if name == branch.DefaultLimitTable {
					name, hasDefault = "default (every other table)", true
				}
				fmt.Printf("    %-32s %d rows\n", name, l.MaxRows)
			}
			if !hasDefault {
				fmt.Printf("    %-32s none\n", "default (every other table)")
			}
		}
		fmt.Println("\nAn agent's UPDATE or DELETE that changes more rows of one table than its limit is refused,")
		fmt.Printf("and recorded in the Blackbox. An admin can let one through with bb.allow_destructive=on (%s admin grant).\n", brand.CLI)
		if failed {
			os.Exit(1)
		}
		return
	}

	rows := int64(-1)
	if value != "none" {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 0 {
			must(fmt.Errorf("the limit is a number of rows (0 or more), or none; got %q", value))
		}
		rows = n
	}
	failed := false
	for _, b := range branches {
		key, err := branch.SetDMLLimit(b, table, rows, cliActor())
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", b, limitHint(b, err))
			failed = true
			continue
		}
		what := key
		if key == branch.DefaultLimitTable {
			what = "the default"
		}
		switch {
		case rows < 0 && key == branch.DefaultLimitTable:
			fmt.Printf("  %s: removed the default: tables without their own limit have none\n", b)
		case rows < 0:
			fmt.Printf("  %s: removed %s's own limit: it follows the default\n", b, what)
		default:
			fmt.Printf("  %s: %s is %d rows\n", b, what, rows)
		}
	}
	if failed {
		os.Exit(1)
	}
}

// limitHint says what to do about an error from a branch.
func limitHint(b string, err error) error {
	switch {
	case errors.Is(err, branch.ErrNoLimitsTable):
		return fmt.Errorf("%w; upgrade it with `%s ledger upgrade %s`", err, brand.CLI, b)
	case errors.Is(err, branch.ErrNoSuchTable):
		return fmt.Errorf("%w (name it as schema.table, or create it first)", err)
	}
	return err
}

func guardrailUsage() {
	fmt.Println("usage: fox guardrail limit [--branch <name>]                        show the agent row limits\n" +
		"       fox guardrail limit [--table <t>] [--branch <name>] <rows>   set one (no --table: the default)\n" +
		"       fox guardrail limit [--table <t>] [--branch <name>] none     remove one\n" +
		"       fox guardrail budget [--branch <name>]                       show the agent budgets\n" +
		"       fox guardrail budget [--table <t>] [--per <1h>] [--branch <name>] <rows>|none")
	os.Exit(2)
}

// guardrailBudgetCmd handles `fox guardrail budget`: how many rows one agent may
// change in a table over a window, across all its statements (bb.guard_impact).
//
//	fox guardrail budget [--branch <name>]                                 show the budgets
//	fox guardrail budget [--table <t>] [--per <dur>] [--branch <name>] <rows>   set one
//	fox guardrail budget [--table <t>] [--branch <name>] none              remove one
//
// --per is a window from 1m to 7d (1h if not given). Like `limit`, without
// --branch a change goes to main and every running branch.
func guardrailBudgetCmd(rest []string) {
	value := firstPositional(rest, "--branch", "--table", "--per")
	table := optValue(rest, "--table")
	branches, err := adminScope(optValue(rest, "--branch"))
	must(err)

	if value == "" {
		failed := false
		for _, b := range branches {
			budgets, err := branch.DMLBudgets(b)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s: %v\n", b, limitHint(b, err))
				failed = true
				continue
			}
			fmt.Printf("  %s:\n", b)
			hasDefault := false
			for _, l := range budgets {
				name := l.Table
				if name == branch.DefaultLimitTable {
					name, hasDefault = "default (every other table)", true
				}
				fmt.Printf("    %-32s %d rows per %s\n", name, l.MaxRows, window(l.PerSecs))
			}
			if !hasDefault {
				fmt.Printf("    %-32s none\n", "default (every other table)")
			}
		}
		fmt.Println("\nAn agent may change at most this many rows of a table in the window, across all its statements;")
		fmt.Printf("the statement that would pass it is refused and recorded. An admin can let one through with bb.allow_destructive=on (%s admin grant).\n", brand.CLI)
		if failed {
			os.Exit(1)
		}
		return
	}

	rows, per := int64(-1), 3600
	if value != "none" {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 0 {
			must(fmt.Errorf("the budget is a number of rows (0 or more), or none; got %q", value))
		}
		rows = n
		if v := optValue(rest, "--per"); v != "" {
			p, err := parseWindow(v)
			must(err)
			per = p
		}
	}
	failed := false
	for _, b := range branches {
		key, err := branch.SetDMLBudget(b, table, rows, per, cliActor())
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", b, limitHint(b, err))
			failed = true
			continue
		}
		what := key
		if key == branch.DefaultLimitTable {
			what = "the default"
		}
		switch {
		case rows < 0 && key == branch.DefaultLimitTable:
			fmt.Printf("  %s: removed the default: tables without their own budget have none\n", b)
		case rows < 0:
			fmt.Printf("  %s: removed %s's own budget: it follows the default\n", b, what)
		default:
			fmt.Printf("  %s: %s is %d rows per %s\n", b, what, rows, window(per))
		}
	}
	if failed {
		os.Exit(1)
	}
}

// parseWindow reads a budget window: a Go duration (90m, 2h) or days (1d, 7d),
// from a minute to a week, in whole seconds.
func parseWindow(v string) (int, error) {
	d, err := time.ParseDuration(v)
	if days, ok := strings.CutSuffix(v, "d"); ok && err != nil {
		n, err2 := strconv.Atoi(days)
		if err2 == nil {
			d, err = time.Duration(n)*24*time.Hour, nil
		}
	}
	if err != nil {
		return 0, fmt.Errorf("--per is a window like 30m, 1h or 1d, not %q", v)
	}
	s := int(d / time.Second)
	if s < branch.MinBudgetWindow || s > branch.MaxBudgetWindow {
		return 0, fmt.Errorf("--per is from 1m to 7d, not %s", v)
	}
	return s, nil
}

// window says a budget's window as a person reads it.
func window(secs int) string {
	switch {
	case secs == 3600:
		return "hour"
	case secs == 86400:
		return "day"
	case secs%86400 == 0:
		return fmt.Sprintf("%d days", secs/86400)
	case secs%3600 == 0:
		return fmt.Sprintf("%d hours", secs/3600)
	case secs%60 == 0:
		return fmt.Sprintf("%d minutes", secs/60)
	}
	return fmt.Sprintf("%d seconds", secs)
}
