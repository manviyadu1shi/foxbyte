// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefuseCrossSite(t *testing.T) {
	const conf = "https://console.example.com"
	h := RefuseCrossSite(conf)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, c := range []struct {
		name, method, host, origin, fetchSite string
		tls                                   bool
		want                                  int
	}{
		{"same origin", "POST", "db.example.com", "https://db.example.com", "", true, 204},
		{"same origin, default port spelled out", "POST", "db.example.com:443", "https://db.example.com", "", true, 204},
		{"the configured console", "DELETE", "db.example.com", conf, "", true, 204},
		{"another site", "POST", "db.example.com", "https://evil.example", "", true, 403},
		{"another site, PUT", "PUT", "db.example.com", "https://evil.example", "", true, 403},
		{"same host, other scheme", "POST", "db.example.com", "http://db.example.com", "", true, 403},
		{"a null origin", "POST", "db.example.com", "null", "", true, 403},
		{"no Origin, but the browser says cross-site", "POST", "db.example.com", "", "cross-site", true, 403},
		{"no Origin, browser says same-origin", "POST", "db.example.com", "", "same-origin", true, 204},
		{"not a browser (curl, an SDK)", "POST", "db.example.com", "", "", true, 204},
		{"a read from anywhere", "GET", "db.example.com", "https://evil.example", "", true, 204},
		{"local console on another loopback name", "POST", "localhost:8080", "https://127.0.0.1:8080", "", true, 403},
	} {
		r := httptest.NewRequest(c.method, "/api/branches", nil)
		r.Host = c.host
		if c.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.fetchSite != "" {
			r.Header.Set("Sec-Fetch-Site", c.fetchSite)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, w.Code, c.want)
		}
	}
}

// A local install: the console at https://localhost:8080 may also be reached as
// 127.0.0.1 or [::1] on the same port (AllowedOrigin's rule), and nothing else.
func TestRefuseCrossSiteLocal(t *testing.T) {
	h := RefuseCrossSite("https://localhost:8080")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for origin, want := range map[string]int{
		"https://localhost:8080": 204,
		"https://127.0.0.1:8080": 204,
		"http://localhost:5173":  403,
		"https://evil.example":   403,
	} {
		r := httptest.NewRequest("POST", "/auth/login", nil)
		r.Host = "localhost:8080"
		r.TLS = &tls.ConnectionState{}
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s: %d, want %d", origin, w.Code, want)
		}
	}
}
