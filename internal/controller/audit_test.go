package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/audit"
	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
)

func postAudit(t *testing.T, s *Server, credential string, batch audit.Batch) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/v1/audit", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+credential)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestAuditCombinesOneUserAcrossAgents(t *testing.T) {
	s, state := testServer(t, func(st *model.State) error {
		securityState(st)
		u := st.Users["u"]
		u.AuditEnabled = true
		u.AuditGeneration = 9
		st.Users["u"] = u
		st.Agents["b"] = model.Agent{ID: "b", Name: "second exit", Enabled: true, CredentialHash: auth.SecretHash("b-secret")}
		st.Attachments["route-b"] = model.Attachment{ID: "route-b", GatewayID: "g", AgentID: "b", Enabled: true}
		st.Links["link-b"] = model.Link{ID: "link-b", AttachmentID: "route-b", Enabled: true}
		st.Grants["grant-b"] = model.Grant{ID: "grant-b", UserID: "u", AttachmentID: "route-b", Enabled: true}
		return nil
	})
	for _, source := range []struct{ grant, link, credential string }{{"grant", "link", "agent-secret"}, {"grant-b", "link-b", "b-secret"}} {
		var events []audit.Event
		for i := 0; i < 60; i++ {
			events = append(events, audit.Event{At: s.now(), GrantID: source.grant, LinkID: source.link, Generation: 9, ConnectionID: strconv.Itoa(i), Kind: "tcp", Result: "attempt", Host: fmt.Sprintf("target-%d.example", i%30), Port: 1000 + i%20})
		}
		if w := postAudit(t, s, source.credential, audit.Batch{Events: events}); w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	view, err := s.audits.Read("u", audit.LimitBytes(0), audit.Filter{}, s.now())
	if err != nil || len(view.Events) != 120 || view.Summary.Attempts != 120 || view.Summary.Targets != 30 || view.Summary.Ports != 20 {
		t.Fatalf("not grouped by user: %v %+v", err, view.Summary)
	}
	data, err := os.ReadFile(state.Path())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("target-")) {
		t.Fatal("audit events leaked into control-plane state")
	}
}

func TestUserAuditBrowserFixture(t *testing.T) {
	address := os.Getenv("XMESH_AUDIT_BROWSER_ADDR")
	if address == "" {
		t.Skip("browser fixture disabled")
	}
	s, state := testServer(t, securityState)
	s.cfg.PublicURL = "http://" + address // Loopback fixture uses HTTP, including API requests.
	if err := state.Update(func(st *model.State) error {
		u := st.Users["u"]
		u.Name = "审计用户"
		u.AuditEnabled = true
		u.AuditGeneration = 1
		st.Users["u"] = u
		other := st.Users["other"]
		other.Name = "默认关闭用户"
		st.Users["other"] = other
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var events []audit.Event
	for i := 0; i < 120; i++ {
		events = append(events, audit.Event{At: s.now(), UserID: "u", GrantID: "grant", ConnectionID: strconv.Itoa(i), Kind: "tcp", Result: "attempt", Host: fmt.Sprintf("target-%d.example", i%30), Port: 1000 + i%20})
	}
	events = append(events, audit.Event{At: s.now(), UserID: "u", UserName: "审计用户", GrantID: "grant", ConnectionID: "fixture-connection", Kind: "tcp", Result: "closed", Host: "audited.example", IP: "203.0.113.10", Port: 443, ExitName: "测试出口", DurationMS: 1000, UploadBytes: 123, DownloadBytes: 256})
	if err := s.audits.Append("u", audit.LimitBytes(0), events, 3, s.now()); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Log("audit browser fixture ready", address)
	if err := http.Serve(listener, s.Handler()); err != nil {
		t.Log(err)
	}
}

func auditAdminPost(t *testing.T, s *Server, path, cookie string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	values.Set("csrf", auth.Derive(s.cfg.sessionKey(), "csrf", cookie))
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestUserAuditDefaultOffOwnershipAndGeneration(t *testing.T) {
	s, state := testServer(t, securityState)
	e := audit.Event{At: s.now(), GrantID: "grant", ConnectionID: "connection", LinkID: "link", Kind: "tcp", Result: "attempt", Host: "example.com", Port: 443}
	if w := postAudit(t, s, "agent-secret", audit.Batch{Events: []audit.Event{e}}); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(state.Path()), "audit", "u")); !os.IsNotExist(err) {
		t.Fatal("default-off user created audit files")
	}
	cookie := auth.SignSession(s.cfg.adminSessionKey(), s.cfg.AdminUsername, s.now().Add(time.Hour))
	if w := auditAdminPost(t, s, "/admin/users/u/audit", cookie, url.Values{"enabled": {"true"}}); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	user := state.Snapshot().Users["u"]
	if !user.AuditEnabled || user.AuditLimitMB != 20 || user.AuditGeneration == 0 {
		t.Fatalf("wrong settings: %+v", user)
	}
	r := httptest.NewRequest("GET", "/api/v1/config", nil)
	r.Header.Set("Authorization", "Bearer agent-secret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var cfg AgentConfig
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AuditGrants["grant"].Generation != user.AuditGeneration || len(cfg.AuditGrants) != 1 {
		t.Fatalf("unscoped audit policy: %+v", cfg.AuditGrants)
	}
	e.Generation = user.AuditGeneration
	if err := state.Update(func(st *model.State) error {
		u := st.Users["u"]
		u.Name = strings.Repeat("长", 10000)
		st.Users["u"] = u
		a := st.Agents["a"]
		a.Name = strings.Repeat("出口", 10000)
		st.Agents["a"] = a
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	spoof := e
	spoof.UserID = "other"
	if w := postAudit(t, s, "agent-secret", audit.Batch{Events: []audit.Event{e, spoof}}); w.Code != 403 {
		t.Fatal("identity spoof accepted", w.Code)
	}
	view, err := s.audits.Read("u", audit.LimitBytes(0), audit.Filter{}, s.now())
	if err != nil || len(view.Events) != 0 {
		t.Fatal("invalid batch partially persisted")
	}
	if w := postAudit(t, s, "gateway-secret", audit.Batch{Events: []audit.Event{e}}); w.Code != 403 {
		t.Fatal("gateway accepted as agent")
	}
	if w := postAudit(t, s, "agent-secret", audit.Batch{Events: []audit.Event{e}}); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	view, err = s.audits.Read("u", audit.LimitBytes(0), audit.Filter{}, s.now())
	if err != nil || len(view.Events) != 1 || view.Events[0].UserID != "u" || view.Events[0].AgentID != "a" {
		t.Fatal("trusted attribution failed", err, view)
	}
	if len(view.Events[0].UserName) > 256 || len(view.Events[0].ExitName) > 256 {
		t.Fatal("labels exceeded bounded audit metadata")
	}
	if w := auditAdminPost(t, s, "/admin/users/u/audit/clear", cookie, url.Values{}); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := postAudit(t, s, "agent-secret", audit.Batch{Events: []audit.Event{e}}); w.Code != 204 {
		t.Fatal(w.Code)
	}
	view, err = s.audits.Read("u", audit.LimitBytes(0), audit.Filter{}, s.now())
	if err != nil || len(view.Events) != 0 {
		t.Fatal("queued event resurrected cleared log")
	}
	if w := auditAdminPost(t, s, "/admin/users/u/audit", cookie, url.Values{}); w.Code != 303 {
		t.Fatal(w.Code)
	}
	if state.Snapshot().Users["u"].AuditEnabled {
		t.Fatal("audit did not disable")
	}
}

func TestAuditEndpointsRequireAdminAndCSRFAndEscapeMetadata(t *testing.T) {
	s, _ := testServer(t, securityState)
	for _, path := range []string{"/admin/users/u/audit", "/admin/users/u/audit/data", "/admin/users/u/audit/download"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 303 {
			t.Fatalf("unprotected %s", path)
		}
	}
	cookie := auth.SignSession(s.cfg.adminSessionKey(), s.cfg.AdminUsername, s.now().Add(time.Hour))
	r := httptest.NewRequest("POST", "/admin/users/u/audit", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("CSRF not required")
	}
	e := audit.Event{At: s.now(), UserID: "u", Host: "<script>alert(1)</script>", Kind: "tcp", Result: "failed", Port: 443, Reason: "<img src=x onerror=alert(1)>"}
	if err := s.audits.Append("u", audit.LimitBytes(0), []audit.Event{e}, 0, s.now()); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "/admin/users/u/audit", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "<script>alert(1)</script>") || strings.Contains(w.Body.String(), "<img src=x onerror=alert(1)>") {
		t.Fatal("unsafe audit rendering", w.Code)
	}
	if !strings.Contains(w.Body.String(), "&lt;script&gt;") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing escaping/cache control")
	}
	r = httptest.NewRequest("GET", "/admin/users/u/audit/data?port=bad", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("invalid filter accepted")
	}
}
