// SPDX-License-Identifier: AGPL-3.0-or-later

package host

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thefoxbyte/foxbyte/internal/daemon"
	"github.com/thefoxbyte/foxbyte/internal/update"
	"github.com/thefoxbyte/foxbyte/internal/version"
)

// Switching edition from the console.
//
// The console's Install button cannot replace fox on a Mac or a Windows PC: the
// control plane serving it runs inside the VM. What it can replace is the
// engine, because inside the VM — and on Linux, where there is no VM — `fox
// upgrade` *is* the whole switch: the engine is the program being run. So the
// control plane starts that, detached, as the job below, and reports on it.
// fox on the computer itself catches up on its next command
// (SyncLauncherEdition).
//
// The job outlives the control plane that started it, since the switch stops
// the servers and starts them again on the other edition. What it did is
// therefore kept in a file beside the engine's other state, which the control
// plane that comes back reads.

// editionSwitchJob is the background job's name (daemon: pid file and log).
const editionSwitchJob = "edition-switch"

// EditionSwitch is the state of the last switch started from the console.
type EditionSwitch struct {
	State      string   `json:"state"` // "running", "done" or "failed"
	Edition    string   `json:"edition"`
	By         string   `json:"by,omitempty"`
	StartedAt  string   `json:"started_at"`
	FinishedAt string   `json:"finished_at,omitempty"`
	Error      string   `json:"error,omitempty"`
	Log        []string `json:"log,omitempty"` // the job's last lines; read with the state, never stored in it
}

// ErrSameEdition and ErrSwitchRunning are refusals the console shows as they are.
var (
	ErrSameEdition   = errors.New("this is already that edition")
	ErrSwitchRunning = errors.New("a switch is already running")
)

// Where the job's state, output and process live: variables so a test can keep
// them out of the real ~/.fox (whose path is fixed once per process).
var (
	editionSwitchPath = func() string { return filepath.Join(cacheDir(), editionSwitchJob+".json") }
	editionSwitchLog  = func() string { return daemon.LogPath(editionSwitchJob) }
	editionSwitchLive = func() bool { return daemon.Alive(editionSwitchJob) }
)

func writeEditionSwitch(s EditionSwitch) error {
	s.Log = nil
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := editionSwitchPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, editionSwitchPath())
}

// editionSwitchLogLines is how much of the job's output the console is shown.
const editionSwitchLogLines = 12

// ReadEditionSwitch returns the last switch, with the tail of its output. ok is
// false when none was ever started here. A switch still marked running whose
// process is gone is reported as failed, rather than as running for ever.
func ReadEditionSwitch() (s EditionSwitch, ok bool) {
	b, err := os.ReadFile(editionSwitchPath())
	if err != nil || json.Unmarshal(b, &s) != nil {
		return EditionSwitch{}, false
	}
	if s.State == "running" && !editionSwitchLive() {
		s.State = "failed"
		s.Error = "the switch stopped before it finished; the log below says how far it got"
	}
	s.Log = tailLines(editionSwitchLog(), editionSwitchLogLines)
	return s, true
}

func tailLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimRight(sc.Text(), " \r"); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// startJob launches the background job; a variable so tests start nothing.
var startJob = func(edition string) error {
	return daemon.Start(editionSwitchJob, []string{"_edition-switch", "--edition", edition})
}

// StartEditionSwitch begins switching this engine to edition in the
// background. by is the account that asked, for the record. The caller has
// already checked that it is an admin and that the licence was accepted.
func StartEditionSwitch(edition, by string) error {
	if edition != update.EditionEnterprise && edition != update.EditionStandard {
		return fmt.Errorf("unknown edition %q (expected enterprise or standard)", edition)
	}
	if edition == update.ThisEdition() {
		return ErrSameEdition
	}
	if v, err := update.ParseVersion(version.Version); err != nil || v.IsDev() {
		return fmt.Errorf("this is a development build (%s); switching installs a published release, so install one first", version.Version)
	}
	if editionSwitchLive() {
		return ErrSwitchRunning
	}
	_ = os.Remove(editionSwitchLog()) // this switch's output, not the last one's
	if err := writeEditionSwitch(EditionSwitch{
		State: "running", Edition: edition, By: by, StartedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}
	return startJob(edition)
}

// RunEditionSwitch is the job itself (`fox _edition-switch`): `fox upgrade`,
// with the licence already accepted in the console, recording how it ended.
func RunEditionSwitch(edition string) error {
	s, _ := ReadEditionSwitch()
	s.Edition, s.State, s.Error = edition, "running", ""
	_ = writeEditionSwitch(s)
	err := Update(UpdateOptions{Edition: edition, AcceptLicense: true, Yes: true})
	s.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		s.State, s.Error = "failed", err.Error()
	} else {
		s.State = "done"
	}
	_ = writeEditionSwitch(s)
	return err
}
