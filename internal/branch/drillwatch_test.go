// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"testing"
	"time"
)

// The watcher records its promotion after the copy is already serving, so the
// drill waits for the record instead of reading the log once.
func TestWaitForWatcher(t *testing.T) {
	calls := 0
	late := func() bool { calls++; return calls >= 3 }
	if !waitForWatcher(late, time.Second, time.Millisecond) {
		t.Error("a record that arrives late was not waited for")
	}
	start := time.Now()
	if waitForWatcher(func() bool { return false }, 20*time.Millisecond, time.Millisecond) {
		t.Error("no record, yet credited to the watcher")
	}
	if time.Since(start) > time.Second {
		t.Error("the wait was not bounded")
	}
	// Found at once: no waiting at all.
	start = time.Now()
	if !waitForWatcher(func() bool { return true }, time.Hour, time.Hour) || time.Since(start) > 100*time.Millisecond {
		t.Error("a record already there was waited for")
	}
}
