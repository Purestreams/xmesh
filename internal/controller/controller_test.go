package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/model"
	"xmesh/internal/store"
)

func testServer(t *testing.T, populate func(*model.State) error) (*Server, *store.Store) {
	t.Helper()
	state, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if populate != nil {
		if err := state.Update(populate); err != nil {
			t.Fatal(err)
		}
	}
	passwordHash, err := auth.PasswordHash("a-strong-test-password")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{PublicURL: "https://panel.example", AdminUsername: "admin", AdminPasswordHash: passwordHash, SessionSecret: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), NodeOfflineAfterSeconds: 45}
	server, err := New(cfg, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	return server, state
}

func TestSubscriptionContainsOnlyPublishedAuthorizedNodes(t *testing.T) {
	state := model.NewState()
	state.Users["u1"] = model.User{ID: "u1", Enabled: true}
	state.Users["u2"] = model.User{ID: "u2", Enabled: true}
	for _, id := range []string{"g1", "g2"} {
		state.Gateways[id] = model.Gateway{ID: id, Name: id, PublicHost: id + ".example", VMessPort: 8080, VMessPath: "/proxy", Enabled: true}
	}
	for _, id := range []string{"a1", "a2"} {
		state.Agents[id] = model.Agent{ID: id, Name: id, Enabled: true}
	}
	for _, attachment := range []model.Attachment{{ID: "n11", GatewayID: "g1", AgentID: "a1", Enabled: true}, {ID: "n12", GatewayID: "g1", AgentID: "a2", Enabled: true}, {ID: "n21", GatewayID: "g2", AgentID: "a1", Enabled: true}, {ID: "n22", GatewayID: "g2", AgentID: "a2", Enabled: true}} {
		state.Attachments[attachment.ID] = attachment
	}
	state.Grants["r1"] = model.Grant{ID: "r1", UserID: "u1", AttachmentID: "n11", VMessUUID: "00000000-0000-4000-8000-000000000001", Enabled: true, Published: true}
	state.Grants["r2"] = model.Grant{ID: "r2", UserID: "u1", AttachmentID: "n22", VMessUUID: "00000000-0000-4000-8000-000000000002", Enabled: true, Published: true}
	state.Grants["hidden"] = model.Grant{ID: "hidden", UserID: "u2", AttachmentID: "n12", Enabled: true, Published: true}
	payload, err := BuildSubscription(state, "u1")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(plain), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d nodes: %s", len(lines), plain)
	}
	if !strings.Contains(lines[0], "vmess://") || lines[0] == lines[1] {
		t.Fatalf("invalid subscription %q", plain)
	}
}

func TestGatewayStatusPublishesGrantOnlyAfterXrayApplied(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Users["u"] = model.User{ID: "u", Enabled: true}
		s.Gateways["g"] = model.Gateway{ID: "g", Enabled: true, CredentialHash: auth.SecretHash("credential"), DesiredVersion: 2}
		s.Agents["a"] = model.Agent{ID: "a", Enabled: true}
		s.Attachments["n"] = model.Attachment{ID: "n", GatewayID: "g", AgentID: "a", Enabled: true}
		s.Links["l"] = model.Link{ID: "l", AttachmentID: "n"}
		s.Grants["r"] = model.Grant{ID: "r", UserID: "u", AttachmentID: "n", Enabled: true}
		return nil
	})
	report := StatusReport{Status: model.NodeStatus{Ready: true, XrayReady: true, AppliedVersion: 2}, Links: []model.LinkStatus{{LinkID: "l", Ready: true}}}
	b, _ := json.Marshal(report)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/status", bytes.NewReader(b))
	request.Header.Set("Authorization", "Bearer credential")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	snapshot := state.Snapshot()
	if !snapshot.Grants["r"].Published {
		t.Fatal("applied grant was not published")
	}
	linkStatus, ok := snapshot.LinkStatus["g/l"]
	if !ok || linkStatus.ReporterNodeID != "g" {
		t.Fatalf("missing reporter-scoped link status: %#v", snapshot.LinkStatus)
	}
}

func TestEnrollmentTokenIsSingleUse(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Enabled: true}
		s.Enrollments["e"] = model.Enrollment{ID: "e", NodeID: "g", Role: model.RoleGateway, TokenHash: auth.SecretHash("one-time"), ExpiresAt: now.Add(time.Hour)}
		return nil
	})
	call := func() int {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/enroll", strings.NewReader(`{"token":"one-time"}`))
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder.Code
	}
	if got := call(); got != http.StatusCreated {
		t.Fatalf("first enrollment status %d", got)
	}
	if got := call(); got != http.StatusUnauthorized {
		t.Fatalf("reused enrollment status %d", got)
	}
	if state.Snapshot().Gateways["g"].CredentialHash == "" {
		t.Fatal("node credential was not stored")
	}
}

func TestPanelRendersPopulatedRelationshipTables(t *testing.T) {
	server, _ := testServer(t, func(s *model.State) error {
		s.Users["u"] = model.User{ID: "u", Name: "User", Enabled: true}
		s.Gateways["g"] = model.Gateway{ID: "g", Name: "Gateway", Enabled: true}
		s.Agents["a"] = model.Agent{ID: "a", Name: "Agent", Enabled: true}
		s.Attachments["n"] = model.Attachment{ID: "n", GatewayID: "g", AgentID: "a", Enabled: true}
		s.Links["l"] = model.Link{ID: "l", Name: "Link", AttachmentID: "n", Enabled: true}
		s.Grants["r"] = model.Grant{ID: "r", UserID: "u", AttachmentID: "n", Enabled: true}
		return nil
	})
	expires := server.now().Add(time.Hour)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.SignSession(server.cfg.sessionKey(), "admin", expires)})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, want := range []string{"Gateway", "Agent", "User", "Link"} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("panel missing %q", want)
		}
	}
}
