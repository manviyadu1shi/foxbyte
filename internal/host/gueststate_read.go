//go:build darwin || windows

// SPDX-License-Identifier: AGPL-3.0-or-later

package host

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// guestStateOutput runs a read in the guest and returns what it printed.
//
// Stdout and stderr are kept apart, unlike guestStateShell, because here stdout
// is the content: a warning on stderr from the shell or the VM tooling must not
// end up inside somebody's licence.
func guestStateOutput(cmd *exec.Cmd) ([]byte, error) {
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.Bytes(), nil
	case errors.As(err, &ee) && fmt.Sprint(ee.ExitCode()) == noStateExit:
		return nil, ErrNoState
	default:
		return nil, fmt.Errorf("%w\n%s", err, strings.TrimSpace(errb.String()))
	}
}
