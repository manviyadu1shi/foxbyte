// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// What `mc retention info --default --json` says, as MinIO says it (recorded
// against the test VM's MinIO, 16 Oct 2026).
func TestParseLockInfo(t *testing.T) {
	cases := []struct {
		name, out string
		want      LockInfo
		wantErr   bool
	}{
		{"a bucket without Object Lock",
			`{"status":"error","error":{"message":"Remote bucket ` + "`%s`" + ` does not support locking","cause":{"message":"","error":{}},"type":"fatal"}}`,
			LockInfo{}, false},
		{"Object Lock with no default rule",
			`{"op":"info","enabled":"Enabled","mode":"","validity":"0","status":"success"}`,
			LockInfo{Enabled: true}, false},
		{"a GOVERNANCE default",
			`{"op":"info","enabled":"Enabled","mode":"GOVERNANCE","validity":"1DAYS","status":"success"}`,
			LockInfo{Enabled: true, DefaultMode: "GOVERNANCE", DefaultValidity: "1DAYS"}, false},
		{"noise before the answer",
			"some warning\n" + `{"op":"info","enabled":"Enabled","mode":"COMPLIANCE","validity":"30DAYS","status":"success"}`,
			LockInfo{Enabled: true, DefaultMode: "COMPLIANCE", DefaultValidity: "30DAYS"}, false},
		{"another error is an error", `{"status":"error","error":{"message":"Access Denied."}}`, LockInfo{}, true},
		{"not JSON", "mc: <ERROR> something", LockInfo{}, true},
	}
	for _, c := range cases {
		got, err := parseLockInfo(c.out)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%s: got %+v, %v", c.name, got, err)
		}
	}
}

// FoxByte cannot see who owns an account, but it can see the same key or the
// same bucket as the backups, which is not independence by any reading.
func TestSameControl(t *testing.T) {
	backup := Target{Kind: "s3", Endpoint: "https://s3.example", Bucket: "backups", AccessKey: "AK1"}
	for _, c := range []struct {
		anchor Target
		same   bool
	}{
		{Target{Endpoint: "https://s3.example", Bucket: "anchors", AccessKey: "AK1"}, true},
		{Target{Endpoint: "https://s3.example", Bucket: "backups", AccessKey: "AK2"}, true},
		{Target{Endpoint: "https://s3.example", Bucket: "anchors", AccessKey: "AK2"}, false},
	} {
		if got := sameControl(c.anchor, backup) != ""; got != c.same {
			t.Errorf("%+v: same control = %v", c.anchor, got)
		}
	}
	if sameControl(Target{AccessKey: "x"}, Target{Kind: "local"}) != "" {
		t.Error("a local backup target is not a remote account to share")
	}
}

// Every copy to the anchor target is locked in COMPLIANCE mode: GOVERNANCE can
// be bypassed by anyone allowed to.
func TestRetentionFlags(t *testing.T) {
	if got := retentionFlags(365); got != "--retention-mode compliance --retention-duration 365d" {
		t.Errorf("retentionFlags = %q", got)
	}
}

// fox check reads the markers, not the network.
func TestCountAnchorCopies(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FOX_ANCHOR_DIR", dir)
	main := filepath.Join(dir, "main")
	if err := os.MkdirAll(main, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"01.json", "02.json", "03.json", "01.json" + lockedSuffix, "02.json" + mirroredSuffix} {
		if err := os.WriteFile(filepath.Join(main, f), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(filepath.Join(main, "02.json"), old, old)
	c := CountAnchorCopies(lockedSuffix)
	if c.Total != 3 || c.Waiting != 2 || c.OldestWaiting < 47*time.Hour {
		t.Errorf("locked copies: %+v", c)
	}
	if m := CountAnchorCopies(mirroredSuffix); m.Waiting != 2 {
		t.Errorf("the two destinations are counted separately: %+v", m)
	}
}
