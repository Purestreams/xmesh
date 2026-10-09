package controller

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/model"
	"xmesh/internal/store"
)

func monitorFixture(now time.Time) func(*model.State) error {
	return func(state *model.State) error {
		state.Monitor = model.MonitorSettings{Enabled: true, Nodes: map[string]string{"private-gw": "香港入口", "private-agent": "东京出口", "private-upstream": "外部出口"}, Links: map[string]string{"private-link": "主链路", "external:private-external-route": "备用出口"}}
		state.Gateways["private-gw"] = model.Gateway{ID: "private-gw", Name: "192.0.2.42", PublicHost: "private-host.example", RealityTarget: "private-target.example:443", RealityPrivateKey: "private-key", CredentialHash: "private-credential", Enabled: true, DesiredVersion: 2}
		state.Agents["private-agent"] = model.Agent{ID: "private-agent", Name: "private-agent-name", AllowedCIDRs: []string{"192.168.0.0/16"}, Enabled: true, DesiredVersion: 2}
		state.Upstreams["private-upstream"] = model.VMessUpstream{ID: "private-upstream", Name: "private-upstream-name", Enabled: true, Endpoint: model.VMessEndpoint{Address: "private-upstream.example", UUID: "private-uuid"}, SubscriptionURL: "https://private-subscription.example/secret", LastError: "private-error 203.0.113.5"}
		state.Attachments["private-route"] = model.Attachment{ID: "private-route", GatewayID: "private-gw", AgentID: "private-agent", Enabled: true}
		state.Attachments["private-external-route"] = model.Attachment{ID: "private-external-route", GatewayID: "private-gw", UpstreamID: "private-upstream", Enabled: true}
		state.Links["private-link"] = model.Link{ID: "private-link", AttachmentID: "private-route", Name: "private-link-name", URL: "reality://192.0.2.42:8443/private-tunnel", TunnelTokenHash: "private-tunnel-token", Enabled: true}
		for _, id := range []string{"private-gw", "private-agent"} {
			state.NodeStatus[id] = model.NodeStatus{NodeID: id, Online: true, Ready: true, XrayReady: true, LastSeen: now, AppliedVersion: 2, BinaryVersion: "private-version", InstanceID: "private-instance", LastError: "private-error 203.0.113.5", UploadBytes: 123456789}
			state.LinkStatus[id+"/private-link"] = model.LinkStatus{LinkID: "private-link", ReporterNodeID: id, Online: true, Ready: true, LastSeen: now, LastSuccess: now, RTTMillis: 52.34, LastError: "private-error 203.0.113.5", UploadBytes: 123456789}
		}
		state.Users["private-user"] = model.User{ID: "private-user", Name: "private-user-name", SubscriptionToken: "private-user-token"}
		state.Grants["private-grant"] = model.Grant{ID: "private-grant", UserID: "private-user", VMessUUID: "private-grant-uuid", SOCKSPassword: "private-password"}
		state.LinkHistory = map[string][]model.LinkSample{"private-link": {
			{At: now.Add(-25 * time.Hour), Ready: true, RTTMillis: 100},
			{At: now.Add(-10 * time.Minute), Ready: true, RTTMillis: 40, InstanceID: "private-instance", UploadBytes: 123456789},
			{At: now.Add(-5 * time.Minute), Ready: false, RTTMillis: 80},
			{At: now, Ready: true, RTTMillis: 52.34},
			{At: now.Add(time.Hour), Ready: true, RTTMillis: 100},
		}}
		return nil
	}
}

func monitorRequest(server *Server, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func readMonitor(t *testing.T, server *Server) monitorPayload {
	t.Helper()
	w := monitorRequest(server, "/api/public/monitor")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("monitor: %d %s", w.Code, w.Body.String())
	}
	var payload monitorPayload
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestMonitorAnonymousAllowlistAndAuthentication(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	server, state := testServer(t, monitorFixture(now))
	before := state.Snapshot()
	for _, path := range []string{"/monitor", "/api/public/monitor", "/assets/monitor.js", "/assets/monitor.css"} {
		w := monitorRequest(server, path)
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		for _, secret := range []string{"private-", "192.0.2.42", "203.0.113.5", "192.168.0.0", "123456789"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("%s exposed %q", path, secret)
			}
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("missing security headers")
		}
	}
	for _, path := range []string{"/", "/admin/dashboard", "/admin/monitor"} {
		w := monitorRequest(server, path)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
			t.Fatalf("management bypass at %s: %d", path, w.Code)
		}
	}
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, httptest.NewRequest(method, "/api/public/monitor", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("public mutation accepted: %s %d", method, w.Code)
		}
	}
	w := monitorRequest(server, "/api/public/monitor")
	var data map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || len(data) != 3 || data["at"] == nil || data["nodes"] == nil || data["links"] == nil {
		t.Fatalf("unexpected public schema: %s", w.Body.String())
	}
	payload := readMonitor(t, server)
	// Check the exact nested allowlist, so new operational fields cannot be
	// added silently even when the fixture does not happen to populate them.
	for field, allowed := range map[string][]string{
		"nodes": {"key", "name", "role", "state"},
		"links": {"name", "gateway", "exit", "state", "rtt_ms", "measured_at", "history"},
	} {
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(data[field], &rows); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if len(row) != len(allowed) {
				t.Fatalf("unexpected %s fields: %#v", field, row)
			}
			for _, key := range allowed {
				if row[key] == nil {
					t.Fatalf("missing public %s.%s", field, key)
				}
			}
		}
	}
	if len(payload.Nodes) != 3 || len(payload.Links) != 2 {
		t.Fatalf("unexpected projection: %#v", payload)
	}
	for _, link := range payload.Links {
		if link.Name == "主链路" {
			if link.State != "ready" || link.RTTMillis == nil || *link.RTTMillis != 52.3 || len(link.History) != 3 || link.History[1].RTTMillis != nil {
				t.Fatalf("invalid measured route: %#v", link)
			}
		} else if link.State != "unprobed" || link.RTTMillis != nil || len(link.History) != 0 {
			t.Fatalf("external route fabricated a probe: %#v", link)
		}
	}
	if !reflect.DeepEqual(before, state.Snapshot()) {
		t.Fatal("public reads modified stored state")
	}
}

func TestMonitorStatusFreshness(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	server, _ := testServer(t, nil)
	for _, test := range []struct {
		name, want string
		change     func(*model.State)
	}{
		{"both ready", "ready", func(*model.State) {}},
		{"one-sided", "pending", func(s *model.State) {
			st := s.LinkStatus["private-agent/private-link"]
			st.Ready = false
			s.LinkStatus["private-agent/private-link"] = st
		}},
		{"old node heartbeat", "stale", func(s *model.State) {
			st := s.NodeStatus["private-agent"]
			st.LastSeen = now.Add(-46 * time.Second)
			s.NodeStatus["private-agent"] = st
		}},
		{"old link heartbeat", "stale", func(s *model.State) {
			st := s.LinkStatus["private-agent/private-link"]
			st.LastSeen = now.Add(-46 * time.Second)
			s.LinkStatus["private-agent/private-link"] = st
		}},
		{"old successful probe", "stale", func(s *model.State) {
			st := s.LinkStatus["private-gw/private-link"]
			st.LastSuccess = now.Add(-46 * time.Second)
			s.LinkStatus["private-gw/private-link"] = st
		}},
		{"no successful probe", "unknown", func(s *model.State) {
			st := s.LinkStatus["private-gw/private-link"]
			st.LastSuccess = time.Time{}
			s.LinkStatus["private-gw/private-link"] = st
		}},
		{"not applied", "pending", func(s *model.State) {
			st := s.NodeStatus["private-gw"]
			st.AppliedVersion = 1
			s.NodeStatus["private-gw"] = st
		}},
		{"xray error", "pending", func(s *model.State) {
			st := s.NodeStatus["private-gw"]
			st.XrayError = "private-error"
			s.NodeStatus["private-gw"] = st
		}},
		{"offline link", "offline", func(s *model.State) {
			st := s.LinkStatus["private-agent/private-link"]
			st.Online = false
			s.LinkStatus["private-agent/private-link"] = st
		}},
		{"disabled attachment", "disabled", func(s *model.State) {
			st := s.Attachments["private-route"]
			st.Enabled = false
			s.Attachments["private-route"] = st
		}},
		{"missing reporter", "unknown", func(s *model.State) { delete(s.LinkStatus, "private-agent/private-link") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := model.NewState()
			_ = monitorFixture(now)(&state)
			test.change(&state)
			for _, link := range server.publicMonitor(&state, now).Links {
				if link.Name == "主链路" && (link.State != test.want || (test.want != "ready" && (link.RTTMillis != nil || link.Measured != nil))) {
					t.Fatalf("%s: %#v", test.name, link)
				}
			}
		})
	}
	for _, value := range []float64{0, -1, math.NaN(), math.Inf(1), math.MaxFloat64} {
		if monitorRTT(value) != nil {
			t.Fatalf("invalid RTT %v", value)
		}
	}
}

func TestMonitorCacheVisibilityRevocationAndDefaults(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	server, state := testServer(t, nil)
	for _, path := range []string{"/monitor", "/api/public/monitor"} {
		if w := monitorRequest(server, path); w.Code != 404 {
			t.Fatalf("default enabled at %s", path)
		}
	}
	if err := state.Update(monitorFixture(now)); err != nil {
		t.Fatal(err)
	}
	_ = readMonitor(t, server) // Warm the 10s cache.
	if err := state.Update(func(s *model.State) error { delete(s.Monitor.Nodes, "private-agent"); return nil }); err != nil {
		t.Fatal(err)
	}
	payload := readMonitor(t, server)
	if len(payload.Nodes) != 2 || len(payload.Links) != 1 {
		t.Fatalf("hiding node left cached route: %#v", payload)
	}
	if err := state.Update(func(s *model.State) error { delete(s.Upstreams, "private-upstream"); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(readMonitor(t, server).Links) != 0 {
		t.Fatal("deleted endpoint remained public")
	}
	if err := state.Update(func(s *model.State) error { s.Monitor.Nodes["private-gw"] = "192.0.2.42"; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(readMonitor(t, server).Nodes) != 0 {
		t.Fatal("unsafe legacy alias was published")
	}
	if err := state.Update(func(s *model.State) error { s.Monitor.Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/monitor", "/api/public/monitor"} {
		if w := monitorRequest(server, path); w.Code != 404 {
			t.Fatalf("cached data after disabling at %s", path)
		}
	}
}

func monitorSave(server *Server, values url.Values, withSession, withCSRF bool) *httptest.ResponseRecorder {
	cookie := auth.SignSession(server.cfg.adminSessionKey(), "admin", server.now().Add(time.Hour))
	if withCSRF {
		values.Set("csrf", auth.Derive(server.cfg.sessionKey(), "csrf", cookie))
	}
	r := httptest.NewRequest("POST", "/admin/monitor", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if withSession {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	return w
}

func TestMonitorSettingsAuthorizationValidationPersistenceAndIsolation(t *testing.T) {
	server, state := testServer(t, monitorFixture(time.Unix(1_800_000_000, 0)))
	if w := monitorSave(server, url.Values{}, false, false); w.Code != 303 {
		t.Fatalf("anonymous save: %d", w.Code)
	}
	if w := monitorSave(server, url.Values{}, true, false); w.Code != 403 {
		t.Fatalf("CSRF bypass: %d", w.Code)
	}
	before := state.Snapshot()
	for _, alias := range []string{"", "192.0.2.42", "private.example", "https://private.example", "<img>", "hello\nworld", strings.Repeat("字", 49)} {
		values := url.Values{"enabled": {"on"}, "show_node_private-gw": {"on"}, "node_private-gw": {alias}}
		if w := monitorSave(server, values, true, true); w.Code != 400 {
			t.Fatalf("accepted alias %q: %d", alias, w.Code)
		}
	}
	values := url.Values{"enabled": {"on"}, "show_link_private-link": {"on"}, "link_private-link": {"主链路"}}
	if w := monitorSave(server, values, true, true); w.Code != 400 {
		t.Fatalf("accepted link without endpoints: %d", w.Code)
	}
	values = url.Values{"enabled": {"on"}, "show_node_private-gw": {"on"}, "node_private-gw": {"香港入口 A"}, "show_node_private-agent": {"on"}, "node_private-agent": {"东京出口"}, "show_link_private-link": {"on"}, "link_private-link": {"主链路"}}
	if w := monitorSave(server, values, true, true); w.Code != 303 {
		t.Fatalf("save failed: %d %s", w.Code, w.Body.String())
	}
	after := state.Snapshot()
	if !reflect.DeepEqual(before.Gateways, after.Gateways) || !reflect.DeepEqual(before.Agents, after.Agents) || !reflect.DeepEqual(before.Grants, after.Grants) {
		t.Fatal("publication changed operational state")
	}
	if len(readMonitor(t, server).Nodes) != 2 {
		t.Fatal("wrong publication selection")
	}
	path := filepath.Join(t.TempDir(), "monitor.json")
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Update(func(s *model.State) error { *s = after; return nil }); err != nil {
		t.Fatal(err)
	}
	reopened, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Monitor, reopened.Snapshot().Monitor) {
		t.Fatal("monitor settings not persisted")
	}
	snapshot := state.Snapshot()
	snapshot.Monitor.Nodes["private-gw"] = "changed"
	snapshot.Monitor.Links["private-link"] = "changed"
	if !reflect.DeepEqual(after.Monitor, state.Snapshot().Monitor) {
		t.Fatal("snapshot aliases mutate live state")
	}
	if w := monitorSave(server, url.Values{}, true, true); w.Code != 303 {
		t.Fatalf("disable failed: %d", w.Code)
	}
	if w := monitorRequest(server, "/api/public/monitor"); w.Code != 404 {
		t.Fatal("disable did not revoke public access")
	}
}

func TestMonitorRateLimit(t *testing.T) {
	server, _ := testServer(t, monitorFixture(time.Unix(1_800_000_000, 0)))
	for range 200 {
		if w := monitorRequest(server, "/api/public/monitor"); w.Code != 200 {
			t.Fatalf("unexpected rate limit: %d", w.Code)
		}
	}
	if w := monitorRequest(server, "/api/public/monitor"); w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("missing bounded rate limit")
	}
	server.now = func() time.Time { return time.Unix(1_800_000_001, 0) }
	if w := monitorRequest(server, "/api/public/monitor"); w.Code != 200 {
		t.Fatal("rate limit did not refill")
	}
}

func TestMonitorCacheDoesNotOutliveProbeExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	server, _ := testServer(t, monitorFixture(now))
	server.now = func() time.Time { return now.Add(44 * time.Second) }
	for _, link := range readMonitor(t, server).Links {
		if link.Name == "主链路" && link.State != "ready" {
			t.Fatal("fresh probe not ready")
		}
	}
	// The cache is only 2 seconds old, but the successful probe has expired.
	server.now = func() time.Time { return now.Add(46 * time.Second) }
	for _, link := range readMonitor(t, server).Links {
		if link.Name == "主链路" && (link.State != "stale" || link.RTTMillis != nil) {
			t.Fatalf("cached probe survived expiry: %#v", link)
		}
	}
}

// Opt-in browser fixture, isolated from production configuration and state.
func TestMonitorBrowserFixture(t *testing.T) {
	address := os.Getenv("XMESH_MONITOR_TEST_ADDR")
	if address == "" {
		t.Skip("set XMESH_MONITOR_TEST_ADDR for browser tests")
	}
	now := time.Now().UTC()
	server, _ := testServer(t, monitorFixture(now))
	server.now = func() time.Time { return now }
	server.cfg.PublicURL = "http://" + address
	t.Logf("Monitor browser fixture: http://%s", address)
	if err := http.ListenAndServe(address, server.Handler()); err != nil {
		t.Fatal(err)
	}
}
