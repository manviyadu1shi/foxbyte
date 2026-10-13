// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"net"
	"strings"
	"testing"
)

// The certificate's identity decides whether verify-full succeeds, and getting
// it wrong fails in the least helpful way available: the standby connects, the
// handshake completes, and the certificate is rejected for naming somebody
// else. So the names are a pure function with a test rather than a line in the
// middle of a command.
func TestServerCertNames(t *testing.T) {
	t.Run("an IP address goes in IPAddresses, not DNSNames", func(t *testing.T) {
		dns, ips := serverCertNames("192.168.105.2")
		if hasString(dns, "192.168.105.2") {
			t.Error("an IP was put in DNSNames, where verify-full will not match it")
		}
		if !hasIP(ips, "192.168.105.2") {
			t.Error("the address is not in IPAddresses")
		}
	})

	t.Run("a hostname goes in DNSNames", func(t *testing.T) {
		dns, ips := serverCertNames("db1.internal")
		if !hasString(dns, "db1.internal") {
			t.Error("the hostname is not in DNSNames")
		}
		if len(ips) != 2 { // the two loopbacks, and nothing invented
			t.Errorf("a hostname produced %d IP addresses", len(ips))
		}
	})

	t.Run("localhost and loopback are always there", func(t *testing.T) {
		// Turning replication TLS on must not break the engine's own local
		// connections, which reach Postgres as localhost.
		for _, addr := range []string{"", "192.168.105.2", "db1.internal"} {
			dns, ips := serverCertNames(addr)
			if !hasString(dns, "localhost") {
				t.Errorf("addr %q: localhost is missing", addr)
			}
			if !hasIP(ips, "127.0.0.1") {
				t.Errorf("addr %q: 127.0.0.1 is missing", addr)
			}
		}
	})

	t.Run("an empty address invents nothing", func(t *testing.T) {
		dns, ips := serverCertNames("   ")
		if len(dns) != 1 || len(ips) != 2 {
			t.Errorf("an empty address produced %v / %v", dns, ips)
		}
	})
}

// The CA's private half is the one thing here that must never leave the
// machine, and the certificate is the one thing that must. Names kept apart so
// a future change cannot ship the wrong one.
func TestTheCAKeyAndCertAreDistinctFiles(t *testing.T) {
	if nodeCACertPath() == nodeCAKeyPath() {
		t.Fatal("the CA certificate and key are the same file")
	}
	if !strings.HasSuffix(nodeCAKeyPath(), ".key") || !strings.HasSuffix(nodeCACertPath(), ".crt") {
		t.Errorf("the CA files are not named for what they are: %s, %s", nodeCACertPath(), nodeCAKeyPath())
	}
	// The server files live in the data directory beside Postgres's own, so
	// they have to be obviously ours.
	for _, n := range []string{nodeServerCertName, nodeServerKeyName} {
		if !strings.HasPrefix(n, "fox-") {
			t.Errorf("%q is not obviously ours beside Postgres's own files", n)
		}
	}
}

func hasString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func hasIP(ips []net.IP, want string) bool {
	w := net.ParseIP(want)
	for _, ip := range ips {
		if ip.Equal(w) {
			return true
		}
	}
	return false
}
