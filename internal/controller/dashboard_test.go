package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/model"
	"xmesh/internal/store"
)

func dashboardRequest(t *testing.T, server *Server) dashboardData {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.SignSession(server.cfg.sessionKey(), "admin", server.now().Add(time.Hour))})
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("dashboard response: %d %s", w.Code, w.Body.String())
	}
	for _, secret := range []string{"private-secret", "subscription-secret", "tunnel-secret", "credential-secret"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("dashboard exposed %s", secret)
		}
	}
	var data dashboardData
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDashboardAuthenticationExpiryAndReadiness(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Enabled: true, DesiredVersion: 2, RealityPrivateKey: "private-secret", CredentialHash: "credential-secret"}
		s.Agents["a"] = model.Agent{ID: "a", Enabled: true, DesiredVersion: 2}
		s.Users["u"] = model.User{ID: "u", SubscriptionToken: "subscription-secret"}
		s.Attachments["r"] = model.Attachment{ID: "r", GatewayID: "g", AgentID: "a", Enabled: true}
		s.Links["l"] = model.Link{ID: "l", AttachmentID: "r", Enabled: true, TunnelTokenHash: "tunnel-secret"}
		for _, id := range []string{"g", "a"} {
			s.NodeStatus[id] = model.NodeStatus{NodeID: id, Online: true, Ready: true, XrayReady: true, AppliedVersion: 2, LastSeen: now}
			s.LinkStatus[id+"/l"] = model.LinkStatus{LinkID: "l", ReporterNodeID: id, Online: true, Ready: true, LastSeen: now}
		}
		return nil
	})
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/admin/dashboard", nil))
	if w.Code != http.StatusSeeOther {
		t.Fatal("dashboard accepted unauthenticated request")
	}
	if got := dashboardRequest(t, server).Routes[0].State; got != "ready" {
		t.Fatalf("healthy route: %s", got)
	}
	if err := state.Update(func(s *model.State) error {
		st := s.LinkStatus["a/l"]
		st.Ready = false
		s.LinkStatus["a/l"] = st
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := dashboardRequest(t, server).Routes[0].State; got != "pending" {
		t.Fatalf("one-sided Link reported ready: %s", got)
	}
	server.now = func() time.Time { return now.Add(time.Minute) }
	if got := dashboardRequest(t, server).Routes[0].State; got != "offline" {
		t.Fatalf("expired heartbeat: %s", got)
	}
	if !state.Snapshot().NodeStatus["g"].Online {
		t.Fatal("read-only dashboard mutated stored status")
	}
}

func TestLinkHistorySamplingRetentionAndRestart(t *testing.T) {
	state := model.NewState()
	state.Links["l"] = model.Link{ID: "l"}
	now := time.Unix(1_800_000_000, 0)
	for i := range 400 {
		sampleLinkHistory(&state, model.LinkStatus{LinkID: "l", Generation: 1, UploadBytes: uint64(i * 1000), Online: true, Ready: true}, now.Add(time.Duration(i)*5*time.Minute))
	}
	if got := len(state.LinkHistory["l"]); got != historyLimit {
		t.Fatalf("retention sample count %d", got)
	}
	last := state.LinkHistory["l"][historyLimit-1].At
	sampleLinkHistory(&state, model.LinkStatus{LinkID: "l"}, last.Add(time.Second))
	if samples := state.LinkHistory["l"]; !samples[len(samples)-1].At.Equal(last) {
		t.Fatal("sample interval ignored")
	}
	sampleLinkHistory(&state, model.LinkStatus{LinkID: "l", Generation: 2, UploadBytes: 1}, last.Add(historyInterval))
	if state.LinkHistory["l"][historyLimit-1].Generation != 2 {
		t.Fatal("restart boundary lost")
	}
	state.LinkHistory["deleted"] = []model.LinkSample{{At: now}}
	sampleLinkHistory(&state, model.LinkStatus{LinkID: "l"}, last.Add(2*historyInterval))
	if _, ok := state.LinkHistory["deleted"]; ok {
		t.Fatal("deleted Link history not pruned")
	}
}

func TestOperationsPersistBoundedAndExcludeFormSecrets(t *testing.T) {
	server, state := testServer(t, nil)
	for i := range 105 {
		if err := state.Update(func(s *model.State) error {
			s.Operations = append(s.Operations, model.Operation{Action: "old"})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		_ = i
	}
	cookie := auth.SignSession(server.cfg.sessionKey(), "admin", server.now().Add(time.Hour))
	values := url.Values{"csrf": {auth.Derive(server.cfg.sessionKey(), "csrf", cookie)}, "name": {"Audit user"}, "password": {"do-not-record-this"}}
	r := httptest.NewRequest("POST", "/admin/users", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("create user: %d %s", w.Code, w.Body.String())
	}
	operations := state.Snapshot().Operations
	if len(operations) != 100 || operations[99].Action != "users" || operations[99].Status != 303 {
		t.Fatalf("operations: %#v", operations)
	}
	encoded, _ := json.Marshal(operations)
	if strings.Contains(string(encoded), "do-not-record-this") {
		t.Fatal("form secret recorded")
	}
	// History and operation records survive Store reopen, including old state files.
	path := t.TempDir() + "/state.json"
	persisted, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := persisted.Update(func(s *model.State) error {
		s.Operations = operations
		s.LinkHistory = map[string][]model.LinkSample{"l": {{At: server.now(), UploadBytes: 42}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Snapshot().Operations) != 100 || reopened.Snapshot().LinkHistory["l"][0].UploadBytes != 42 {
		t.Fatal("history not persisted")
	}
}

func TestRejectedStatusDoesNotSampleAndAgentDoesNotDoubleCount(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Enabled: true, CredentialHash: auth.SecretHash("gateway-credential")}
		s.Agents["a"] = model.Agent{ID: "a", Enabled: true, CredentialHash: auth.SecretHash("agent-credential")}
		s.Attachments["r"] = model.Attachment{ID: "r", GatewayID: "g", AgentID: "a", Enabled: true}
		s.Links["l"] = model.Link{ID: "l", AttachmentID: "r", Enabled: true}
		return nil
	})
	post := func(credential, link string) int {
		r := httptest.NewRequest("POST", "/api/v1/status", strings.NewReader(`{"status":{"online":true},"links":[{"link_id":"`+link+`","upload_bytes":123}]}`))
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if post("agent-credential", "l") != 204 || len(state.Snapshot().LinkHistory) != 0 {
		t.Fatal("Agent report sampled twice")
	}
	if post("gateway-credential", "other") == 204 || len(state.Snapshot().LinkHistory) != 0 {
		t.Fatal("rejected report sampled")
	}
	if post("gateway-credential", "l") != 204 || len(state.Snapshot().LinkHistory["l"]) != 1 {
		t.Fatal("Gateway report not sampled")
	}
}
