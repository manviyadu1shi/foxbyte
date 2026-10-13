// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"encoding/json"
	"strings"
	"testing"
)

// Carrying the accounts to another node and reading them back, which is phase
// 2's whole content as far as this package is concerned. Two stores stand in
// for two machines.

func TestSharedStateCarriesWhatASecondMachineNeeds(t *testing.T) {
	a, b := testStore(t), testStore(t)

	u, err := a.CreateUser("alice@x.com", "password1")
	if err != nil {
		t.Fatal(err)
	}
	key, info, err := a.CreateAPIKey(u.ID, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.CreateScopedAPIKey(u.ID, "agent", "agent-7"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetBranchOwner("alice-dev", u.ID); err != nil {
		t.Fatal(err)
	}

	doc, digest, err := a.SharedState()
	if err != nil {
		t.Fatal(err)
	}
	// The document is for another machine, so a credential must not be in it in
	// a form that machine (or anything that reads the document) could use
	// directly: a key is stored as a hash and travels as one.
	if strings.Contains(string(doc), key) {
		t.Error("the document carries an API key in the clear")
	}
	if !strings.Contains(string(doc), info.Prefix) {
		t.Error("the document does not carry the key at all")
	}

	got, err := b.ApplySharedState(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Changed() || got.Digest != digest {
		t.Errorf("apply = %+v, want a change and digest %s", got, digest)
	}

	// The property that matters: the key minted on one machine opens the other.
	vu, scope, ok := b.VerifyKey(key)
	if !ok || vu.Email != "alice@x.com" || scope != "" {
		t.Errorf("the key does not work on the second node: %+v %q %v", vu, scope, ok)
	}
	// And so does signing in, which is the phase's stated unlock.
	if _, err := b.Login("alice@x.com", "password1"); err != nil {
		t.Errorf("signing in on the second node: %v", err)
	}
	// Authorisation travels with it, or an account arrives owning nothing.
	if owner, ok := b.BranchOwner("alice-dev"); !ok || owner != u.ID {
		t.Errorf("branch owner = %v, %v, want %d", owner, ok, u.ID)
	}
	// The id is the same on both, which everything else keys on: api_keys,
	// branch_owners and change_requests all carry a user_id.
	if vb, ok := b.UserByEmail("alice@x.com"); !ok || vb.ID != u.ID {
		t.Errorf("the account arrived with id %v, not %d", vb.ID, u.ID)
	}
}

// Applying the same document twice must change nothing the second time, because
// the follower applies whatever it is given whenever the digest moves, and an
// apply that always reports a change would be indistinguishable from a real one.
func TestApplySharedStateIsIdempotent(t *testing.T) {
	a, b := testStore(t), testStore(t)
	u, _ := a.CreateUser("alice@x.com", "password1")
	_, _, _ = a.CreateAPIKey(u.ID, "laptop")
	doc, _, _ := a.SharedState()

	if _, err := b.ApplySharedState(doc); err != nil {
		t.Fatal(err)
	}
	second, err := b.ApplySharedState(doc)
	if err != nil {
		t.Fatal(err)
	}
	if second.Removed != 0 {
		t.Errorf("a second apply removed %d rows", second.Removed)
	}
	// Written counts the upserts, which do run again; what must not change is
	// the store, and the digest is how the node decides that.
	d, err := b.SharedStateDigest()
	if err != nil {
		t.Fatal(err)
	}
	if d != second.Digest {
		t.Errorf("after applying, the store's digest is %s and the document's %s", d, second.Digest)
	}
}

// A revoked key must be revoked everywhere. This is the half an upsert cannot
// do, and the reason the follower deletes what the document no longer carries.
func TestSharedStateCarriesRemovals(t *testing.T) {
	a, b := testStore(t), testStore(t)
	u, _ := a.CreateUser("alice@x.com", "password1")
	key, info, _ := a.CreateAPIKey(u.ID, "laptop")
	doc, _, _ := a.SharedState()
	if _, err := b.ApplySharedState(doc); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := b.VerifyKey(key); !ok {
		t.Fatal("the key did not arrive")
	}

	if err := a.RevokeKey(u.ID, info.ID); err != nil {
		t.Fatal(err)
	}
	doc2, _, _ := a.SharedState()
	got, err := b.ApplySharedState(doc2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Removed != 1 {
		t.Errorf("removed %d rows, want 1", got.Removed)
	}
	if _, _, ok := b.VerifyKey(key); ok {
		t.Error("a key revoked on the primary still opens the second node")
	}
	// The account itself is untouched: only what the document dropped goes.
	if _, ok := b.UserByEmail("alice@x.com"); !ok {
		t.Error("removing a key removed the account")
	}
}

// What counts as a change. `last_used` is bumped on every authenticated
// request, so counting it would make the document "changed" every few seconds
// for ever and the node would ship the accounts in a permanent loop to report
// that somebody used a key.
func TestKeyUseIsNotAChangeWorthShipping(t *testing.T) {
	a := testStore(t)
	u, _ := a.CreateUser("alice@x.com", "password1")
	key, _, _ := a.CreateAPIKey(u.ID, "laptop")
	_, before, err := a.SharedState()
	if err != nil {
		t.Fatal(err)
	}

	if _, _, ok := a.VerifyKey(key); !ok { // bumps last_used
		t.Fatal("the key does not work")
	}
	doc, after, err := a.SharedState()
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Error("using a key changed the digest, so the node would ship on every request")
	}
	// It still rides along, so a node that gets the document for another reason
	// learns when the key was last used.
	if !strings.Contains(string(doc), "last_used") {
		t.Error("last_used is not in the document at all")
	}
	// Something real does change it.
	if _, err := a.CreateUser("bob@x.com", "password1"); err != nil {
		t.Fatal(err)
	}
	if _, d, _ := a.SharedState(); d == before {
		t.Error("a new account did not change the digest")
	}
}

// The two tables that are deliberately left behind, and the reason each is.
func TestSharedStateLeavesSessionsAndTheChainBehind(t *testing.T) {
	a := testStore(t)
	u, _ := a.CreateUser("alice@x.com", "password1")
	tok, err := a.createSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.Audit(EvLoginOK, "alice@x.com", "", "127.0.0.1", "")
	doc, _, err := a.SharedState()
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Tables map[string]any `json:"tables"`
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		t.Fatal(err)
	}
	for _, left := range []string{"sessions", "security_events", "pipeline_runs", "realtime_activity"} {
		if _, ok := d.Tables[left]; ok {
			t.Errorf("the document carries %s", left)
		}
	}
	// A session token is a bearer credential; it must not be in the document at
	// all, not merely absent from a table list.
	if strings.Contains(string(doc), tok) {
		t.Error("the document carries a session token")
	}
}

// A node must refuse a document it does not understand rather than apply the
// half of it that looks familiar: half an account list is worse than none,
// because the missing half is somebody who cannot sign in.
func TestApplySharedStateRefusesWhatItCannotRead(t *testing.T) {
	b := testStore(t)
	for _, bad := range []struct{ what, doc string }{
		{"not JSON", "{"},
		{"a newer version", `{"version":99,"tables":{}}`},
		{"a missing table", `{"version":1,"tables":{"users":{"cols":["id","email","pw_hash","created"],"rows":[]}}}`},
		{"different columns", `{"version":1,"tables":{"users":{"cols":["id","email"],"rows":[]}}}`},
	} {
		if _, err := b.ApplySharedState([]byte(bad.doc)); err == nil {
			t.Errorf("%s was accepted", bad.what)
		}
	}
	// And after every refusal the store is untouched.
	if b.HasAnyUser() {
		t.Error("a refused document left accounts behind")
	}
}

// The document is applied in one transaction, so a document that fails halfway
// leaves nothing behind. Two accounts with one address is the realistic way for
// that to happen -- users.email is UNIQUE, nothing else in this schema declares
// a foreign key on user_id -- and the rows written before it must go too.
func TestApplySharedStateIsAllOrNothing(t *testing.T) {
	a, b := testStore(t), testStore(t)
	u, _ := a.CreateUser("alice@x.com", "password1")
	_, _, _ = a.CreateAPIKey(u.ID, "laptop")
	doc, _, _ := a.SharedState()

	var d sharedDoc
	if err := json.Unmarshal(doc, &d); err != nil {
		t.Fatal(err)
	}
	// A second account claiming the first one's address.
	d.Tables["users"].Rows = append(d.Tables["users"].Rows,
		[]*string{strptr("42"), strptr("alice@x.com"), strptr("x"), strptr("1")})
	broken, _ := json.Marshal(d)

	if _, err := b.ApplySharedState(broken); err == nil {
		t.Fatal("a document with two accounts on one address was accepted")
	}
	if b.HasAnyUser() {
		t.Error("a failed apply left accounts behind")
	}
	// And the good document still applies afterwards: a refusal must not leave
	// the store unable to accept the next one.
	if _, err := b.ApplySharedState(doc); err != nil {
		t.Fatalf("after a refusal, a good document was refused too: %v", err)
	}
	if !b.HasAnyUser() {
		t.Error("the good document applied nothing")
	}
}

func strptr(s string) *string { return &s }
