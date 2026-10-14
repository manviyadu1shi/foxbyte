// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/thefoxbyte/foxbyte/internal/brand"
	"github.com/thefoxbyte/foxbyte/internal/edition"
	"github.com/thefoxbyte/foxbyte/internal/host"
	"github.com/thefoxbyte/foxbyte/internal/license"
)

// `fox license` — activate, show, rebind, remove.
//
// It runs on the host and is never forwarded into the VM (localCommands), for
// the same reason the fingerprint is read there: recreating the VM is an
// ordinary repair step, and a licence that moved with it would be rebound by a
// routine fix.
func licenseCmd(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "activate":
		return licenseActivate(args[1:])
	case "show", "":
		return licenseShow()
	case "rebind":
		return licenseRebind()
	case "remove":
		return licenseRemove()
	default:
		return fmt.Errorf("unknown: %s license %s\n\n%s", brand.CLI, sub, licenseUsage())
	}
}

func licenseUsage() string {
	return fmt.Sprintf(`%[1]s license show                 what is installed, and this machine's fingerprint
%[1]s license activate <file|->   install a licence (- reads stdin)
%[1]s license rebind              move the installed licence to this machine
%[1]s license remove              uninstall it`, brand.CLI)
}

// activate reads a licence, checks it, and installs it.
//
// From a file or stdin, never from an argument: a licence is not a secret, but
// arguments end up in shell history and in the process list, and that is a
// habit worth not teaching — the same reason `fox license` takes a path here
// and the update tooling takes its token from the environment.
func licenseActivate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: %s license activate <file|->", brand.CLI)
	}
	var raw []byte
	var err error
	if args[0] == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(args[0])
	}
	if err != nil {
		return err
	}
	var l license.License
	if err := json.Unmarshal(raw, &l); err != nil {
		return fmt.Errorf("that file is not a licence: %w", err)
	}
	pub, err := license.PublicKey()
	if err != nil {
		return err
	}
	// Refuse a forgery outright. Everything else — expired, bound elsewhere —
	// installs with a warning, because those are states a real customer can be
	// in and the way out differs for each.
	if err := license.CheckSignature(l, pub); err != nil {
		return fmt.Errorf("%w\n\nNothing was installed", err)
	}
	fp := host.MachineID()
	// Bound to whatever it was issued for, not to this machine: activating a
	// licence meant for another one should say so, and `rebind` is the way to
	// move it. Binding silently on activation would make the warning
	// unreachable and the record meaningless.
	if err := license.Save(l, l.Fingerprint, 0); err != nil {
		return err
	}
	// A licence activated here supersedes any removal still waiting to reach
	// the engine; finishing that removal now would take this one away.
	_ = license.ClearRemoved()
	fmt.Printf("Activated %s for %s.\n", l.ID, l.Customer)
	giveEngineTheLicence()
	noteEnginePicksItUp()
	st := license.EvaluateBound(l, pub, l.Fingerprint, fp, time.Now())
	printLicenseState(st, fp)
	return nil
}

func licenseShow() error {
	// Asked before answering, so a licence activated in the console is the one
	// shown here — the console said "activated", and this saying "none" would
	// leave somebody believing one of them is broken.
	syncLicenceWithEngine(false)
	fp := host.MachineID()
	l, boundTo, rebinds, err := license.Load()
	if err == license.ErrNone {
		fmt.Printf("%s\n\n", noLicenceLine())
		printFingerprint(fp)
		return nil
	}
	if err != nil {
		return err
	}
	pub, perr := license.PublicKey()
	if perr != nil {
		return perr
	}
	fmt.Printf("%s — %s\n", l.ID, l.Customer)
	if len(l.Features) > 0 {
		fmt.Printf("  unlocks    %s\n", strings.Join(l.Features, ", "))
	}
	fmt.Printf("  issued     %s\n", l.IssuedAt.UTC().Format("2 January 2006"))
	if !l.NotAfter.IsZero() {
		fmt.Printf("  runs to    %s\n", l.NotAfter.UTC().Format("2 January 2006"))
	}
	switch {
	case l.Fingerprint == "":
		fmt.Printf("  machine    any — this licence is not tied to one\n")
	default:
		fmt.Printf("  issued for %s\n", short(l.Fingerprint))
		fmt.Printf("  bound to   %s", short(boundTo))
		if rebinds > 0 {
			fmt.Printf(" (moved %d time%s)", rebinds, plural(rebinds))
		}
		fmt.Println()
	}
	fmt.Println()
	printLicenseState(license.EvaluateBound(l, pub, boundTo, fp, time.Now()), fp)
	return nil
}

// rebind moves an installed licence to this machine. It is one command rather
// than reinstalling because the count is kept across it, which is what makes
// the record worth anything.
func licenseRebind() error {
	l, _, rebinds, err := license.Load()
	if err == license.ErrNone {
		return fmt.Errorf("there is no licence to move — install one with `%s license activate <file>`", brand.CLI)
	}
	if err != nil {
		return err
	}
	fp := host.MachineID()
	if fp == "" {
		return fmt.Errorf("this machine's identifier could not be read, so there is nothing to bind to")
	}
	if err := license.Save(l, fp, rebinds+1); err != nil {
		return err
	}
	giveEngineTheLicence()
	fmt.Printf("Moved %s to this machine (%s).\n", l.ID, short(fp))
	noteEnginePicksItUp()
	fmt.Printf("That is move %d. It is recorded here and in the security log; the\n"+
		"number of machines on an account is counted where the licence was issued.\n", rebinds+1)
	return nil
}

func licenseRemove() error {
	l, _, _, err := license.Load()
	if err == license.ErrNone {
		fmt.Println("No licence was installed.")
		return nil
	}
	// Recorded before anything is removed, so a removal the engine cannot hear
	// now — the VM stopped — is finished by the next `fox start` instead of
	// being undone by it. Reconciling copies would otherwise read "the engine
	// has a licence this machine lacks" as the engine being ahead.
	if err == nil {
		if merr := license.MarkRemoved(l.ID); merr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not record the removal: %v\n", merr)
		}
	}
	if err := license.Remove(); err != nil {
		return err
	}
	// The engine's copy goes too, or the entitlement would outlive the licence
	// on exactly the platforms where it is hardest to notice.
	rerr := host.RemoveState(license.FileName)
	reportEngineCopy(rerr, "take the licence away from")
	if rerr == nil {
		// Delivered, so there is nothing left to finish.
		_ = license.ClearRemoved()
	}
	fmt.Printf("Removed. %s No data was touched.\n", noLicenceLine())
	noteEnginePicksItUp()
	return nil
}

// giveEngineTheLicence copies the installed licence to wherever the engine
// reads its state.
//
// On Linux that is this directory and the copy is a no-op. On macOS and Windows
// the engine is in the VM with a home of its own, so without this a licence
// would be installed, shown as active by `fox license show`, and still unlock
// nothing — the worst of the three possible outcomes, because it looks like it
// worked.
func giveEngineTheLicence() {
	raw, err := os.ReadFile(license.Path())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: the licence was saved but could not be read back: %v\n", err)
		return
	}
	reportEngineCopy(host.PushState(license.FileName, raw), "give the licence to")
}

// reportEngineCopy says what happened to the engine's copy without letting it
// fail the command. The licence is already installed on this machine at this
// point: a stopped VM is a thing to mention, not a reason to unwind work that
// succeeded.
func reportEngineCopy(err error, what string) {
	switch {
	case err == nil:
	case errors.Is(err, host.ErrNoEngine):
		fmt.Printf("The engine is not running yet, so it does not have this change.\n"+
			"`%s start` passes it on.\n", brand.CLI)
	default:
		fmt.Fprintf(os.Stderr, "warning: could not %s the engine: %v\n", what, err)
	}
}

// noteEnginePicksItUp says when a change reaches an engine that is already
// running: within seconds, by itself. Every long-running engine process now
// watches the licence file (license.Watch), so the restart this used to ask for
// is no longer needed — and asking for one that is not needed teaches people
// to restart a database for no reason.
func noteEnginePicksItUp() {
	if !host.EngineRunning() {
		return
	}
	fmt.Println("\nThe running engine picks this up within a few seconds; nothing needs restarting.")
}

// printLicenseState says what is true and what to do about it, in that order.
// One place, so the CLI, `fox check` and the console cannot drift into saying
// three different things about the same licence.
func printLicenseState(st license.Status, fp string) {
	switch st.State {
	case license.Active:
		fmt.Println("Status: active.")
	default:
		fmt.Printf("Status: %s — %s.\n", st.State, st.Reason)
		if st.Action != "" {
			fmt.Printf("        %s\n", st.Action)
		}
	}
	if st.State != license.Active {
		fmt.Println()
		printFingerprint(fp)
	}
}

// The fingerprint is the one piece of output designed to be copied: it is what
// someone pastes into a renewal or sends to support, so it goes on a line of
// its own with nothing to trim off.
func printFingerprint(fp string) {
	if fp == "" {
		fmt.Println("This machine's identifier could not be read. That does not stop anything;")
		fmt.Println("a licence bound to another machine still works, with a warning.")
		return
	}
	fmt.Println("This machine's fingerprint, for a new or renewed licence:")
	fmt.Println()
	fmt.Println("  " + fp)
}

func short(fp string) string {
	if len(fp) > 12 {
		return fp[:12] + "…"
	}
	return fp
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// syncLicenceWithEngine reconciles this machine's licence with the engine's
// copy, and says what it did.
//
// On Linux the two are one file and this does nothing. On macOS and Windows the
// console can activate a licence the host never hears about, and a removal made
// while the VM was down never reached the engine; license.Reconcile decides
// which copy is right and this carries it out.
//
// startEngine is for `fox start`: the stack is about to come up anyway, so the
// VM may be started to do this, and the engine then reads the reconciled
// licence the moment it starts. Everywhere else a stopped VM is left stopped.
//
// Best-effort and quiet unless something changed. A licence is never a reason a
// command fails, and a database is never a reason to wait on one.
func syncLicenceWithEngine(startEngine bool) {
	pull := host.PullState
	if startEngine {
		pull = host.PullStateStartingEngine
	}
	engineRaw, err := pull(license.FileName)
	switch {
	case errors.Is(err, host.ErrNoEngine):
		return // nothing to reconcile with yet
	case errors.Is(err, host.ErrNoState):
		engineRaw = nil
	case err != nil:
		fmt.Fprintf(os.Stderr, "warning: could not read the engine's licence: %v\n", err)
		return
	}
	hostRaw, _ := os.ReadFile(license.Path())
	hostCopy, engineCopy := license.ReadCopy(hostRaw), license.ReadCopy(engineRaw)
	switch license.Reconcile(hostCopy, engineCopy, license.RemovedID()) {
	case license.SyncPush:
		_ = host.PushState(license.FileName, hostRaw)
	case license.SyncPull:
		if err := license.WriteRaw(engineRaw); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not keep the engine's licence here: %v\n", err)
			return
		}
		_ = license.ClearRemoved()
		fmt.Printf("Kept a copy of licence %s here — it was activated in the console, so this machine\n"+
			"had not heard of it, and rebuilding the VM would have lost it.\n", engineCopy.ID())
	case license.SyncRemove:
		if err := host.RemoveState(license.FileName); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not finish removing the licence from the engine: %v\n", err)
			return
		}
		_ = license.ClearRemoved()
		fmt.Printf("Finished removing licence %s from the engine — it was removed here while the\n"+
			"engine was stopped.\n", engineCopy.ID())
	}
}

// noLicenceLine says what having no licence means, in the terms the console's
// badge uses. It used to name the build — "so this is the enterprise edition"
// — which on an Enterprise build with no licence is true of the binary and
// wrong about everything a person can do with it: the paid features are locked,
// and the console now says Standard for exactly that reason.
func noLicenceLine() string {
	if edition.Enterprise {
		return "No licence is installed, so the paid features are locked."
	}
	return "No licence is installed, and this standard edition needs none."
}
