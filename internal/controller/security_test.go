package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/model"
	"xmesh/internal/store"
)

func securityState(s *model.State) error {
	s.Users["u"] = model.User{ID: "u", Enabled: true, SubscriptionToken: "old-subscription-token"}
	s.Users["other"] = model.User{ID: "other", Enabled: true}
	s.Gateways["g"] = model.Gateway{ID: "g", Enabled: true, CredentialHash: auth.SecretHash("gateway-secret"), DesiredVersion: 1, PublicHost: "example.invalid", VMessPort: 8080, VMessPath: "/proxy"}
	s.Agents["a"] = model.Agent{ID: "a", Enabled: true, CredentialHash: auth.SecretHash("agent-secret"), AllowedCIDRs: []string{"127.0.0.0/8"}}
	s.Attachments["route"] = model.Attachment{ID: "route", GatewayID: "g", AgentID: "a", Enabled: true}
	s.Links["link"] = model.Link{ID: "link", AttachmentID: "route", Enabled: true}
	s.Grants["grant"] = model.Grant{ID: "grant", UserID: "u", AttachmentID: "route", Enabled: true, Published: true, VMessUUID: "00000000-0000-4000-8000-000000000001", SOCKSUsername: "grant", SOCKSPassword: "socks-secret"}
	s.Grants["other"] = model.Grant{ID: "other", UserID: "other", AttachmentID: "route", Enabled: true, Published: true, VMessUUID: "other-uuid", SOCKSPassword: "other-secret"}
	return nil
}

func securityAdminPost(s *Server, path, cookie string) *httptest.ResponseRecorder {
	form := url.Values{"csrf": {auth.Derive(s.cfg.sessionKey(), "csrf", cookie)}}
	r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestLogoutRevokesCookieAcrossRestartAndPasswordRotation(t *testing.T) {
	s, _ := testServer(t, nil)
	statePath := filepath.Join(t.TempDir(), "sessions.json")
	state, err := store.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	s.store = state
	cookie := auth.SignSession(s.cfg.adminSessionKey(), "admin", s.now().Add(12*time.Hour))
	request := httptest.NewRequest("POST", "/logout", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	rejected := httptest.NewRecorder()
	s.Handler().ServeHTTP(rejected, request)
	if rejected.Code != http.StatusForbidden {
		t.Fatal("logout without CSRF was accepted", rejected.Code)
	}
	if w := securityAdminPost(s, "/logout", cookie); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	r := httptest.NewRequest("GET", "/admin/dashboard", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	if s.isAdmin(r) {
		t.Fatal("logged-out cookie still authorized")
	}
	// Reopen the actual persisted state rather than relying on an in-memory denylist.
	state.View(func(v *model.State) {
		if len(v.RevokedAdminSessions) != 1 {
			t.Error("revocation missing")
		}
	})
	reopened, err := store.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(s.cfg, reopened, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = s.now
	if restarted.isAdmin(r) {
		t.Fatal("restart lost revocation")
	}
	fresh := auth.SignSession(s.cfg.adminSessionKey(), "admin", s.now().Add(12*time.Hour))
	r.AddCookie(&http.Cookie{Name: "irrelevant", Value: "test"})
	r.Header.Set("Cookie", sessionCookie+"="+fresh)
	if !s.isAdmin(r) {
		t.Fatal("fresh session was rejected")
	}
	s.cfg.AdminPasswordHash = "a-different-password-hash"
	if s.isAdmin(r) {
		t.Fatal("password change did not revoke old session")
	}
}

func TestSubscriptionResetRotatesOnlyUsersAccessCredentials(t *testing.T) {
	s, state := testServer(t, securityState)
	before := state.Snapshot()
	cookie := auth.SignSession(s.cfg.adminSessionKey(), "admin", s.now().Add(time.Hour))
	if w := securityAdminPost(s, "/admin/users/u/reset-subscription", cookie); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	after := state.Snapshot()
	if after.Users["u"].SubscriptionToken == before.Users["u"].SubscriptionToken || after.Grants["grant"].VMessUUID == before.Grants["grant"].VMessUUID || after.Grants["grant"].SOCKSPassword == before.Grants["grant"].SOCKSPassword {
		t.Fatal("compromised credentials retained")
	}
	if after.Grants["grant"].Published || after.Gateways["g"].DesiredVersion != 2 {
		t.Fatal("rotation did not wait for Gateway acknowledgement")
	}
	if after.Grants["other"] != before.Grants["other"] {
		t.Fatal("another user's credentials changed")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/subscription/old-subscription-token", nil))
	if w.Code != 404 {
		t.Fatal("old subscription URL accepted")
	}
}

func TestDisabledNodesReceiveEmptyConfigButCannotReport(t *testing.T) {
	s, state := testServer(t, securityState)
	if err := state.Update(func(v *model.State) error {
		g := v.Gateways["g"]
		g.Enabled = false
		v.Gateways["g"] = g
		a := v.Agents["a"]
		a.Enabled = false
		v.Agents["a"] = a
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"gateway-secret", "agent-secret"} {
		r := httptest.NewRequest("GET", "/api/v1/config", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), "socks-secret") || strings.Contains(w.Body.String(), `"enabled":true`) || strings.Contains(w.Body.String(), `"id":"link"`) {
			t.Fatal(w.Code, w.Body.String())
		}
		r = httptest.NewRequest("POST", "/api/v1/status", strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("disabled status accepted", w.Code)
		}
	}
}

func TestAgentConfigScopesGrantsAndExcludesDisabledGateway(t *testing.T) {
	s, state := testServer(t, securityState)
	call := func() AgentConfig {
		r := httptest.NewRequest("GET", "/api/v1/config", nil)
		r.Header.Set("Authorization", "Bearer agent-secret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		var config AgentConfig
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil {
			t.Fatal(err)
		}
		return config
	}
	config := call()
	if len(config.Links) != 1 || len(config.Links[0].GrantIDs) != 2 {
		t.Fatal("missing per-link authorization", config)
	}
	if err := state.Update(func(v *model.State) error { g := v.Gateways["g"]; g.Enabled = false; v.Gateways["g"] = g; return nil }); err != nil {
		t.Fatal(err)
	}
	config = call()
	if len(config.Links) != 0 || len(config.GrantIDs) != 0 {
		t.Fatal("disabled Gateway remains authorized", config)
	}
}
