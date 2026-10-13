// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// The probe behind the fence. A TCP connection and nothing more: the question
// is "would promoting here make a second primary", and anything answering on
// that port is reason enough to believe it would.
func TestRemotePrimaryAnswering(t *testing.T) {
	// Something listening is a primary as far as this is concerned.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback listener available")
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	var p int
	if _, err := fmt.Sscan(port, &p); err != nil {
		t.Fatal(err)
	}
	if !RemotePrimaryAnswering(Joined{Address: "127.0.0.1", Port: p}, 2*time.Second) {
		t.Error("a listening port was read as a dead primary, which would allow a second one")
	}

	// And a closed port is a primary that has gone.
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback listener available")
	}
	_, port2, _ := net.SplitHostPort(ln2.Addr().String())
	var p2 int
	if _, err := fmt.Sscan(port2, &p2); err != nil {
		t.Fatal(err)
	}
	_ = ln2.Close()
	if RemotePrimaryAnswering(Joined{Address: "127.0.0.1", Port: p2}, 2*time.Second) {
		t.Error("a closed port was read as a live primary, which would block every failover")
	}
}

// An unroutable address must not hang the command: a failover is run when
// something is already wrong, and the timeout is the only thing bounding it.
func TestRemotePrimaryAnsweringGivesUp(t *testing.T) {
	start := time.Now()
	// 203.0.113.0/24 is reserved for documentation and routes nowhere.
	if RemotePrimaryAnswering(Joined{Address: "203.0.113.1", Port: 5432}, 2*time.Second) {
		t.Error("an unroutable address answered")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the probe took %s; a failover cannot wait on a dead network", d)
	}
}

// A standby of this machine's own main needs no fence: stopping the local
// primary is the whole of it, and HAFailover already does that.
func TestALocalStandbyNeedsNoFence(t *testing.T) {
	if IsStandbyNode() {
		t.Skip("this machine follows a remote primary")
	}
	if err := fenceRemotePrimary(false); err != nil {
		t.Errorf("a local standby was refused a promotion: %v", err)
	}
}

// What the refusal has to say. The message is the whole value of the guard:
// somebody is reading it during an incident, and it has to name the way
// forward and its own limit.
func TestTheFenceRefusalNamesTheLimit(t *testing.T) {
	// Rendered from a known state rather than from the machine's own, so the
	// text is readable in a test.
	j := Joined{Address: "10.0.0.9", Port: 5432}
	msg := fenceRefusal(j)
	for _, want := range []string{
		"still answering",     // what it found
		"diverge",             // what it would cost
		"Stop that one first", // the way forward
		"--force",             // the escape hatch, named
		"partitioned",         // and the limit of the check itself
		"third opinion",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "10.0.0.9") {
		t.Error("the refusal does not name the primary it found")
	}
}
