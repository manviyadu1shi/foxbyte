// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An agent key opens its owner's branches as an agent, and is refused wherever
// it would be its owner again. The Gateway half (logging it in as an agent
// role) is asserted in the integration suite, against a real connection.
func TestAgentKey(t *testing.T) {
	s := testStore(t)
	u, err := s.CreateUser("a@x.com", "password1")
	if err != nil {
		t.Fatal(err)
	}
	key, info, err := s.CreateAgentKey(u.ID, "cursor")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("is an unscoped key of its own kind", func(t *testing.T) {
		if !strings.HasPrefix(key, KeyPrefix) {
			t.Errorf("an agent key is the Gateway's password like any other, so begins %q: %q", KeyPrefix, key)
		}
		gotU, scope, kind, ok := s.VerifyKeyKind(key)
		if !ok || gotU.ID != u.ID || scope != "" || kind != KindAgent {
			t.Fatalf("VerifyKeyKind = (%d, %q, %q, %v), want (%d, \"\", %q, true)", gotU.ID, scope, kind, ok, u.ID, KindAgent)
		}
		if info.Kind != KindAgent || info.Scope != "" {
			t.Errorf("info = %+v", info)
		}
	})

	t.Run("is refused at the control plane", func(t *testing.T) {
		// The console's SQL runs as a person: an agent key there would be its
		// owner again.
		if _, ok := s.userFromRequest(bearer(key)); ok {
			t.Error("an agent key authenticated against the control plane")
		}
	})

	t.Run("is accepted by the Agent API", func(t *testing.T) {
		if code := authnCode(s.AuthnAgents, bearer(key)); code != http.StatusOK {
			t.Errorf("AuthnAgents answered %d to an agent key", code)
		}
	})

	t.Run("and the Agent API still takes what it always took, and refuses the rest", func(t *testing.T) {
		account, _, _ := s.CreateAPIKey(u.ID, "mine")
		scoped, _, _ := s.CreateScopedAPIKey(u.ID, "b", "agent-b")
		rtk, _, _ := s.CreateRealtimeKey(u.ID, "rt", "main")
		for _, c := range []struct {
			what, key string
			want      int
		}{
			{"an account key", account, http.StatusOK},
			{"a scoped key", scoped, http.StatusUnauthorized},
			{"a realtime key", rtk, http.StatusUnauthorized},
			{"no key", "", http.StatusUnauthorized},
		} {
			if code := authnCode(s.AuthnAgents, bearer(c.key)); code != c.want {
				t.Errorf("AuthnAgents answered %d to %s, want %d", code, c.what, c.want)
			}
		}
	})

	t.Run("an account key is unaffected", func(t *testing.T) {
		account, _, _ := s.CreateAPIKey(u.ID, "mine2")
		if _, ok := s.userFromRequest(bearer(account)); !ok {
			t.Error("an account key no longer reaches the control plane")
		}
	})

	t.Run("is listed as an agent key", func(t *testing.T) {
		keys, _ := s.ListKeys(u.ID)
		for _, k := range keys {
			if k.ID == info.ID {
				if k.Kind != KindAgent {
					t.Errorf("listed with kind %q", k.Kind)
				}
				return
			}
		}
		t.Error("the agent key is not listed")
	})
}

// POST /api/keys mints an agent key when asked, an account key by default, and
// nothing else: a realtime key is minted for a branch, by its manager.
func TestCreateKeyKinds(t *testing.T) {
	s := testStore(t)
	u, err := s.CreateUser("a@x.com", "password1")
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string) (int, KeyInfo) {
		r := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), userKey, u))
		w := httptest.NewRecorder()
		s.handleCreateKey(w, r)
		var out struct{ Info KeyInfo }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Info
	}
	if code, info := post(`{"name":"ci"}`); code != http.StatusCreated || info.Kind != "" {
		t.Errorf("default: %d, kind %q", code, info.Kind)
	}
	if code, info := post(`{"name":"bot","kind":"agent"}`); code != http.StatusCreated || info.Kind != KindAgent {
		t.Errorf("agent: %d, kind %q", code, info.Kind)
	}
	if code, _ := post(`{"name":"x","kind":"realtime"}`); code != http.StatusBadRequest {
		t.Errorf("realtime through /api/keys: %d, want 400", code)
	}
}

func authnCode(mw func(http.Handler) http.Handler, r *http.Request) int {
	w := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(w, r)
	return w.Code
}
