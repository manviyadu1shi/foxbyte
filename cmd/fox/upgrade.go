// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/thefoxbyte/foxbyte/internal/brand"
	"github.com/thefoxbyte/foxbyte/internal/host"
	"github.com/thefoxbyte/foxbyte/internal/update"
)

const upgradeUsage = `usage: fox upgrade --edition enterprise|standard [--accept-license] [--yes]

Switches this install to the other edition of the version it already runs:
FoxByte Enterprise, or back to Standard. The signed release is downloaded and
checked exactly as ` + "`fox update`" + ` does, the engine and the servers are replaced,
and fox on this computer too. Databases, branches, accounts, keys, the
Blackbox and settings are not touched.

FoxByte Enterprise is licensed under the FoxByte Enterprise License, not the
AGPL. The licence is shown, and has to be accepted, before anything is
downloaded. Its paid features need a licence key; activate one afterwards in
the console (Upgrade) or with ` + "`fox license activate <file>`" + `.

  --edition E        enterprise, or standard to switch back
  --accept-license   Accept the FoxByte Enterprise License without being asked
  --yes, -y          Switch without asking about the restart
`

// upgradeCmd runs `fox upgrade`. It lives in the free edition on purpose: all
// it knows about Enterprise is the name of the file to fetch and where its
// licence is published. The Enterprise code itself only ever arrives in that
// separately licensed download, so this build stays free of it.
func upgradeCmd(args []string) {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(upgradeUsage)
			return
		}
	}
	opts, err := parseUpgradeArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		fmt.Fprint(os.Stderr, upgradeUsage)
		os.Exit(2)
	}
	if opts.Edition == update.ThisEdition() {
		fmt.Printf("This is already the %s edition.\n", strings.ToUpper(opts.Edition[:1])+opts.Edition[1:])
		if opts.Edition == update.EditionEnterprise {
			fmt.Println("To unlock the paid features, activate your licence: in the console choose Upgrade, or run `fox license activate <file>`.")
		}
		return
	}
	must(host.Update(opts))
}

func parseUpgradeArgs(args []string) (host.UpdateOptions, error) {
	var o host.UpdateOptions
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--yes" || a == "-y":
			o.Yes = true
		case a == "--accept-license" || a == "--accept-licence":
			o.AcceptLicense = true
		case a == "--edition":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return o, fmt.Errorf("--edition needs enterprise or standard")
			}
			o.Edition = args[i+1]
			i++
		case strings.HasPrefix(a, "--edition="):
			o.Edition = strings.TrimPrefix(a, "--edition=")
		default:
			return o, fmt.Errorf("unknown argument %q", a)
		}
	}
	switch strings.ToLower(o.Edition) {
	case update.EditionEnterprise, update.EditionStandard:
		o.Edition = strings.ToLower(o.Edition)
	case "":
		return o, fmt.Errorf("say which edition: --edition enterprise (or standard to switch back)")
	default:
		return o, fmt.Errorf("unknown edition %q (expected enterprise or standard)", o.Edition)
	}
	return o, nil
}

// editionSwitchCmd is the job the console's Install button starts (`fox
// _edition-switch --edition E`), detached, inside the engine. Not meant to be
// typed: `fox upgrade` is the command for people.
func editionSwitchCmd(args []string) {
	o, err := parseUpgradeArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if err := host.RunEditionSwitch(o.Edition); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// rerun runs the same command again on exe, the fox that just replaced this
// one, and exits with its status. A replaced program carries on as the old one
// otherwise, and would answer as the edition the person has just left.
func rerun(exe string) {
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Once, not again: a replacement that somehow left the old edition behind
	// must not send the new fox round the same loop.
	cmd.Env = append(os.Environ(), brand.EnvName(host.EnvNoEditionSync)+"=1")
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	os.Exit(0)
}
