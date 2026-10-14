// SPDX-License-Identifier: AGPL-3.0-or-later

package license

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Two copies of one licence, and which of them is right.
//
// On macOS and Windows a licence exists twice: on the machine a person sits at,
// where `fox license` runs because that is the machine it is bound to, and in
// the VM, where the engine that honours it runs. For as long as the only way in
// was `fox license activate` on the host, the host's copy was the original and
// the VM's a copy pushed from it.
//
// The console broke that. Activating there writes the VM's copy, and nothing on
// the host hears of it — so the host went on saying there was no licence, a
// rebuilt VM lost it, and worst, the next `fox start` pushed the host's *older*
// copy over the newer one, undoing an upgrade without a word. So the two are now
// reconciled rather than one assumed to be the original: whichever was
// activated most recently wins.
//
// And one more case, which predates the console: `fox license remove` with the
// VM stopped never reached the engine, because `fox start` only ever pushed a
// licence the host *had*. Pulling the engine's copy back would make that worse
// — it would resurrect the removed licence on the host — so a removal is
// recorded, and a copy the engine still holds of exactly that licence is
// finished off instead of brought back.

// SyncAction is what reconciling decided.
type SyncAction int

const (
	SyncNone   SyncAction = iota // both agree, or neither has one
	SyncPush                     // this machine's copy goes to the engine
	SyncPull                     // the engine's copy comes to this machine
	SyncRemove                   // the engine's copy goes: it is one this machine removed
)

func (a SyncAction) String() string {
	switch a {
	case SyncPush:
		return "push"
	case SyncPull:
		return "pull"
	case SyncRemove:
		return "remove"
	}
	return "none"
}

// Copy is one side's licence file, read and checked.
type Copy struct {
	Raw []byte
	// OK is whether there is a licence here that may be acted on: present,
	// readable, and signed by the key this build trusts. A copy that is not
	// OK never wins — a forged or garbled file on one side must not replace a
	// good one on the other.
	OK     bool
	stored stored
}

// ReadCopy reads a licence file's bytes into a Copy. Absent or empty is a Copy
// that is not OK, which reconciles as "this side has none".
func ReadCopy(raw []byte) Copy {
	c := Copy{Raw: raw}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return c
	}
	if err := json.Unmarshal(raw, &c.stored); err != nil || c.stored.License.ID == "" {
		return c
	}
	pub, err := PublicKey()
	if err != nil {
		return c
	}
	c.OK = CheckSignature(c.stored.License, pub) == nil
	return c
}

// ID is the licence's id, or "" when there is none.
func (c Copy) ID() string {
	if !c.OK {
		return ""
	}
	return c.stored.License.ID
}

// Reconcile decides which copy both sides should hold. Pure, so every
// combination is read in a test rather than reproduced across a VM boundary.
//
// removedID is the licence this machine last removed, if any (MarkRemoved).
func Reconcile(host, engine Copy, removedID string) SyncAction {
	switch {
	case !host.OK && !engine.OK:
		return SyncNone
	case host.OK && !engine.OK:
		return SyncPush
	case !host.OK && engine.OK:
		// The engine has one and this machine does not. Either this machine
		// removed it while the engine could not hear — finish the removal —
		// or the engine was given it from the console, and this machine is
		// the one that is behind.
		if removedID != "" && engine.ID() == removedID {
			return SyncRemove
		}
		return SyncPull
	}
	if string(host.Raw) == string(engine.Raw) {
		return SyncNone
	}
	// Both have one and they differ. The most recent activation wins: that
	// is the last thing somebody did on purpose. A tie keeps this machine's,
	// which is what `fox start` has always done.
	if engine.stored.ActivatedAt.After(host.stored.ActivatedAt) {
		return SyncPull
	}
	return SyncPush
}

// RemovedFileName records which licence this machine removed. It holds an id
// and nothing else: the licence itself is gone, which was the point.
const RemovedFileName = "license.removed"

// removedPath sits beside the licence it records the removal of, wherever that
// is — which is also what lets it be tested without the real state directory.
func removedPath() string { return filepath.Join(filepath.Dir(Path()), RemovedFileName) }

// MarkRemoved records that the licence with this id was removed here.
func MarkRemoved(id string) error {
	if id == "" {
		return nil
	}
	p := removedPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(id+"\n"), 0o600)
}

// RemovedID is the id MarkRemoved last recorded, or "".
func RemovedID() string {
	b, err := os.ReadFile(removedPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ClearRemoved forgets a removal, once it has reached the engine or a licence
// has been activated since.
func ClearRemoved() error {
	if err := os.Remove(removedPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// WriteRaw installs a licence file exactly as another side holds it.
//
// Exactly, rather than re-saved: its activation time and binding are part of
// what was decided over there, and re-saving would stamp a new activation time
// on it and make it look newer than the copy it came from — which the next
// reconcile would then push back, for ever.
func WriteRaw(raw []byte) error {
	c := ReadCopy(raw)
	if !c.OK {
		return errors.New("refusing to install a licence that does not verify")
	}
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
