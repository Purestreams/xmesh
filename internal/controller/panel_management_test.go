package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/model"
)

func TestLoginRateLimitExpires(t *testing.T) {
	server, _ := testServer(t, nil)
	now := server.now()
	for range 5 {
		server.recordLoginFailure("192.0.2.1")
	}
	if !server.loginLimited("192.0.2.1") || server.loginLimited("192.0.2.2") {
		t.Fatal("rate limit not scoped to source IP")
	}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=wrong"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.RemoteAddr = "192.0.2.1:12345"
	recorder := httptest.NewRecorder()
	server.login(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("login endpoint did not enforce limit: %d", recorder.Code)
	}
	server.now = func() time.Time { return now.Add(16 * time.Minute) }
	if server.loginLimited("192.0.2.1") {
		t.Fatal("rate limit did not expire")
	}
}

func TestPanelRendersManagementAndDeleteImpact(t *testing.T) {
	server, _ := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Name: "Gateway", PublicHost: "edge.example", VMessPort: 8080, VMessPath: "/proxy"}
		s.Agents["a"] = model.Agent{ID: "a", Name: "Agent", AllowedCIDRs: []string{"0.0.0.0/0"}}
		s.Attachments["n"] = model.Attachment{ID: "n", GatewayID: "g", AgentID: "a"}
		s.Links["l"] = model.Link{ID: "l", Name: "Link", AttachmentID: "n", URL: "reality://edge.example:8443/tunnel"}
		s.Grants["r"] = model.Grant{ID: "r", AttachmentID: "n"}
		return nil
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.SignSession(server.cfg.sessionKey(), "admin", server.now().Add(time.Hour))})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("panel: %d %s", recorder.Code, recorder.Body.String())
	}
	for _, want := range []string{"Delete impact: 1 assignments, 1 Links, 1 grants", "Delete assignment", "Delete Link", "Delete grant", "Refresh status"} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("panel missing %q", want)
		}
	}
}
