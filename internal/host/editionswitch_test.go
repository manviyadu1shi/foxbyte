// SPDX-License-Identifier: AGPL-3.0-or-later

package host

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thefoxbyte/foxbyte/internal/update"
	"github.com/thefoxbyte/foxbyte/internal/version"
)

// isolateSwitch points the job's files at a temporary directory and starts
// nothing; it returns the editions a switch was started for.
func isolateSwitch(t *testing.T, live bool) *[]string {
	t.Helper()
	dir := t.TempDir()
	oldPath, oldLog, oldLive, oldStart, oldVer := editionSwitchPath, editionSwitchLog, editionSwitchLive, startJob, version.Version
	editionSwitchPath = func() string { return filepath.Join(dir, "edition-switch.json") }
	editionSwitchLog = func() string { return filepath.Join(dir, "edition-switch.log") }
	editionSwitchLive = func() bool { return live }
	var started []string
	startJob = func(ed string) error { started = append(started, ed); return nil }
	t.Cleanup(func() {
		editionSwitchPath, editionSwitchLog, editionSwitchLive, startJob, version.Version = oldPath, oldLog, oldLive, oldStart, oldVer
	})
	return &started
}

func TestStartEditionSwitch(t *testing.T) {
	other := otherEdition()
	started := isolateSwitch(t, false)

	version.Version = "0.1.0-dev"
	if err := StartEditionSwitch(other, "root@x.com"); err == nil {
		t.Fatal("a development build started a switch")
	}
	version.Version = "1.2.0"
	if err := StartEditionSwitch(update.ThisEdition(), "root@x.com"); err != ErrSameEdition {
		t.Fatalf("switching to this edition: %v", err)
	}
	if err := StartEditionSwitch("gold", "root@x.com"); err == nil {
		t.Fatal("an unknown edition was accepted")
	}
	if len(*started) != 0 {
		t.Fatalf("a refused switch started: %v", *started)
	}

	if err := StartEditionSwitch(other, "root@x.com"); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 1 || (*started)[0] != other {
		t.Fatalf("started %v", *started)
	}
	s, ok := ReadEditionSwitch()
	// The job is not "live" in this test, so a state still saying running reads
	// as a job that died — the case ReadEditionSwitch exists to catch.
	if !ok || s.Edition != other || s.By != "root@x.com" || s.State != "failed" {
		t.Fatalf("state %+v", s)
	}
}

func TestOnlyOneSwitchAtATime(t *testing.T) {
	isolateSwitch(t, true)
	version.Version = "1.2.0"
	if err := StartEditionSwitch(otherEdition(), "root@x.com"); err != ErrSwitchRunning {
		t.Fatalf("a second switch: %v", err)
	}
}

func TestReadEditionSwitchShowsTheLastLines(t *testing.T) {
	isolateSwitch(t, true)
	if err := writeEditionSwitch(EditionSwitch{State: "running", Edition: "enterprise"}); err != nil {
		t.Fatal(err)
	}
	var log string
	for i := 1; i <= 20; i++ {
		log += "line " + string(rune('a'+i)) + "\n"
	}
	if err := os.WriteFile(editionSwitchLog(), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	s, ok := ReadEditionSwitch()
	if !ok || s.State != "running" || len(s.Log) != editionSwitchLogLines || s.Log[len(s.Log)-1] != "line u" {
		t.Fatalf("got %+v", s)
	}
}

func TestNeedsLauncherSync(t *testing.T) {
	for _, c := range []struct {
		goos, cmd, engine, this string
		want                    bool
	}{
		{"darwin", "status", "enterprise", "standard", true},
		{"windows", "branch", "standard", "enterprise", true},
		{"darwin", "status", "standard", "standard", false},
		{"darwin", "status", "", "standard", false},          // engine not running, or too old to say
		{"linux", "status", "enterprise", "standard", false}, // fox is the engine there
		{"darwin", "upgrade", "enterprise", "standard", false},
		{"darwin", "update", "enterprise", "standard", false},
		{"darwin", "uninstall", "enterprise", "standard", false},
		{"darwin", "setup", "enterprise", "standard", false},
	} {
		if got := needsLauncherSync(c.goos, c.cmd, c.engine, c.this); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}
