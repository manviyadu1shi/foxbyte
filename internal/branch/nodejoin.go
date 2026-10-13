// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thefoxbyte/foxbyte/internal/brand"
)

// The other side of `node expose`: this install becomes the standby for a
// primary on another machine.
//
// Phase 1 of docs/FOX_HA_Plan.md, and the last of its items. What it reuses is
// the point — the standby container, its storage, the promotion, the failover
// refusals, slot synchronisation, zero-data-loss mode and the drill all work
// on a standby whose primary is across a network exactly as they do on one
// whose primary is the container next to it. The only differences are where
// the base backup comes from and that the walreceiver has a certificate to
// verify.
//
// What this install's own `main` is for afterwards: nothing. A node that hosts
// a standby of another install is a standby node, and its own main sits idle.
// That is honest rather than elegant, and it is what makes this additive: no
// existing path changes meaning, and `ha failover` on this machine promotes the
// standby and reroutes exactly as it always has.

// joinedCAName is where the primary's CA lives on this machine, and
// joinedCAMount where it appears inside the containers that need it.
//
// The same path in both the base backup and the standby, and that is not a
// tidiness choice: pg_basebackup -R copies the parameters it connected with
// into primary_conninfo, sslrootcert among them, as the path it saw. The
// walreceiver then reads that path from inside the standby container. Give the
// backup a different path from the standby and the standby cannot verify
// anything — measured.
const (
	joinedCAName  = "ca-primary.crt"
	joinedCAMount = "/fox-tls/ca.crt"
)

func joinedCAPath() string { return filepath.Join(nodeCADir(), joinedCAName) }

// Joined is what this install knows about the primary it follows.
type Joined struct {
	Address string    `json:"address"`
	Port    int       `json:"port"`
	Role    string    `json:"role"`
	At      time.Time `json:"at"`
}

func joinedPath() string { return brand.StatePath("node-joined.json") }

// CurrentJoin reports the primary this install is a standby of, or false when
// it is a primary in its own right — which is every install until somebody
// runs `node join`.
func CurrentJoin() (Joined, bool) {
	b, err := os.ReadFile(joinedPath())
	if err != nil {
		return Joined{}, false
	}
	var j Joined
	if err := json.Unmarshal(b, &j); err != nil || j.Address == "" {
		return Joined{}, false
	}
	return j, true
}

// IsStandbyNode reports whether this install follows a primary elsewhere. Read
// by startStandbyContainer and by the guards on `ha enable`, so it must be
// cheap and must not need a database.
func IsStandbyNode() bool { _, ok := CurrentJoin(); return ok }

// joinedMount is the CA, read-only, for a container that has to verify the
// primary. Empty on an install that follows nobody, so a local standby's
// arguments are exactly what they were.
func joinedMount() []string {
	if !IsStandbyNode() {
		return nil
	}
	return []string{"-v", joinedCAPath() + ":" + joinedCAMount + ":ro"}
}

// remoteBaseBackupArgs copies a primary on another machine.
//
// Separate from baseBackupArgs rather than another flag on it: that one takes
// a container name on this host's docker network and this one takes an address,
// a port, a TLS mode and a certificate — and the local form has a test
// asserting it is byte-for-byte what it has always been, which is worth
// keeping readable.
//
// sslmode=verify-full, not require. `require` encrypts and verifies nothing,
// which would leave this standby sending a credential that copies the database
// to whoever answered on that address.
func remoteBaseBackupArgs(j Joined, dataDir string) []string {
	conninfo := fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=verify-full sslrootcert=%s",
		j.Address, j.Port, j.Role, pgDatabase, joinedCAMount)
	return []string{
		"pg_basebackup", "-d", conninfo, "-D", dataDir,
		"-R", "-X", "stream", "-c", "fast",
	}
}

// NodeJoin makes this install the standby of the primary a token describes.
//
// Not destructive of this install's own data: the standby has storage of its
// own (the same storage `ha enable` uses), so main is untouched. Re-running it
// rebuilds the standby from the primary, which is how a standby that has fallen
// too far behind is repaired.
func NodeJoin(token string) error {
	p, err := ParseJoinToken(token)
	if err != nil {
		return err
	}
	if PrimaryContainer() == container("standby") {
		return fmt.Errorf("this install is serving 'main' from its promoted standby since `%s ha failover`.\n"+
			"Run `%s ha failback` first", brand.CLI, brand.CLI)
	}
	// A standby of this install's own main, and a standby of somebody else's,
	// are the same container and the same storage. Doing both is not a
	// configuration, it is a collision.
	if !IsStandbyNode() && ContainerState("standby") != "absent" {
		return fmt.Errorf("this install already has a standby of its own main.\n"+
			"Run `%s ha disable` first if this machine should follow %s instead",
			brand.CLI, p.Address)
	}

	// The CA before anything else: without it the backup cannot verify the
	// primary, and verifying is the whole reason the token carries it.
	if err := os.MkdirAll(nodeCADir(), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(joinedCAPath(), []byte(p.CACert), 0o644); err != nil {
		return fmt.Errorf("writing the primary's CA certificate: %w", err)
	}

	j := Joined{Address: p.Address, Port: p.Port, Role: p.Role, At: time.Now().UTC()}

	fmt.Printf("1/4 copying %s:%d over TLS…\n", j.Address, j.Port)
	store := activeStorage()
	quiet("docker", "rm", "-f", container("standby"))
	if err := store.resetStandby(); err != nil {
		return err
	}
	if err := run("chown", "-R", pgUID+":"+pgUID, store.standbyPath()); err != nil {
		return err
	}
	// The replication role's password comes from the token: this install's own
	// secret derives a different one, and the primary knows nothing about it.
	env := joinedReplicaEnvFile(p.Password)
	args := append([]string{"run", "--rm", "--user", pgUID, "--network", network,
		"--env-file", env,
		"-v", store.standbyPath() + ":/data",
		"-v", joinedCAPath() + ":" + joinedCAMount + ":ro",
		pgImage()},
		remoteBaseBackupArgs(j, "/data/pgdata")...)
	if err := run("docker", args...); err != nil {
		return fmt.Errorf("copying the primary: %w\n\n"+
			"Check that `%s node expose` has been run there, that this machine's address is the\n"+
			"--peer it names, and that %s:%d is reachable from here", err, brand.CLI, j.Address, j.Port)
	}

	// Recorded only once the copy exists, so a failed join leaves an install
	// that is still its own primary rather than one claiming to follow
	// somebody it never reached.
	fmt.Println("2/4 recording the primary…")
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(joinedPath(), append(b, '\n'), 0o600); err != nil {
		return err
	}

	fmt.Println("3/4 starting the standby…")
	if err := startStandbyContainer(store); err != nil {
		return err
	}
	if err := waitReady("standby"); err != nil {
		return err
	}

	fmt.Println("4/4 waiting for it to stream…")
	if err := pollUntil("the standby to start streaming from "+j.Address, 2*time.Minute, func() bool {
		return StandbyStreamingFromRemote()
	}); err != nil {
		return fmt.Errorf("%w\n\nThe copy succeeded, so the credentials and TLS are right; the standby is not\n"+
			"replaying yet. `%s ha status` and `%s logs` say more", err, brand.CLI, brand.CLI)
	}

	fmt.Printf("\nThis machine is now a standby of %s:%d.\n", j.Address, j.Port)
	fmt.Printf("  Streaming over TLS as %s, verifying that primary's certificate.\n", j.Role)
	fmt.Printf("  `%s ha status` shows how far behind it is; `%s ha failover` promotes it.\n",
		brand.CLI, brand.CLI)
	fmt.Printf("  This install's own 'main' is idle while it follows another primary.\n")
	return nil
}

// StandbyStreamingFromRemote reports whether the local standby is receiving
// WAL from the primary it follows.
//
// Asked of the standby rather than of the primary, which is the only side
// reachable from here: pg_stat_replication lives on the sender, and this
// machine is the receiver. pg_stat_wal_receiver is the mirror of it.
func StandbyStreamingFromRemote() bool {
	out, err := capture("docker", "exec", "--env-file", pgEnvFile(), container("standby"),
		"psql", "-U", pgUser, "-d", pgDatabase, "-tAc",
		"SELECT count(*) FROM pg_stat_wal_receiver WHERE status = 'streaming'")
	return err == nil && strings.TrimSpace(out) == "1"
}

// NodeLeave stops following the primary. The standby's storage goes with it,
// because a copy of somebody else's database that nothing keeps up to date is
// not a backup, it is a stale liability.
func NodeLeave() error {
	j, ok := CurrentJoin()
	if !ok {
		fmt.Println("This install follows no other primary.")
		return nil
	}
	if PrimaryContainer() == container("standby") {
		return fmt.Errorf("this machine is serving 'main' from the standby it was following %s with.\n"+
			"Leaving now would delete the only copy of those writes. Run `%s ha failback` first",
			j.Address, brand.CLI)
	}
	quiet("docker", "rm", "-f", container("standby"))
	activeStorage().destroyStandby()
	if err := os.Remove(joinedPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(joinedCAPath())
	fmt.Printf("No longer following %s:%d. The standby and its copy are gone.\n", j.Address, j.Port)
	return nil
}

// DescribeJoin is the one-line summary `ha status` and `fox check` use.
func DescribeJoin() string {
	j, ok := CurrentJoin()
	if !ok {
		return ""
	}
	return fmt.Sprintf("a standby of %s:%d, over TLS as %s", j.Address, j.Port, j.Role)
}
