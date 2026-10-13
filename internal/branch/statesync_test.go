// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thefoxbyte/foxbyte/internal/brand"
)

// Which node writes the accounts, which copies them, and which does neither.
//
// The one thing this must never do is let two nodes write: that is how an
// account revoked on one machine comes back from the other. A node follows if
// and only if it follows a primary elsewhere — and `ha failover` clears that
// record, so a promotion makes a node the writer by the same act that makes it
// the primary.
func TestStateSyncDuty(t *testing.T) {
	cases := []struct {
		what                       string
		joined                     bool
		standbyState, primaryState string
		want                       stateDuty
		wantCont                   string
	}{
		{"an ordinary install owns its accounts", false, "absent", "running", stateLead, container("main")},
		{"…and still does with a standby beside it", false, "running", "running", stateLead, container("main")},
		{"a joined node copies them from its own copy", true, "running", "exited", stateFollow, container("standby")},
		{"nothing to carry them through", false, "absent", "exited", stateIdle, ""},
		{"a joined node whose copy is not running", true, "exited", "exited", stateIdle, ""},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			got, cont := stateSyncDuty(c.joined, c.standbyState, c.primaryState)
			if got != c.want || cont != c.wantCont {
				t.Errorf("stateSyncDuty(%v, %q, %q) = (%v, %q), want (%v, %q)",
					c.joined, c.standbyState, c.primaryState, got, cont, c.want, c.wantCont)
			}
		})
	}

	t.Run("exactly one duty writes", func(t *testing.T) {
		// Asserted as a property rather than read off the table above: if a
		// third duty is ever added, this is the line that has to be thought
		// about again.
		writes := 0
		for _, d := range []stateDuty{stateIdle, stateLead, stateFollow} {
			if d.String() == "ships" {
				writes++
			}
		}
		if writes != 1 {
			t.Errorf("%d duties ship the document, want exactly 1", writes)
		}
	})
}

// A node that follows must never be the one writing, whatever its containers
// are doing. This is the same rule as above, stated the way a reader of an
// incident report would ask it.
func TestAJoinedNodeNeverShips(t *testing.T) {
	for _, standby := range []string{"running", "exited", "absent", "created", "restarting"} {
		for _, primary := range []string{"running", "exited", "absent"} {
			if d, _ := stateSyncDuty(true, standby, primary); d == stateLead {
				t.Errorf("a joined node with standby=%q primary=%q ships the document", standby, primary)
			}
		}
	}
}

func TestStateSyncInterval(t *testing.T) {
	env := brand.EnvName(envStateSync)
	for _, c := range []struct {
		set  string
		want time.Duration
	}{
		{"", stateSyncDefault},
		{"off", 0},
		{"0", 0},
		{"45s", 45 * time.Second},
		{"not-a-duration", stateSyncDefault}, // said, not obeyed
		{"-5s", stateSyncDefault},
	} {
		t.Run("("+c.set+")", func(t *testing.T) {
			if c.set == "" {
				os.Unsetenv(env)
			} else {
				os.Setenv(env, c.set)
			}
			defer os.Unsetenv(env)
			if got := stateSyncInterval(); got != c.want {
				t.Errorf("%s=%q gave %s, want %s", env, c.set, got, c.want)
			}
		})
	}
}

// "Nothing has shipped here yet" and "this is broken" must not be confused: the
// first is how every install starts and how a node looks the second after it
// joins, and reporting it as a fault would mean an error in the log every ten
// seconds on a healthy machine.
func TestMissingStateIsNotAFault(t *testing.T) {
	for _, e := range []error{
		errors.New(`ERROR: database "statedb" does not exist (SQLSTATE 3D000)`),
		errors.New(`ERROR: relation "shared" does not exist (SQLSTATE 42P01)`),
		errors.New("no rows in result set"),
	} {
		if !missingState(e) {
			t.Errorf("%v was read as a fault", e)
		}
	}
	for _, e := range []error{
		errors.New("connection refused"),
		errors.New(`password authentication failed for user "dbadmin"`),
		errors.New("context deadline exceeded"),
		nil,
	} {
		if missingState(e) {
			t.Errorf("%v was read as nothing shipped yet", e)
		}
	}
}

// The document goes into a database of its own, and that matters for a reason
// that has nothing to do with HA: `fox export` dumps a branch's appdb, and
// password hashes in there would travel inside somebody's portable export.
func TestTheSharedStateIsNotInTheApplicationDatabase(t *testing.T) {
	if StateDatabase == pgDatabase {
		t.Fatalf("the shared state is in %q, which `fox export` dumps", StateDatabase)
	}
	if strings.Contains(StateDatabase, brand.Product) || strings.Contains(StateDatabase, brand.CLI) {
		t.Errorf("%q carries the product name; stored names are brand-free and frozen", StateDatabase)
	}
}

// The wiring: an idle duty reaches neither branch of SyncSharedState's switch,
// which matters because that function is called every ten seconds for the life
// of the process and a stopped install is an ordinary state to be in.
func TestAnIdleNodeTouchesNothing(t *testing.T) {
	duty, cont := stateSyncDuty(false, "absent", "absent")
	if duty != stateIdle || cont != "" {
		t.Fatalf("duty = %v, %q", duty, cont)
	}
	if fmt.Sprint(duty) != "idle" {
		t.Errorf("an idle duty describes itself as %q", duty)
	}
}
