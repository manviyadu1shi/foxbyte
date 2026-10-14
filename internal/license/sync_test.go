// SPDX-License-Identifier: AGPL-3.0-or-later

package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thefoxbyte/foxbyte/internal/edition"
)

// trustKeyForTest makes this build trust a key the test made, and points the
// licence at a temporary file, undoing both afterwards.
func trustKeyForTest(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, oldPath := licensePublicKey, pathOverride
	licensePublicKey = base64.StdEncoding.EncodeToString(pub)
	dir := t.TempDir()
	pathOverride = filepath.Join(dir, FileName)
	t.Cleanup(func() { licensePublicKey, pathOverride = oldKey, oldPath })
	return priv, dir
}

// storedFile is a licence file as Save writes it, activated at a given time.
func storedFile(t *testing.T, priv ed25519.PrivateKey, id string, activated time.Time) []byte {
	t.Helper()
	l := License{
		ID: id, Customer: "Acme Ltd", Edition: "enterprise",
		Features: []string{"anchors", "realtime"},
		IssuedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		NotAfter: time.Now().AddDate(1, 0, 0).UTC(),
	}
	Sign(&l, priv)
	b, err := json.Marshal(stored{License: l, ActivatedAt: activated})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Which copy both sides should hold. The case that motivated all of this is
// the third: the console activated a licence in the VM, and the host — with an
// older one or none — must not push over it.
func TestReconcile(t *testing.T) {
	priv, _ := trustKeyForTest(t)
	early := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	late := early.Add(48 * time.Hour)
	a := ReadCopy(storedFile(t, priv, "FB-OLD", early))
	b := ReadCopy(storedFile(t, priv, "FB-NEW", late))
	none := ReadCopy(nil)
	forged := ReadCopy(append([]byte(nil), storedFile(t, ed25519.NewKeyFromSeed(make([]byte, 32)), "FB-FAKE", late)...))

	cases := []struct {
		what         string
		host, engine Copy
		removed      string
		want         SyncAction
	}{
		{"neither has one", none, none, "", SyncNone},
		{"only this machine: push, as `fox start` always has", a, none, "", SyncPush},
		{"only the engine: the console activated it, so keep a copy here", none, b, "", SyncPull},
		{"only the engine, and it is the one removed here: finish removing it", none, a, "FB-OLD", SyncRemove},
		{"…but a *different* one the engine has is a new activation, not the removed one", none, b, "FB-OLD", SyncPull},
		{"both the same", a, a, "", SyncNone},
		{"the engine's is newer: pull, never push the older over it", a, b, "", SyncPull},
		{"this machine's is newer: push", b, a, "", SyncPush},
		{"a forged copy in the engine never wins", a, forged, "", SyncPush},
		{"…and is not pulled when this machine has none", none, forged, "", SyncNone},
		{"a forged copy here never wins either", forged, b, "", SyncPull},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			if got := Reconcile(c.host, c.engine, c.removed); got != c.want {
				t.Errorf("Reconcile = %v, want %v", got, c.want)
			}
		})
	}
}

// A tie keeps this machine's, which is what `fox start` did before there was
// anything to reconcile — so nothing changes for anybody who never used the
// console.
func TestReconcileTieKeepsThisMachines(t *testing.T) {
	priv, _ := trustKeyForTest(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	h := ReadCopy(storedFile(t, priv, "FB-A", at))
	e := ReadCopy(storedFile(t, priv, "FB-B", at))
	if got := Reconcile(h, e, ""); got != SyncPush {
		t.Errorf("a tie decided %v, want push", got)
	}
}

// Pulled exactly, not re-saved: re-saving would stamp a new activation time,
// making this copy look newer than the one it came from, and the next reconcile
// would push it straight back — for ever.
func TestWriteRawKeepsTheActivationTime(t *testing.T) {
	priv, _ := trustKeyForTest(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	raw := storedFile(t, priv, "FB-NEW", at)
	if err := WriteRaw(raw); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Error("the pulled licence was rewritten rather than kept byte for byte")
	}
	// Having pulled it, the two sides agree and nothing more happens.
	if a := Reconcile(ReadCopy(got), ReadCopy(raw), ""); a != SyncNone {
		t.Errorf("after a pull the sides still disagree: %v", a)
	}
	// And a licence that does not verify is never installed this way.
	if err := WriteRaw([]byte(`{"license":{"id":"FB-X"}}`)); err == nil {
		t.Error("an unsigned licence was installed")
	}
}

func TestTheRemovalRecord(t *testing.T) {
	trustKeyForTest(t)
	if RemovedID() != "" {
		t.Fatal("a fresh install remembers a removal")
	}
	if err := MarkRemoved("FB-OLD"); err != nil {
		t.Fatal(err)
	}
	if got := RemovedID(); got != "FB-OLD" {
		t.Errorf("RemovedID = %q", got)
	}
	// Beside the licence, wherever that is.
	if filepath.Dir(removedPath()) != filepath.Dir(Path()) {
		t.Errorf("the removal record is at %s, not beside %s", removedPath(), Path())
	}
	if err := ClearRemoved(); err != nil {
		t.Fatal(err)
	}
	if RemovedID() != "" {
		t.Error("ClearRemoved left it behind")
	}
	// Clearing what is not there is fine: removal records are best-effort.
	if err := ClearRemoved(); err != nil {
		t.Errorf("clearing twice: %v", err)
	}
	// Nothing is recorded for a licence with no id.
	if err := MarkRemoved(""); err != nil || RemovedID() != "" {
		t.Error("an empty id was recorded")
	}
}

func TestShouldReinstall(t *testing.T) {
	now := time.Now()
	a := stamp{exists: true, size: 600, mod: now}
	b := stamp{exists: true, size: 601, mod: now}
	gone := stamp{}
	for _, c := range []struct {
		what      string
		prev, cur stamp
		since     time.Duration
		want      bool
	}{
		{"nothing changed and it is not time", a, a, time.Minute, false},
		{"the file was replaced", a, b, 0, true},
		{"the licence was removed", a, gone, 0, true},
		{"one arrived", gone, a, 0, true},
		{"nothing changed, but time may have: an expiry, a grace period ending", a, a, 11 * time.Minute, true},
	} {
		if got := shouldReinstall(c.prev, c.cur, c.since, 10*time.Minute); got != c.want {
			t.Errorf("%s: got %v, want %v", c.what, got, c.want)
		}
	}
}

// Only a change worth a log line is one: "expires in 9 days" becoming "in 8"
// every ten minutes would bury the line that matters.
func TestChangedIgnoresWording(t *testing.T) {
	x := Status{State: Warning, Reason: "expires in 9 days", License: License{ID: "FB-1"}}
	y := Status{State: Warning, Reason: "expires in 8 days", License: License{ID: "FB-1"}}
	if changed(x, y) {
		t.Error("the same state in different words counts as a change")
	}
	z := Status{State: Lapsed, License: License{ID: "FB-1"}}
	if !changed(x, z) {
		t.Error("a licence lapsing is not a change")
	}
}

// The property the console's Upgrade button depends on: a process that is
// already running honours a licence that arrives after it started, without
// being restarted. Before this, the Gateway and the Agent API refused paid
// features until somebody restarted them, while the console said "activated".
func TestARunningProcessHonoursALicenceThatArrivesLater(t *testing.T) {
	priv, _ := trustKeyForTest(t)
	edition.SetEntitlement(nil)
	t.Cleanup(func() { edition.SetEntitlement(nil) })
	Install() // nothing there yet: no entitlement
	if Installed().Present() {
		t.Fatal("a licence is installed before one was written")
	}

	seen := make(chan Status, 4)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	// The real loop, at a test's speed — and stopped when the test ends, or it
	// would go on to read whatever the licence path means after the override
	// is put back, which is the real one.
	go watch(Path(), stampOf(Path()), 20*time.Millisecond, time.Hour, func(st Status) { seen <- st }, stop)

	if err := os.WriteFile(Path(), storedFile(t, priv, "FB-LIVE", time.Now()), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case st := <-seen:
		if st.License.ID != "FB-LIVE" {
			t.Errorf("the watcher reported %q", st.License.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a running process never noticed the licence arriving")
	}
	if !Installed().Present() || Installed().License.ID != "FB-LIVE" {
		t.Errorf("Installed() = %+v after the watcher saw the file", Installed())
	}
	// And in a build that contains the paid code, the feature itself opens —
	// which is what the console's lock was waiting for. A Standard build stays
	// shut whatever the licence says, by design.
	if edition.Enterprise && !edition.Has("anchors") {
		t.Error("the licence arrived and the feature it covers is still refused")
	}
	if !edition.Enterprise && edition.Has("anchors") {
		t.Error("a Standard build opened a paid feature because a licence appeared")
	}
}
