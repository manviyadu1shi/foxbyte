// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/thefoxbyte/foxbyte/internal/brand"
)

// How a person connects to a branch.
//
// Two different things were both called "connecting", and neither was easy to
// find. From an application you go through the gateway on :6432, where the
// database name is the branch and the password is an API key. From this machine
// you need no key at all: the engine opens psql inside the branch's own
// container. `fox connect` does the second and prints the first, because until
// now `fox psql` only ever opened main and `fox branch create` printed a Docker
// container id.

// KeyPlaceholder stands in for the API key in a printed connection string.
// Nothing mints a key here on purpose: a key is a credential, and it belongs to
// the person who will use it, created when they ask for it.
const KeyPlaceholder = "<API_KEY>"

// GatewayDSN is the connection string an application uses for a branch. With an
// empty key it carries KeyPlaceholder, so the shape is right and the missing
// secret is obvious.
func GatewayDSN(branchName, key string) string {
	shown := key
	if shown == "" {
		shown = KeyPlaceholder
	}
	u := url.URL{Scheme: "postgresql", Host: gatewayHostPort(), Path: "/" + branchName}
	u.User = url.UserPassword(pgUser, shown)
	u.RawQuery = "sslmode=require"
	out := u.String()
	if key == "" {
		// url escapes the placeholder's angle brackets, which makes the line
		// harder to read than the thing it stands for.
		out = strings.ReplaceAll(out, url.QueryEscape(KeyPlaceholder), KeyPlaceholder)
	}
	return out
}

// Connect prints how to reach a branch from an application and then, unless
// dsnOnly, opens an interactive psql on it. That session goes through the
// container rather than the gateway, so it needs no API key.
func Connect(name string, dsnOnly bool) error {
	if err := checkName(name); err != nil {
		return err
	}
	if !Exists(name) {
		return fmt.Errorf("no branch %q — list them with `%s branch list`", name, brand.CLI)
	}
	if st := ContainerState(name); st != "running" {
		fmt.Printf("Branch %q is %s; starting it …\n", name, st)
		// Wake, not Resume: for main it routes to the current primary instead of
		// starting a container that may be a stepped-down old one.
		if err := Wake(name); err != nil {
			return fmt.Errorf("starting %q: %w", name, err)
		}
	}
	fmt.Println("From your application (through the gateway, over TLS, in any language):")
	fmt.Printf("  %s\n", GatewayDSN(name, ""))
	fmt.Printf("\nThat password is an API key, not an account password. Mint one with:\n")
	fmt.Printf("  %s apikey create <email> %s\n", brand.CLI, name)
	// The read-only endpoint, offered where somebody is already looking for a
	// connection string rather than behind a flag they would have to know
	// about. Only when there is a standby to answer it, and the same key opens
	// both: it is the same branch, read from the copy.
	if _, err := StandbyReadAddr(name); err == nil {
		fmt.Printf("\nRead-only sessions can be answered by the standby instead, which keeps a long\n"+
			"report off the primary. Same key, one different name — and the data may be a\n"+
			"little behind (`%s ha status` says how far):\n", brand.CLI)
		fmt.Printf("  %s\n", GatewayDSN(StandbyReadName(name), ""))
	}
	if dsnOnly {
		return nil
	}
	fmt.Printf("\nOpening psql on %q — from this machine no key is needed (\\q to leave) …\n\n", name)
	return PsqlShell(name)
}

// PrintConnectHint writes the lines a user needs once a branch exists: where it
// is, how to get the password, and how to open a shell on it.
func PrintConnectHint(w io.Writer, name string) {
	fmt.Fprintf(w, "  connect  %s\n", GatewayDSN(name, ""))
	fmt.Fprintf(w, "  key      %s apikey create <email> %s   (the password above; shown once)\n", brand.CLI, name)
	fmt.Fprintf(w, "  shell    %s connect %s\n", brand.CLI, name)
}
