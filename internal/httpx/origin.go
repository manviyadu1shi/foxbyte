// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"net/http"
	"net/url"
	"strings"
)

// RefuseCrossSite refuses a request that changes something when a browser sent
// it from a page this server does not trust (cross-site request forgery).
//
// The session cookie is SameSite=Lax by default, which already keeps it off
// another site's POST. But when the console is served from another origin
// (FOX_WEB_ORIGIN), the cookie has to be SameSite=None to reach the API at all,
// and then any site the person visits could make their browser send a signed-in
// request. So the browser's own word on where a request came from is checked:
//
//   - Origin, which browsers send on every POST, PUT, PATCH and DELETE: it must
//     be this server itself or an origin the API allows (AllowedOrigin);
//   - "Origin: null" (a sandboxed frame, a file) is never trusted;
//   - with no Origin, Sec-Fetch-Site: cross-site is refused.
//
// A request with neither is not from a browser — curl, an SDK, a server — and
// carries an API key rather than an ambient cookie, so it passes. Reads (GET,
// HEAD, OPTIONS) are not checked: they change nothing, and CORS keeps their
// answers from another site's page.
func RefuseCrossSite(configured string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if changes(r.Method) && crossSite(configured, r) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"refused: this request came from a page on another site"}` + "\n"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func changes(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	}
	return true
}

// crossSite reports whether a browser sent r from a page that may not act here.
func crossSite(configured string, r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site")
	}
	if o == "null" {
		return true
	}
	return !sameOrigin(o, r) && !AllowedOrigin(configured, o)
}

// sameOrigin reports whether origin is the address r was sent to.
func sameOrigin(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return u.Scheme == scheme && strings.EqualFold(withPort(u.Host, scheme), withPort(r.Host, scheme))
}

// withPort spells out a scheme's default port, so "h" and "h:443" compare equal.
func withPort(host, scheme string) string {
	if strings.LastIndexByte(host, ':') > strings.LastIndexByte(host, ']') {
		return host
	}
	if scheme == "https" {
		return host + ":443"
	}
	return host + ":80"
}
