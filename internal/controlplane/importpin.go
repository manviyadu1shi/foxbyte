// SPDX-License-Identifier: AGPL-3.0-or-later

package controlplane

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// lookupSRV and lookupTXT resolve a mongodb+srv:// address; variables so tests
// need no DNS.
var (
	lookupSRV = net.LookupSRV
	lookupTXT = net.LookupTXT
)

// pinImportSource is the import guard for a source an import is about to
// connect to. It returns the connection string to use and the "name:ip" pins
// for branch.As.
//
// An admin's source passes or fails the guard and is used as given, as always.
// Anyone else's is held to the addresses the guard checked:
//
//   - postgres:// gains libpq's hostaddr, one address per host. libpq then
//     connects there and uses the name only for TLS, so nothing is looked up
//     again — not by pg_dump, not by a continuous import's subscription.
//   - mysql://, mariadb:// and mongodb:// are used as given; their tools run in
//     throwaway containers, and the pins become --add-host entries there.
//   - mongodb+srv:// is looked up here, once, and written out as the mongodb://
//     seed list it names (with TLS, which +srv implies, and the options its TXT
//     record may set). The driver would otherwise look the SRV record up again
//     at connect time, and could be sent anywhere by then.
func pinImportSource(src string, admin bool) (string, []string, error) {
	if admin {
		return src, nil, checkImportSource(src, true)
	}
	if strings.HasPrefix(src, "mongodb+srv://") {
		seed, err := expandMongoSRV(src)
		if err != nil {
			return "", nil, err
		}
		src = seed
	}
	found, err := vetImportSource(src, false)
	if err != nil {
		return "", nil, err
	}
	pins := make([]string, 0, len(found))
	for name, ip := range found {
		pins = append(pins, name+":"+ip)
	}
	sort.Strings(pins)
	if strings.HasPrefix(src, "postgres://") || strings.HasPrefix(src, "postgresql://") {
		if src, err = withHostaddr(src, found); err != nil {
			return "", nil, err
		}
	}
	return src, pins, nil
}

// splitURI cuts scheme://userinfo@hosts/path?query into its parts. userinfo
// keeps its "@", and tail is everything from the first / or ?.
func splitURI(src string) (scheme, userinfo, hosts, tail string) {
	scheme, rest, _ := strings.Cut(src, "://")
	hosts = rest
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		hosts, tail = rest[:i], rest[i:]
	}
	if i := strings.LastIndexByte(hosts, '@'); i >= 0 {
		userinfo, hosts = hosts[:i+1], hosts[i+1:]
	}
	return scheme, userinfo, hosts, tail
}

// withHostaddr adds libpq's hostaddr to a postgres:// address: for each host it
// will try, the address that host was checked at.
func withHostaddr(src string, found map[string]string) (string, error) {
	_, _, authority, tail := splitURI(src)
	query := ""
	if _, q, ok := strings.Cut(tail, "?"); ok {
		query = q
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return "", fmt.Errorf("cannot read the options in this connection string: %v", err)
	}
	if len(q["hostaddr"]) > 0 {
		return src, nil // addresses already, checked as such; libpq looks nothing up
	}
	list := authority
	if hs := q["host"]; len(hs) > 0 {
		list = strings.Join(hs, ",") // libpq takes the host from the query when it is there
	}
	var addrs []string
	for _, hp := range strings.Split(list, ",") {
		h := hostOnly(hp)
		if ip := net.ParseIP(h); ip != nil {
			addrs = append(addrs, ip.String())
			continue
		}
		ip, ok := found[h]
		if !ok {
			return "", fmt.Errorf("cannot hold %s to the address it was checked at", h)
		}
		addrs = append(addrs, ip)
	}
	sep := "?"
	if strings.Contains(tail, "?") {
		sep = "&"
	}
	return src + sep + "hostaddr=" + url.QueryEscape(strings.Join(addrs, ",")), nil
}

// srvOptions are the only options a mongodb+srv TXT record may set.
var srvOptions = []string{"authSource", "replicaSet", "loadBalanced"}

// expandMongoSRV turns a mongodb+srv:// address into the mongodb:// seed list
// its SRV record names, as a driver would at connect time.
func expandMongoSRV(src string) (string, error) {
	_, userinfo, authority, tail := splitURI(src)
	host := hostOnly(authority)
	if host == "" || strings.Contains(authority, ",") || strings.Contains(authority, ":") {
		return "", fmt.Errorf("a mongodb+srv:// address names exactly one host, with no port")
	}
	// A driver accepts only targets in the host's own parent domain; so does this.
	dot := strings.IndexByte(host, '.')
	if dot < 0 {
		return "", fmt.Errorf("a mongodb+srv:// host must be a domain name, got %s", host)
	}
	parent := strings.ToLower(host[dot:])
	_, srvs, err := lookupSRV("mongodb", "tcp", host)
	if err != nil || len(srvs) == 0 {
		return "", fmt.Errorf("cannot resolve %s: %v", host, err)
	}
	var seeds []string
	for _, s := range srvs {
		t := strings.TrimSuffix(s.Target, ".")
		if !strings.HasSuffix(strings.ToLower(t), parent) {
			return "", fmt.Errorf("%s's SRV record names %s, outside %s", host, t, parent[1:])
		}
		seeds = append(seeds, net.JoinHostPort(t, strconv.Itoa(int(s.Port))))
	}
	path, query, _ := strings.Cut(tail, "?")
	own, err := url.ParseQuery(query)
	if err != nil {
		return "", fmt.Errorf("cannot read the options in this connection string: %v", err)
	}
	opts := url.Values{}
	if txts, err := lookupTXT(host); err == nil {
		for _, t := range txts {
			if v, err := url.ParseQuery(t); err == nil {
				for _, k := range srvOptions {
					if x := v.Get(k); x != "" {
						opts.Set(k, x)
					}
				}
			}
		}
	}
	for k, vs := range own { // the address's own options win over the record's
		opts[k] = vs
	}
	if own.Get("tls") == "" && own.Get("ssl") == "" {
		opts.Set("tls", "true") // +srv means TLS unless it says otherwise
	}
	if path == "" {
		path = "/"
	}
	return "mongodb://" + userinfo + strings.Join(seeds, ",") + path + "?" + opts.Encode(), nil
}
