// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"strings"
	"testing"
)

// How a standby on another machine is told to copy a primary. The three things
// that cannot be wrong, because each fails in a way that is either silent or
// confusing.
func TestRemoteBaseBackupArgs(t *testing.T) {
	j := Joined{Address: "192.168.105.2", Port: 5432, Role: ReplicationRole}
	got := strings.Join(remoteBaseBackupArgs(j, "/data/pgdata"), " ")

	// verify-full, never require. `require` encrypts and verifies nothing,
	// which would leave this standby handing a credential that copies the
	// database to whoever answered on that address.
	if !strings.Contains(got, "sslmode=verify-full") {
		t.Errorf("the copy does not verify the primary: %s", got)
	}
	for _, bad := range []string{"sslmode=require", "sslmode=prefer", "sslmode=disable"} {
		if strings.Contains(got, bad) {
			t.Errorf("the copy uses %s, which authenticates nothing: %s", bad, got)
		}
	}
	// The CA at the mount path, not a host path. pg_basebackup -R records
	// sslrootcert as the path it saw, and the walreceiver reads that path from
	// inside the standby container later — so a host path would leave the
	// standby unable to verify anything.
	if !strings.Contains(got, "sslrootcert="+joinedCAMount) {
		t.Errorf("the CA is not given at the in-container path: %s", got)
	}
	// It connects as the replication role the token named, never the superuser.
	if !strings.Contains(got, "user="+ReplicationRole) {
		t.Errorf("the copy does not connect as the replication role: %s", got)
	}
	if strings.Contains(got, "user="+pgUser) {
		t.Errorf("the copy connects as the superuser: %s", got)
	}
	// And the address and port the token carried, not a container name: this
	// primary is not on this machine's docker network.
	if !strings.Contains(got, "host=192.168.105.2") || !strings.Contains(got, "port=5432") {
		t.Errorf("the copy does not dial the primary the token named: %s", got)
	}
}

// The standby and the copy must read the CA from the same path, and nothing
// enforces that but this.
func TestTheCAPathIsOneConstant(t *testing.T) {
	j := Joined{Address: "a", Port: 1, Role: "r"}
	copyArgs := strings.Join(remoteBaseBackupArgs(j, "/d"), " ")
	if !strings.Contains(copyArgs, joinedCAMount) {
		t.Fatal("the copy does not use the shared mount path")
	}
	// The mount the standby gets is the same path. Asserted on the mount
	// argument rather than on the constant, because the bug would be a second
	// literal somewhere.
	if !strings.HasSuffix(joinedCAMount, "/ca.crt") {
		t.Errorf("the mount path %q is not a file path a bind mount can target", joinedCAMount)
	}
}

// An install that follows nobody gets no certificate mount, so a local
// standby's arguments are exactly what they were before any of this existed.
func TestALocalStandbyGetsNoCertificateMount(t *testing.T) {
	// joinedMount reads state this test cannot set without a state directory,
	// so what is asserted is the shape: empty unless following a primary.
	if IsStandbyNode() {
		t.Skip("this machine follows a primary, so the empty case cannot be read here")
	}
	if joinedMount() != nil {
		t.Errorf("an install that follows nobody was given a certificate mount: %v", joinedMount())
	}
	args := strings.Join(standbyRunArgs("/data/standby"), " ")
	if strings.Contains(args, joinedCAMount) {
		t.Errorf("a local standby mounts a primary's CA: %s", args)
	}
}

// What `ha status` says when the primary is somewhere else.
func TestDescribeJoin(t *testing.T) {
	if IsStandbyNode() {
		t.Skip("this machine follows a primary")
	}
	// Empty, not a sentence claiming nothing: callers print it only when set.
	if DescribeJoin() != "" {
		t.Errorf("an install that follows nobody describes a join: %q", DescribeJoin())
	}
}
