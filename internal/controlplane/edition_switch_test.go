// SPDX-License-Identifier: AGPL-3.0-or-later

package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thefoxbyte/foxbyte/internal/auth"
	"github.com/thefoxbyte/foxbyte/internal/host"
	"github.com/thefoxbyte/foxbyte/internal/update"
)

// switchCall sends one request to the edition-switch routes as u.
func switchCall(t *testing.T, s *auth.Store, u auth.User, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	registerEditionSwitch(mux, s)
	key, _, err := s.CreateAPIKey(u.ID, "t")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "/api/edition/switch", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	s.Authn(mux).ServeHTTP(rec, req)
	return rec
}

// stubSwitch records what would have been started instead of starting it.
func stubSwitch(t *testing.T, result error) *[]string {
	t.Helper()
	var calls []string
	old := startEditionSwitch
	startEditionSwitch = func(ed, by string) error { calls = append(calls, ed+" by "+by); return result }
	t.Cleanup(func() { startEditionSwitch = old })
	return &calls
}

func TestSwitchingEditionNeedsAnAdmin(t *testing.T) {
	s, _, alice, _ := withACL(t)
	calls := stubSwitch(t, nil)
	if rec := switchCall(t, s, alice, "POST", `{"edition":"enterprise","accept_license":true}`); rec.Code != 403 {
		t.Fatalf("an ordinary account got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := switchCall(t, s, alice, "GET", ""); rec.Code != 403 {
		t.Fatalf("an ordinary account read the switch: %d", rec.Code)
	}
	if len(*calls) != 0 {
		t.Fatalf("a switch was started: %v", *calls)
	}
}

// Enterprise is another licence: not accepted, nothing starts.
func TestSwitchingToEnterpriseNeedsTheLicenceAccepted(t *testing.T) {
	s, admin, _, _ := withACL(t)
	calls := stubSwitch(t, nil)
	rec := switchCall(t, s, admin, "POST", `{"edition":"enterprise"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), host.EnterpriseLicenseURL()) {
		t.Fatalf("got %d %s, want 400 naming the licence", rec.Code, rec.Body.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("a switch was started without the licence accepted: %v", *calls)
	}
}

func TestAnAdminStartsTheSwitchAndItIsRecorded(t *testing.T) {
	s, admin, _, _ := withACL(t)
	calls := stubSwitch(t, nil)
	rec := switchCall(t, s, admin, "POST", `{"edition":"enterprise","accept_license":true}`)
	if rec.Code != 202 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 || (*calls)[0] != "enterprise by "+admin.Email {
		t.Fatalf("started %v", *calls)
	}
	evs, err := s.RecentSecurityEvents(20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		if e.Kind == auth.EvEditionSwitch && e.Actor == admin.Email && strings.Contains(e.Detail, "Enterprise License accepted") {
			found = true
		}
	}
	if !found {
		t.Errorf("the switch is not in the security log: %+v", evs)
	}
}

func TestARefusedSwitchSaysWhy(t *testing.T) {
	s, admin, _, _ := withACL(t)
	stubSwitch(t, host.ErrSwitchRunning)
	if rec := switchCall(t, s, admin, "POST", `{"edition":"enterprise","accept_license":true}`); rec.Code != 409 {
		t.Fatalf("a second switch got %d, want 409", rec.Code)
	}
}

// The public answer is the edition, and nothing else.
func TestEditionIsPublicAndSaysOnlyTheEdition(t *testing.T) {
	mux := http.NewServeMux()
	registerEditionPublic(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/edition", nil))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(out) != 1 || out["edition"] != update.ThisEdition() {
		t.Errorf("got %v, want only the edition", out)
	}
}
