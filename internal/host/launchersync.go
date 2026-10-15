// SPDX-License-Identifier: AGPL-3.0-or-later

package host

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/thefoxbyte/foxbyte/internal/brand"
	"github.com/thefoxbyte/foxbyte/internal/update"
	"github.com/thefoxbyte/foxbyte/internal/version"
)

// fox on this computer catching up with an engine switched from the console.
//
// On macOS and Windows there are two programs: fox here, and the engine in the
// VM. The console's Install button can switch only the engine
// (editionswitch.go), and a launcher of one edition in front of an engine of
// the other offers commands that are not there, or hides ones that are. So
// before a command runs, fox asks the engine which edition it is — one request
// to the local control plane, answered in milliseconds and skipped quickly when
// nothing is listening — and on a mismatch fetches the matching build of the
// same version through the same verified path as `fox update`, swaps itself,
// and runs the command again on the new binary.

// EnvNoEditionSync turns the check off (FOX_NO_EDITION_SYNC=1).
const EnvNoEditionSync = "NO_EDITION_SYNC"

// noSync are the commands that must not trigger it: the ones that replace fox
// themselves, take the engine away, or are fox's own internal steps; and the
// ones that describe this binary, whose answer must be about the fox that was
// run, not the one it would become. `fox version` replacing itself first
// printed a download's progress before a version line, and on a development
// build a failure, where a script expected one line.
var noSync = map[string]bool{
	"update": true, "upgrade": true, "uninstall": true, "setup": true,
	"_update-guest": true, "_edition-switch": true,
	"version": true, "-v": true, "--version": true,
	"help": true, "-h": true, "--help": true,
}

// needsLauncherSync is the decision, apart from the asking: on a computer with
// a separate launcher, for an ordinary command, when the engine said which
// edition it is and that is not this one.
func needsLauncherSync(goos, cmd, engineEd, thisEd string) bool {
	if goos != "darwin" && goos != "windows" {
		return false // on Linux fox is the engine
	}
	if noSync[cmd] || engineEd == "" {
		return false
	}
	return engineEd != thisEd
}

// engineEdition asks the local control plane which edition it runs, or ""
// when it cannot tell: nothing listening, or an engine from before it said.
var engineEdition = func() string {
	c := &http.Client{
		Timeout: 600 * time.Millisecond,
		// The local control plane's certificate is self-signed; this reads one
		// public word from it and sends nothing.
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
	}
	resp, err := c.Get("https://localhost:8080/api/edition")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var v struct {
		Edition string `json:"edition"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<10)).Decode(&v) != nil {
		return ""
	}
	switch v.Edition {
	case update.EditionStandard, update.EditionEnterprise:
		return v.Edition
	}
	return ""
}

// SyncLauncherEdition brings fox on this computer to the engine's edition when
// the two differ. It returns the path of the new fox when it replaced itself,
// so the caller can run the command again on it, or "" when nothing changed.
// A failure is reported, with the command that finishes the job, and never
// stops the command the person actually typed.
func SyncLauncherEdition(cmd string) string {
	if truthyEnvHost(brand.EnvName(EnvNoEditionSync)) {
		return ""
	}
	ed := engineEdition()
	if !needsLauncherSync(runtime.GOOS, cmd, ed, update.ThisEdition()) {
		return ""
	}
	name := editionName(ed)
	fmt.Fprintf(os.Stderr, "The engine was switched to the %s edition; updating %s on this computer to match…\n", name, brand.CLI)
	exe, err := replaceLauncher(ed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  could not: %v\n  Run `%s upgrade --edition %s` to finish.\n\n", err, brand.CLI, ed)
		return ""
	}
	fmt.Fprintf(os.Stderr, "  done — %s on this computer is now the %s edition.\n\n", brand.CLI, name)
	return exe
}

func truthyEnvHost(name string) bool {
	switch os.Getenv(name) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// replaceLauncher installs this computer's fox in edition ed, at the version
// already installed: downloaded and verified exactly as an update's files are.
func replaceLauncher(ed string) (string, error) {
	cur, err := update.ParseVersion(version.Version)
	if err != nil || cur.IsDev() {
		return "", fmt.Errorf("this is a development build (%s)", version.Version)
	}
	hooks := platformUpdateHooks(newEngineHost())
	if hooks.replaceHost == nil {
		return "", fmt.Errorf("nothing to replace on this platform")
	}
	// Built here rather than asked of the VM: only this computer's file is
	// needed, and asking the VM its CPU would cost the time this is avoiding.
	t := update.Target{GOOS: runtime.GOOS, HostArch: runtime.GOARCH, Edition: ed}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := update.NewClient(os.Getenv, updateCheckCache())
	o, err := client.Resolve(ctx, cur, t, cur.String())
	if err != nil {
		return "", err
	}
	if o == nil {
		return "", fmt.Errorf("no published %s build of %s", editionName(ed), cur)
	}
	name := update.HostAsset(t)
	a, ok := o.Release.Asset(name)
	if !ok {
		return "", fmt.Errorf("release %s has no %s", o.Release.Tag, name)
	}
	path, err := client.Download(ctx, a, o.Sums[name], filepath.Join(cacheDir(), "updates", o.Release.Tag))
	if err != nil {
		return "", err
	}
	if hooks.preflight != nil {
		if err := hooks.preflight(); err != nil {
			return "", err
		}
	}
	if err := hooks.replaceHost(map[string]string{name: path}, t); err != nil {
		return "", err
	}
	return hostExecutable()
}
