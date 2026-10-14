// SPDX-License-Identifier: AGPL-3.0-or-later

package access

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/thefoxbyte/foxbyte/internal/auth"
)

func storeWith(t *testing.T) (*auth.Store, auth.User, auth.User, auth.User) {
	t.Helper()
	s, err := auth.Open(auth.Config{DBPath: filepath.Join(t.TempDir(), "a.db"), WebOrigin: "http://x", SignupOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	admin, _ := s.CreateUser("admin@x.com", "password1")
	alice, _ := s.CreateUser("alice@x.com", "password1")
	bob, _ := s.CreateUser("bob@x.com", "password1")
	return s, admin, alice, bob
}

func TestLevels(t *testing.T) {
	s, admin, alice, bob := storeWith(t)
	c := NewWith(s, func(e string) bool { return e == "admin@x.com" })
	if err := c.Own(alice, "alice-dev"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		who    auth.User
		branch string
		want   Level
	}{
		{admin, "alice-dev", Manage}, // an admin reaches everything
		{admin, "nobodys", Manage},
		{alice, "alice-dev", Manage}, // the owner
		{bob, "alice-dev", None},     // someone else's branch is not there
		{alice, "main", Use},         // main is shared
		{bob, "", Use},               // "" is main
		{alice, "nobodys", None},     // no owner: an admin's
		{auth.User{}, "main", Use},   // (no account id cannot own anything)
		{auth.User{}, "alice-dev", None},
	} {
		if got := c.Level(tc.who, tc.branch); got != tc.want {
			t.Errorf("%s on %q: %v, want %v", tc.who.Email, tc.branch, got, tc.want)
		}
	}
	// A deleted branch's owner is forgotten, so a new branch of that name is
	// not handed to the old owner.
	c.Forget("alice-dev")
	if c.Level(alice, "alice-dev") != None {
		t.Error("the owner of a deleted branch still reaches a branch of the same name")
	}
	// A deleted account's branches go to the admins.
	_ = c.Own(bob, "bob-dev")
	if err := s.DeleteAccount(bob.ID); err != nil {
		t.Fatal(err)
	}
	if c.Level(bob, "bob-dev") != None {
		t.Error("a deleted account still owns its branch")
	}
}

func TestAdminIsCachedBriefly(t *testing.T) {
	s, _, alice, _ := storeWith(t)
	calls, admin := 0, true
	c := NewWith(s, func(string) bool { calls++; return admin })
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	c.Admin(alice)
	c.Admin(alice)
	if calls != 1 {
		t.Errorf("asked %d times inside the TTL, want 1", calls)
	}
	admin = false
	now = now.Add(adminTTL)
	if c.Admin(alice) || calls != 2 {
		t.Errorf("a revoked admin was still an admin after the TTL (calls %d)", calls)
	}
}

// A grant or revoke the security log records reaches the cache within a
// second, wherever it was made, instead of when the old answer expires.
func TestARecordedRevokeBitesWithinASecond(t *testing.T) {
	s, _, alice, _ := storeWith(t)
	calls, admin := 0, true
	c := NewWith(s, func(string) bool { calls++; return admin })
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	if !c.Admin(alice) {
		t.Fatal("not an admin to begin with")
	}

	// Revoked elsewhere (the CLI, another process), and recorded as such.
	admin = false
	s.Audit(auth.EvAdminRevoked, "root@x.com", alice.Email, "", "branch main")

	now = now.Add(epochEvery / 2) // the log is not asked again this soon
	if !c.Admin(alice) || calls != 1 {
		t.Fatalf("asked inside epochEvery (calls %d)", calls)
	}
	now = now.Add(epochEvery) // well inside adminTTL
	if c.Admin(alice) {
		t.Error("a recorded revoke had not bitten after a second")
	}
	if calls != 2 {
		t.Errorf("calls %d, want 2", calls)
	}
}

// Anything else the log records leaves the remembered answers alone.
func TestOtherEventsKeepTheCache(t *testing.T) {
	s, _, alice, _ := storeWith(t)
	calls := 0
	c := NewWith(s, func(string) bool { calls++; return true })
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	c.Admin(alice)
	s.Audit(auth.EvLoginFailed, "", alice.Email, "203.0.113.1", "")
	now = now.Add(2 * epochEvery)
	c.Admin(alice)
	if calls != 1 {
		t.Errorf("an unrelated event emptied the cache (calls %d)", calls)
	}
}
