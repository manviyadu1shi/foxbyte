// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// How current the backups are, as `fox check` and `fox status` report it.
//
// The case this exists for: an install that is not running. Listing base
// backups means reaching an object store, and the default store is a container
// of this install's own — so with the install down there is nothing to answer,
// and the old code waited to find that out. On a machine whose pool had been
// destroyed it waited for ever (the listing had no deadline), and `fox check`
// printed nothing at all.
func TestBackupHealth(t *testing.T) {
	local := Target{Kind: "local"}
	remote := Target{Kind: "s3", Bucket: "acme-fox-backups", Endpoint: "https://s3.eu-west-1.amazonaws.com"}
	up := func() bool { return true }
	down := func() bool { return false }
	none := func() (time.Time, error) {
		t.Helper()
		t.Error("the object store was asked although it is not running")
		return time.Time{}, nil
	}

	t.Run("the local store being down is reported, not waited for", func(t *testing.T) {
		h := backupHealth(local, down, none)
		if !h.Unknown {
			t.Fatal("want Unknown: nothing was established about the backups")
		}
		// And it says which thing is not there, because the way out differs:
		// `fox start` for this, credentials or a network for a remote bucket.
		if !strings.Contains(h.Err, "object store is not running") {
			t.Errorf("Err %q does not say the local store is down", h.Err)
		}
		// Not "there are no backups": the bucket may hold a hundred.
		if h.Newest != "" {
			t.Errorf("Newest %q, want empty", h.Newest)
		}
	})

	t.Run("a remote bucket is still asked with the install down", func(t *testing.T) {
		// The whole point of moving backups off this machine is that they
		// survive it, so a stopped stack is no reason to skip the listing.
		asked := 0
		newest := func() (time.Time, error) {
			asked++
			return time.Now().Add(-3 * time.Hour), nil
		}
		h := backupHealth(remote, down, newest)
		if asked != 1 {
			t.Fatalf("the bucket was asked %d time(s), want 1", asked)
		}
		if h.Unknown || h.Stale {
			t.Errorf("want a current backup, got unknown=%v stale=%v (%s)", h.Unknown, h.Stale, h.Err)
		}
	})

	t.Run("a listing that fails is unknown, and stays stale for older readers", func(t *testing.T) {
		h := backupHealth(remote, up, func() (time.Time, error) {
			return time.Time{}, errors.New("listing base backups: dial tcp: i/o timeout\nsecond line")
		})
		if !h.Unknown {
			t.Fatal("want Unknown")
		}
		if !strings.Contains(h.Err, "i/o timeout") {
			t.Errorf("Err %q does not carry the reason", h.Err)
		}
		if strings.Contains(h.Err, "second line") {
			t.Errorf("Err %q should be one line", h.Err)
		}
		// Every version before Unknown existed called a failed listing stale.
		// A reader that does not know the new field keeps the old judgement.
		if !h.Stale {
			t.Error("want Stale as well, so an older reader is not told all is well")
		}
	})

	t.Run("an empty bucket is a fresh install, not an unanswerable question", func(t *testing.T) {
		// The two must not collapse into one line: this one really does mean
		// "take a backup".
		h := backupHealth(local, up, func() (time.Time, error) { return time.Time{}, nil })
		if h.Unknown {
			t.Errorf("an empty bucket was reported as unknown (%s)", h.Err)
		}
		if !h.Stale || h.Newest != "" {
			t.Errorf("want stale with no newest, got stale=%v newest=%q", h.Stale, h.Newest)
		}
	})
}

// The throwaway container the listing runs in must not look like a branch.
// Anything named `pg-…` is enumerated as one — by `fox list`, by the console,
// and by the change-feed sweep — and a listing lives for a few seconds.
func TestBackupListContainerIsNotABranch(t *testing.T) {
	first := backupListContainer()
	if strings.HasPrefix(first, containerPrefix) {
		t.Errorf("%q is under the branch prefix %q", first, containerPrefix)
	}
	if !strings.Contains(first, "backup-list") {
		t.Errorf("%q does not say what it is", first)
	}
	// Two listings at once must not collide on the name.
	if second := backupListContainer(); second == first {
		t.Errorf("two listings got the same name %q", first)
	}
}
