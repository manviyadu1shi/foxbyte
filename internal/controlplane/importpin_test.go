// SPDX-License-Identifier: AGPL-3.0-or-later

package controlplane

import (
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
)

// fakeDNS answers lookups from a table, so these tests need no network.
func fakeDNS(t *testing.T, a map[string][]string, srv map[string][]*net.SRV, txt map[string][]string) {
	t.Helper()
	oldIP, oldSRV, oldTXT := lookupIP, lookupSRV, lookupTXT
	t.Cleanup(func() { lookupIP, lookupSRV, lookupTXT = oldIP, oldSRV, oldTXT })
	lookupIP = func(h string) ([]net.IP, error) {
		var out []net.IP
		for _, s := range a[h] {
			out = append(out, net.ParseIP(s))
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no such host %s", h)
		}
		return out, nil
	}
	lookupSRV = func(service, proto, name string) (string, []*net.SRV, error) {
		if s, ok := srv[name]; ok {
			return "", s, nil
		}
		return "", nil, fmt.Errorf("no SRV for %s", name)
	}
	lookupTXT = func(name string) ([]string, error) { return txt[name], nil }
}

// An admin's source is used as given: the guard passes or fails it, nothing more.
func TestAnAdminsSourceIsUsedAsGiven(t *testing.T) {
	fakeDNS(t, map[string][]string{"db.example.com": {"203.0.113.7"}}, nil, nil)
	src := "postgres://u:p@db.example.com:5432/app"
	got, pins, err := pinImportSource(src, true)
	if err != nil || got != src || pins != nil {
		t.Fatalf("got %q %v %v", got, pins, err)
	}
}

func TestAPostgresSourceIsHeldToTheCheckedAddress(t *testing.T) {
	fakeDNS(t, map[string][]string{
		"db.example.com": {"2001:db8::1", "203.0.113.7"},
		"b.example.com":  {"198.51.100.2"},
	}, nil, nil)
	for _, c := range []struct{ in, want string }{
		{"postgres://u:p@db.example.com:5432/app", "postgres://u:p@db.example.com:5432/app?hostaddr=203.0.113.7"},
		{"postgresql://db.example.com/app?sslmode=verify-full", "postgresql://db.example.com/app?sslmode=verify-full&hostaddr=203.0.113.7"},
		// Several hosts: one address each, in order; an address stays itself.
		{"postgres://db.example.com:5432,198.51.100.9:5433/app", "postgres://db.example.com:5432,198.51.100.9:5433/app?hostaddr=203.0.113.7%2C198.51.100.9"},
		// libpq takes the host from the query when it is there.
		{"postgres://db.example.com/app?host=b.example.com", "postgres://db.example.com/app?host=b.example.com&hostaddr=198.51.100.2"},
		// Already addresses: nothing is looked up, nothing to add.
		{"postgres://db.example.com/app?hostaddr=203.0.113.9", "postgres://db.example.com/app?hostaddr=203.0.113.9"},
	} {
		got, _, err := pinImportSource(c.in, false)
		if err != nil || got != c.want {
			t.Errorf("%s:\n got %q (%v)\nwant %q", c.in, got, err, c.want)
		}
	}
}

func TestOtherSourcesArePinnedForTheirContainers(t *testing.T) {
	fakeDNS(t, map[string][]string{"my.example.com": {"203.0.113.8"}, "mg.example.com": {"203.0.113.9"}}, nil, nil)
	for src, want := range map[string][]string{
		"mysql://u:p@my.example.com:3306/app":               {"my.example.com:203.0.113.8"},
		"mongodb://mg.example.com:27017,my.example.com/app": {"mg.example.com:203.0.113.9", "my.example.com:203.0.113.8"},
	} {
		got, pins, err := pinImportSource(src, false)
		if err != nil || got != src || !reflect.DeepEqual(pins, want) {
			t.Errorf("%s: got %q %v %v, want the source unchanged and %v", src, got, pins, err, want)
		}
	}
}

// The guard still decides: pinning never lets through what it refused.
func TestPinningStillRefusesPrivateAddresses(t *testing.T) {
	fakeDNS(t, map[string][]string{"in.example.com": {"10.0.0.5"}}, nil, nil)
	if _, _, err := pinImportSource("postgres://in.example.com/app", false); err == nil || !strings.Contains(err.Error(), "only an admin") {
		t.Fatalf("a private address passed: %v", err)
	}
}

func TestAMongoSRVAddressIsExpandedOnceAndPinned(t *testing.T) {
	fakeDNS(t,
		map[string][]string{"n1.c.example.com": {"203.0.113.21"}, "n2.c.example.com": {"203.0.113.22"}},
		map[string][]*net.SRV{"cl.c.example.com": {{Target: "n1.c.example.com.", Port: 27017}, {Target: "n2.c.example.com.", Port: 27018}}},
		map[string][]string{"cl.c.example.com": {"authSource=admin&replicaSet=rs0&w=0"}})
	got, pins, err := pinImportSource("mongodb+srv://u:p%40x@cl.c.example.com/app?appName=fox&replicaSet=mine", false)
	if err != nil {
		t.Fatal(err)
	}
	// The record's options (only those it may set), the address's own winning,
	// and TLS, which +srv implies.
	want := "mongodb://u:p%40x@n1.c.example.com:27017,n2.c.example.com:27018/app?appName=fox&authSource=admin&replicaSet=mine&tls=true"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if !reflect.DeepEqual(pins, []string{"n1.c.example.com:203.0.113.21", "n2.c.example.com:203.0.113.22"}) {
		t.Errorf("pins %v", pins)
	}
	// tls=false said outright is kept.
	if got, _, _ := pinImportSource("mongodb+srv://cl.c.example.com/?tls=false", false); !strings.Contains(got, "tls=false") || strings.Contains(got, "tls=true") {
		t.Errorf("tls=false not kept: %q", got)
	}
}

// A driver refuses SRV targets outside the host's parent domain, and so does this.
func TestAMongoSRVTargetMustShareTheParentDomain(t *testing.T) {
	fakeDNS(t, map[string][]string{"x.elsewhere.net": {"203.0.113.30"}},
		map[string][]*net.SRV{"cl.c.example.com": {{Target: "x.elsewhere.net.", Port: 27017}}}, nil)
	if _, _, err := pinImportSource("mongodb+srv://cl.c.example.com/app", false); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("a foreign target passed: %v", err)
	}
}
