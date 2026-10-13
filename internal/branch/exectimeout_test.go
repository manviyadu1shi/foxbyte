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
