// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
)

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The operator's loads are what they always were: the superuser, and the input
// passed through untouched.
func TestOperatorLoadsAreUnchanged(t *testing.T) {
	var op As
	if op.confined() || op.psqlRole() != pgUser {
		t.Fatalf("the zero As must be the operator running as %s, got %q", pgUser, op.psqlRole())
	}
	in := "\\restrict abc\nCREATE TABLE t (a int);\n\\unrestrict abc\n"
	if got := readAll(t, op.restricted(strings.NewReader(in))); got != in {
		t.Fatalf("operator input changed:\n%q\n%q", in, got)
	}
	if f := dumpRestrictFlag(op); f != "" {
		t.Fatalf("operator pg_dump gained a flag: %q", f)
	}
	if err := errors.New("permission denied"); explainAs(op, err) != err {
		t.Fatal("an operator's error was rewritten")
	}
}

var restrictLine = regexp.MustCompile(`^\\restrict [0-9a-f]{32}\n`)

func TestAnAccountsLoadIsRestrictedAndRunsAsItself(t *testing.T) {
	a := As{Account: "ann@example.com"}
	if !a.confined() || a.psqlRole() != "ann@example.com" {
		t.Fatalf("an account's load must run as the account, got %q", a.psqlRole())
	}
	got := readAll(t, a.restricted(strings.NewReader("SELECT 1;\n\\set x 1\nSELECT 2;")))
	if !restrictLine.MatchString(got) {
		t.Fatalf("not restricted first: %q", got)
	}
	if rest := restrictLine.ReplaceAllString(got, ""); rest != "SELECT 1;\n\\set x 1\nSELECT 2;" {
		t.Fatalf("content changed: %q", rest)
	}
}

// A file a current pg_dump wrote restricts itself; its own pair of lines is
// dropped so a normal dump loads without complaint. Nothing else is.
func TestADumpsOwnRestrictLinesAreDropped(t *testing.T) {
	a := As{Account: "ann@example.com"}
	in := "\\restrict Kdump1\nCREATE TABLE t (a int);\n\\unrestrict Kother\n\\unrestrict Kdump1\n"
	got := restrictLine.ReplaceAllString(readAll(t, a.restricted(strings.NewReader(in))), "")
	want := "CREATE TABLE t (a int);\n\\unrestrict Kother\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// After pg_dump's header comments, as a real dump has it.
	in = "--\n-- PostgreSQL database dump\n--\n\n\\restrict Kdump2\n\nCREATE TABLE t (a int);\n\\unrestrict Kdump2\n"
	got = restrictLine.ReplaceAllString(readAll(t, a.restricted(strings.NewReader(in))), "")
	if want := "--\n-- PostgreSQL database dump\n--\n\n\nCREATE TABLE t (a int);\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Only the first line that counts can be the dump's own.
	in = "SELECT 1;\n\\restrict Klater\n\\unrestrict Klater\n"
	got = restrictLine.ReplaceAllString(readAll(t, a.restricted(strings.NewReader(in))), "")
	if got != in {
		t.Fatalf("a later restrict pair was dropped: %q", got)
	}
}

func TestEachConfinedDumpGetsItsOwnKey(t *testing.T) {
	a := As{Account: "ann@example.com"}
	f1, f2 := dumpRestrictFlag(a), dumpRestrictFlag(a)
	if !regexp.MustCompile(`^ --restrict-key=[0-9a-f]{32}$`).MatchString(f1) || f1 == f2 {
		t.Fatalf("flags %q %q", f1, f2)
	}
}

func TestAConfinedFailureSaysWhoItRanAs(t *testing.T) {
	a := As{Account: "ann@example.com"}
	err := explainAs(a, errors.New(`ERROR:  permission denied to create extension "x"`))
	if !strings.Contains(err.Error(), "ann@example.com's own privileges") {
		t.Fatalf("no explanation: %v", err)
	}
	other := errors.New("syntax error at or near")
	if explainAs(a, other) != other {
		t.Fatal("an unrelated error was rewritten")
	}
}

// A refused backslash command did not run, so it counts as a failed statement.
func TestARefusedBackslashCommandCountsAsAFailure(t *testing.T) {
	c := &psqlErrorCollector{}
	_, _ = c.Write([]byte("psql:<stdin>:3: backslash commands are restricted; only \\unrestrict is allowed\nNOTICE:  fine\n"))
	c.flush()
	if c.count != 1 {
		t.Fatalf("count = %d, want 1", c.count)
	}
}
