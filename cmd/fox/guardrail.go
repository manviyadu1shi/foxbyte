// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

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
		"       fox guardrail limit [--table <t>] [--branch <name>] none     remove one")
	os.Exit(2)
}
