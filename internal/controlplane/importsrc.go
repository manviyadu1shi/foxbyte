// SPDX-License-Identifier: AGPL-3.0-or-later

package controlplane

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// An import connects from inside the engine to whatever a connection string
// names. Any signed-in account could therefore make the engine probe its own
// network — the object store, other branches' Postgres, the services on this
// machine, a cloud metadata endpoint — and read the answer back through the
// import's errors or the branch it filled (audit v2 G12). So:
//
//   - link-local and cloud metadata addresses are refused for everyone: no
//     database lives there, and credentials do;
//   - loopback, private networks and names only the engine's network resolves
//     (a container name) are for admins;
//   - anything else — a database with a public address — anyone may import.
//
// The check resolves the name when the import starts. For an admin that is the
// end of it. For anyone else the import is then held to the addresses that were
// checked (pinImportSource), so a name that resolves somewhere else a moment
// later — DNS rebinding — still reaches the address that passed.

// importHosts lists the hosts a connection string would connect to.
func importHosts(src string) ([]string, error) {
	scheme, rest, ok := strings.Cut(src, "://")
	if !ok {
		return nil, fmt.Errorf("not a connection string")
	}
	authority := rest
	query := ""
	if i := strings.IndexAny(authority, "/?"); i >= 0 {
		if j := strings.IndexByte(authority, '?'); j >= 0 {
			query = authority[j+1:]
		}
		authority = authority[:i]
	}
	if i := strings.LastIndexByte(authority, '@'); i >= 0 {
		authority = authority[i+1:]
	}
	var hosts []string
	for _, hp := range strings.Split(authority, ",") {
		if h := hostOnly(hp); h != "" {
			hosts = append(hosts, h)
		}
	}
	// libpq also takes the host from the query string, and it wins.
	if q, err := url.ParseQuery(query); err == nil {
		for _, k := range []string{"host", "hostaddr"} {
			for _, v := range q[k] {
				for _, hp := range strings.Split(v, ",") {
					if h := hostOnly(hp); h != "" {
						hosts = append(hosts, h)
					}
				}
			}
		}
	}
	if len(hosts) == 0 {
		// No host: libpq and the drivers connect to this machine.
		hosts = []string{"localhost"}
	}
	if scheme == "mongodb+srv" {
		var out []string
		for _, h := range hosts {
			_, srvs, err := net.LookupSRV("mongodb", "tcp", h)
			if err != nil {
				return nil, fmt.Errorf("cannot resolve %s: %w", h, err)
			}
			for _, s := range srvs {
				out = append(out, strings.TrimSuffix(s.Target, "."))
			}
		}
		return out, nil
	}
	return hosts, nil
}

func hostOnly(hp string) string {
	hp = strings.TrimSpace(hp)
	if hp == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(hp); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(hp, "[]")
}

// metadataNames are cloud instance-metadata endpoints reached by name.
var metadataNames = map[string]bool{
	"metadata.google.internal": true, "metadata": true, "metadata.goog": true, "instance-data": true,
}

// neverIP is an address no import may reach, whoever asks.
func neverIP(ip net.IP) bool {
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsUnspecified() || ip.Equal(net.ParseIP("100.100.100.200")) || ip.Equal(net.ParseIP("fd00:ec2::254"))
}

// cgnat is 100.64.0.0/10, shared address space: internal to a provider.
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// internalIP is an address on this machine or a private network.
func internalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || cgnat.Contains(ip)
}

// lookupIP resolves a host; a variable so tests need no DNS.
var lookupIP = net.LookupIP

// checkImportSource applies the rule above to a connection string.
func checkImportSource(src string, admin bool) error {
	_, err := vetImportSource(src, admin)
	return err
}

// vetImportSource is checkImportSource, also returning the address each name
// was found at: the one an import is to be held to.
func vetImportSource(src string, admin bool) (map[string]string, error) {
	hosts, err := importHosts(src)
	if err != nil {
		return nil, err
	}
	found := map[string]string{}
	for _, h := range hosts {
		if metadataNames[strings.ToLower(strings.TrimSuffix(h, "."))] {
			return nil, fmt.Errorf("imports cannot connect to %s: it is a cloud metadata endpoint, not a database", h)
		}
		var ips []net.IP
		if ip := net.ParseIP(h); ip != nil {
			ips = []net.IP{ip}
		} else if ips, err = lookupIP(h); err != nil || len(ips) == 0 {
			// A name only the engine's own network knows — a container.
			if admin {
				continue
			}
			return nil, fmt.Errorf("cannot resolve %s; only an admin may import from a host that is not publicly resolvable", h)
		} else {
			found[h] = preferIPv4(ips).String()
		}
		for _, ip := range ips {
			if neverIP(ip) {
				return nil, fmt.Errorf("imports cannot connect to %s (%s): link-local and metadata addresses are refused", h, ip)
			}
			if internalIP(ip) && !admin {
				return nil, fmt.Errorf("%s is on this machine or a private network (%s); only an admin may import from there", h, ip)
			}
		}
	}
	return found, nil
}

// preferIPv4 picks the address to hold a name to. Every one of them passed;
// an IPv4 one is chosen where there is one, since a container's network often
// has no IPv6 route.
func preferIPv4(ips []net.IP) net.IP {
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip
		}
	}
	return ips[0]
}
