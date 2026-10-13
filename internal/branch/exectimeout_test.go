// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The defaults have to be far enough apart to mean something: a base backup of a
// large database is legitimately long, while an inspect answering slowly is a
// fault.
func TestExecTiersAreOrdered(t *testing.T) {
	if !(execProbe < execClean && execClean < execQuick && execQuick < execLong) {
		t.Errorf("tiers out of order: probe %s, clean %s, quick %s, long %s",
			execProbe, execClean, execQuick, execLong)
	}
	// The probe tier exists for `fox check`, which someone runs when the
	// install is already broken. A probe measured in minutes would be no
	// different from the hang it replaced.
	if execProbe > 30*time.Second {
		t.Errorf("the probe tier is %s; `check` cannot wait that long for one line", execProbe)
	}
}

// A longer FOX_EXEC_TIMEOUT must not lengthen the probe: it is set to let slow
// real work finish, and `check` waiting an hour for one line is not that.
func TestLongExecTimeoutLeavesTheProbeShort(t *testing.T) {
	t.Setenv("FOX_EXEC_TIMEOUT", "3h")
	if got := execTimeout(execProbe); got != execProbe {
		t.Errorf("a 3h setting stretched the probe to %s", got)
	}
	// A shorter one still applies, as it does to every tier.
	t.Setenv("FOX_EXEC_TIMEOUT", "2s")
	if got := execTimeout(execProbe); got != 2*time.Second {
		t.Errorf("a 2s setting left the probe at %s", got)
	}
}

// FOX_EXEC_TIMEOUT: a shorter setting tightens every tier, a longer one only
// raises the long one, and "off" restores the old unbounded behaviour.
func TestExecTimeoutEnv(t *testing.T) {
	t.Setenv("FOX_EXEC_TIMEOUT", "")
	if got := execTimeout(execLong); got != execLong {
		t.Errorf("unset gave %s, want the default %s", got, execLong)
	}
	t.Setenv("FOX_EXEC_TIMEOUT", "30s")
	for _, tier := range []time.Duration{execClean, execQuick, execLong} {
		if got := execTimeout(tier); got != 30*time.Second {
			t.Errorf("a 30s setting left tier %s at %s", tier, got)
		}
	}
	t.Setenv("FOX_EXEC_TIMEOUT", "3h")
	if got := execTimeout(execLong); got != 3*time.Hour {
		t.Errorf("a longer setting did not raise the long tier: %s", got)
	}
	if got := execTimeout(execQuick); got != execQuick {
		t.Errorf("a longer setting should not slow the quick tier: %s", got)
	}
	for _, off := range []string{"off", "0", "false", "no"} {
		t.Setenv("FOX_EXEC_TIMEOUT", off)
		if got := execTimeout(execLong); got != 0 {
			t.Errorf("%q should switch deadlines off, got %s", off, got)
		}
	}
	// Nonsense is ignored rather than becoming no timeout at all.
	t.Setenv("FOX_EXEC_TIMEOUT", "not-a-duration")
	if got := execTimeout(execQuick); got != execQuick {
		t.Errorf("a bad value changed the deadline to %s", got)
	}
}

// With deadlines off there is no context, so nothing can cancel the command —
// the behaviour this replaced, kept available for whoever needs it.
func TestSudoCmdOffHasNoContext(t *testing.T) {
	t.Setenv("FOX_EXEC_TIMEOUT", "off")
	cmd, ctx, cancel := sudoCmd(execQuick, "true")
	defer cancel()
	if ctx != nil {
		t.Error("deadlines are off, so there should be no context")
	}
	if cmd == nil {
		t.Fatal("no command")
	}
}

// A command that outlives its deadline is stopped, and the error says so — the
// whole point, since before this it simply never returned.
func TestSudoCmdStopsALongCommand(t *testing.T) {
	t.Setenv("FOX_EXEC_TIMEOUT", "300ms")
	cmd, ctx, cancel := sudoCmd(execQuick, "sleep", "30")
	defer cancel()
	// Run `sleep` directly: the test machine may not have passwordless sudo, and
	// what is being checked is the deadline, not the privilege.
	direct := exec.CommandContext(ctx, "sleep", "30")
	direct.WaitDelay = cmd.WaitDelay
	start := time.Now()
	err := direct.Run()
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the command ran for %s; the deadline did not stop it", took)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Errorf("context error was %v, want DeadlineExceeded", ctx.Err())
	}
	msg := execErr(ctx, execQuick, "sleep", err).Error()
	for _, want := range []string{"sleep", "did not finish within", "FOX_EXEC_TIMEOUT"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error %q does not mention %q", msg, want)
		}
	}
}

// An ordinary failure must not be dressed up as a timeout.
func TestExecErrLeavesOtherFailuresAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	plain := errors.New("exit status 1")
	if got := execErr(ctx, execQuick, "docker", plain); got != plain {
		t.Errorf("execErr changed a plain failure into %v", got)
	}
	if got := execErr(ctx, execQuick, "docker", nil); got != nil {
		t.Errorf("execErr invented an error: %v", got)
	}
	if got := execErr(nil, execQuick, "docker", plain); got != plain {
		t.Errorf("execErr with no context changed the error to %v", got)
	}
}

// What a failing command said, which is the difference between a line somebody
// can act on and "exit status 1".
//
// Reported against `fox check`: its Blackbox line said "cannot verify the
// record: verify query: exit status 1" — true, and useless, because psql had
// written the reason to stderr and capture dropped it.
func TestCaptureErrorsKeepWhatTheCommandSaid(t *testing.T) {
	psql := func(stderr string) error {
		return withStderr(&exec.ExitError{ProcessState: nil, Stderr: []byte(stderr)})
	}

	t.Run("the reason reaches the error", func(t *testing.T) {
		got := psql("psql: error: connection to server at \"pg-main\" failed: Connection refused\n" +
			"\tIs the server running on that host and accepting TCP/IP connections?\n").Error()
		if !strings.Contains(got, "Connection refused") {
			t.Errorf("the error %q does not say what went wrong", got)
		}
		// One line, so a caller that prints the first line of an error prints
		// the part that matters and nothing else.
		if strings.Contains(got, "\n") {
			t.Errorf("the error spans lines:\n%s", got)
		}
	})

	t.Run("a command that said nothing is left alone", func(t *testing.T) {
		bare := &exec.ExitError{}
		if got := withStderr(bare); got != error(bare) {
			t.Errorf("withStderr changed an empty failure into %v", got)
		}
	})

	t.Run("something that is not a command failure is untouched", func(t *testing.T) {
		// A deadline, for one: execErr says something better about it than a
		// killed command's half-written stderr would.
		plain := errors.New("context deadline exceeded")
		if got := withStderr(plain); got != plain {
			t.Errorf("withStderr changed %v", got)
		}
		if withStderr(nil) != nil {
			t.Error("withStderr invented an error")
		}
	})

	t.Run("the original error still matches", func(t *testing.T) {
		// Callers compare errors, so wrapping must not break errors.Is/As.
		ee := &exec.ExitError{Stderr: []byte("boom")}
		var as *exec.ExitError
		if !errors.As(withStderr(ee), &as) {
			t.Error("the wrapped error is no longer an *exec.ExitError")
		}
	})
}

// stderrLine is what bounds the above, and everything it handles arrives from
// outside the process.
func TestStderrLine(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"nothing", "", ""},
		{"blank lines only", "\n  \n\t\n", ""},
		{"a leading blank line is skipped", "\n\nERROR:  relation \"x\" does not exist",
			`ERROR:  relation "x" does not exist`},
		{"trailing whitespace goes", "  psql: fatal  \n", "psql: fatal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stderrLine([]byte(c.in)); got != c.want {
				t.Errorf("stderrLine(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	t.Run("a very long line is cut and marked", func(t *testing.T) {
		got := stderrLine([]byte(strings.Repeat("x", stderrShown+50)))
		if len([]rune(got)) != stderrShown+1 || !strings.HasSuffix(got, "…") {
			t.Errorf("a long line became %d runes ending %q", len([]rune(got)), got[len(got)-4:])
		}
	})

	t.Run("it cuts by rune, not by byte", func(t *testing.T) {
		// Postgres answers in the server's locale, so an error message is not
		// necessarily ASCII, and half a character is not a character.
		got := stderrLine([]byte(strings.Repeat("é", stderrShown+10)))
		if !strings.HasSuffix(got, "é…") {
			t.Errorf("a multi-byte line was cut mid-character: %q", got[len(got)-6:])
		}
	})
}
