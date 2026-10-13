// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"errors"
	"strings"
	"testing"
)

// The routing key, which is the whole public surface of this feature: a session
// asks for the copy by name, and everything after that is about the branch.
func TestSplitReadTarget(t *testing.T) {
	cases := []struct {
		in      string
		name    string
		standby bool
	}{
		{"main", "main", false},
		{"main:standby", "main", true},
		{"demo", "demo", false},
		{"demo:standby", "demo", true},
		// Not a suffix: a branch called "standby" is an ordinary branch, and
		// the separator alone names nothing.
		{"standby", "standby", false},
		{":standby", ":standby", false},
		{"", "", false},
		// The suffix is a suffix, not a word to be found anywhere.
		{"main:standby:standby", "main:standby", true},
		{"standby:main", "standby:main", false},
	}
	for _, c := range cases {
		name, standby := SplitReadTarget(c.in)
		if name != c.name || standby != c.standby {
			t.Errorf("SplitReadTarget(%q) = (%q, %v), want (%q, %v)", c.in, name, standby, c.name, c.standby)
		}
	}
}

// The separator has to be one a branch cannot contain, or the feature reserves
// a name somebody might legitimately want and shadows their branch with a copy
// of another one.
func TestTheSuffixCannotBeABranchName(t *testing.T) {
	if ValidName("main" + StandbySuffix) {
		t.Errorf("%q is a valid branch name, so the routing key is ambiguous", "main"+StandbySuffix)
	}
	if !ValidName("main") {
		t.Error("the test is not checking anything: \"main\" is not a valid name either")
	}
}

// Which states may serve a read, and which must refuse.
//
// The promoted standby is the one that matters most: a read-only DSN that
// quietly became a connection to the primary would keep working and keep
// looking right, while putting every read back on the machine the reader was
// trying to spare.
func TestStandbyReadRefusal(t *testing.T) {
	t.Run("a running standby on the primary's own machine serves reads", func(t *testing.T) {
		if err := standbyReadRefusal("main", "main", "running", false); err != nil {
			t.Errorf("refused a healthy standby: %v", err)
		}
	})

	t.Run("no standby at all", func(t *testing.T) {
		err := standbyReadRefusal("main", "main", "absent", false)
		if !errors.Is(err, ErrNoStandbyRead) {
			t.Fatalf("err = %v, want it to wrap ErrNoStandbyRead", err)
		}
		if !strings.Contains(err.Error(), "ha enable") {
			t.Errorf("the refusal does not say how to get one: %v", err)
		}
	})

	t.Run("the standby was promoted", func(t *testing.T) {
		err := standbyReadRefusal("main", "standby", "running", false)
		if err == nil {
			t.Fatal("a promoted standby was treated as a second copy")
		}
		// Not ErrNoStandbyRead: this one comes back, and the gateway tells the
		// client to retry rather than that the name is wrong.
		if errors.Is(err, ErrNoStandbyRead) {
			t.Error("a promoted standby is reported as a name that does not exist")
		}
		for _, want := range []string{"promoted", "ha failback"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not mention %q: %v", want, err)
			}
		}
	})

	t.Run("a branch has no standby of its own", func(t *testing.T) {
		err := standbyReadRefusal("demo", "main", "running", false)
		if !errors.Is(err, ErrNoStandbyRead) {
			t.Fatalf("err = %v, want it to wrap ErrNoStandbyRead", err)
		}
		if !strings.Contains(err.Error(), `"demo"`) {
			t.Errorf("the refusal does not name the branch: %v", err)
		}
		// And says where to go instead, which here is a name rather than a
		// command: reading a branch is reading the branch.
		if !strings.Contains(err.Error(), "only main has a standby") {
			t.Errorf("the refusal does not say why: %v", err)
		}
	})

	t.Run("a standby that is not running has nothing to answer with", func(t *testing.T) {
		err := standbyReadRefusal("main", "main", "exited", false)
		if err == nil {
			t.Fatal("a stopped standby was routed to")
		}
		if !strings.Contains(err.Error(), "exited") {
			t.Errorf("the refusal does not say what it is doing: %v", err)
		}
	})

	t.Run("a machine that follows a primary elsewhere", func(t *testing.T) {
		// The case the plan wants and this does not deliver: the copy on a
		// joined node carries the remote primary's roles, whose passwords come
		// from that install's secret, and `node join` deliberately carries no
		// secret. The gateway here would authenticate with its own. Measured,
		// not assumed — and refused with the reason rather than failing at the
		// backend handshake with "authentication failed".
		err := standbyReadRefusal("main", "main", "running", true)
		if !errors.Is(err, ErrNoStandbyRead) {
			t.Fatalf("err = %v, want it to wrap ErrNoStandbyRead", err)
		}
		for _, want := range []string{"follows a primary elsewhere", "roles"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not mention %q: %v", want, err)
			}
		}
	})

	t.Run("being a joined node is answered before the branch name", func(t *testing.T) {
		// Both are true on a joined node asked for a branch's copy, and the
		// one worth saying is the one the operator can act on.
		err := standbyReadRefusal("demo", "main", "running", true)
		if !strings.Contains(err.Error(), "follows a primary elsewhere") {
			t.Errorf("the less useful refusal won: %v", err)
		}
	})
}
