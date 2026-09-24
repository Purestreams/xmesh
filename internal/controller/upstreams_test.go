package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"xmesh/internal/auth"
	"xmesh/internal/model"
)

func testVMessLink(t *testing.T, name, address, uuid string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"v": "2", "ps": name, "add": address, "port": "443", "id": uuid, "aid": "0", "net": "ws", "host": "cdn.example", "path": "/relay", "tls": "tls"})
	if err != nil {
		t.Fatal(err)
	}
	return "vmess://" + base64.RawStdEncoding.EncodeToString(b)
}

func postUpstreamForm(path string, values url.Values, handler http.HandlerFunc) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	parts := strings.Split(path, "/")
	if len(parts) > 3 {
		request.SetPathValue("id", parts[3])
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func TestVMessSubscriptionSelectionAndGatewayRoute(t *testing.T) {
	first := testVMessLink(t, "Singapore", "one.example", "00000000-0000-4000-8000-000000000011")
	second := testVMessLink(t, "Tokyo", "two.example", "00000000-0000-4000-8000-000000000012")
	var body atomic.Value
	body.Store(first + "\n" + second)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body.Load().(string))) }))
	defer source.Close()
	server, state := testServer(t, func(s *model.State) error {
		s.Users["user"] = model.User{ID: "user", Name: "User", Enabled: true}
		s.Gateways["gateway"] = model.Gateway{ID: "gateway", Name: "Edge", PublicHost: "edge.example", VMessPort: 8080, VMessPath: "/proxy", Enabled: true, DesiredVersion: 1, CredentialHash: auth.SecretHash("gateway-secret")}
		s.NodeStatus["gateway"] = model.NodeStatus{ExternalUpstreams: true}
		return nil
	})
	created := postUpstreamForm("/admin/upstreams", url.Values{"name": {"Remote"}, "source": {source.URL}}, server.createUpstream)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create status %d: %s", created.Code, created.Body.String())
	}
	var upstream model.VMessUpstream
	for _, item := range state.Snapshot().Upstreams {
		upstream = item
	}
	if upstream.ID == "" || upstream.Endpoint.UUID != "" || len(upstream.Candidates) != 2 {
		t.Fatalf("subscription should await selection: %+v", upstream)
	}
	panelRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	panelRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: "fixture"})
	panel := httptest.NewRecorder()
	server.panel(panel, panelRequest)
	if !strings.Contains(panel.Body.String(), "外部出口") || strings.Contains(panel.Body.String(), source.URL) {
		t.Fatal("panel does not show safe upstream controls")
	}
	attached := postUpstreamForm("/admin/upstream-attachments", url.Values{"gateway_id": {"gateway"}, "upstream_id": {upstream.ID}}, server.attachUpstream)
	if attached.Code != http.StatusSeeOther {
		t.Fatalf("attach status %d: %s", attached.Code, attached.Body.String())
	}
	var route model.Attachment
	for _, item := range state.Snapshot().Attachments {
		route = item
	}
	panel = httptest.NewRecorder()
	server.panel(panel, panelRequest)
	if !strings.Contains(panel.Body.String(), `name="attachment_id" value="`+route.ID+`"`) || !strings.Contains(panel.Body.String(), "· 外部出口") {
		t.Fatal("external route missing from unified subscription chooser")
	}
	if got := postUpstreamForm("/admin/subscribe", url.Values{"user_id": {"user"}, "attachment_id": {route.ID}}, server.openSubscription); got.Code != http.StatusBadRequest {
		t.Fatalf("unselected upstream was granted: %d", got.Code)
	}
	selected := postUpstreamForm("/admin/upstreams/"+upstream.ID+"/select", url.Values{"selected_key": {upstream.Candidates[1].Key}}, server.selectUpstream)
	if selected.Code != http.StatusSeeOther {
		t.Fatalf("select status %d: %s", selected.Code, selected.Body.String())
	}
	if err := state.Update(func(s *model.State) error {
		s.NodeStatus["gateway"] = model.NodeStatus{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := postUpstreamForm("/admin/subscribe", url.Values{"user_id": {"user"}, "attachment_id": {route.ID}}, server.openSubscription); got.Code != http.StatusBadRequest {
		t.Fatalf("old Gateway accepted external route: %d", got.Code)
	}
	if err := state.Update(func(s *model.State) error {
		s.NodeStatus["gateway"] = model.NodeStatus{ExternalUpstreams: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := postUpstreamForm("/admin/subscribe", url.Values{"user_id": {"user"}, "attachment_id": {route.ID}}, server.openSubscription); got.Code != http.StatusSeeOther {
		t.Fatalf("grant status %d: %s", got.Code, got.Body.String())
	}
	var grant model.Grant
	for _, item := range state.Snapshot().Grants {
		grant = item
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	request.Header.Set("Authorization", "Bearer gateway-secret")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("config status %d: %s", recorder.Code, recorder.Body.String())
	}
	var config GatewayConfig
	if err := json.Unmarshal(recorder.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Upstreams) != 1 || config.Upstreams[0].Endpoint.Address != "two.example" || len(config.Grants) != 1 || len(config.Links) != 0 || strings.Contains(recorder.Body.String(), source.URL) {
		t.Fatalf("wrong Gateway config: %s", recorder.Body.String())
	}
	report := StatusReport{Status: model.NodeStatus{Ready: true, XrayReady: true, ExternalUpstreams: true, AppliedVersion: state.Snapshot().Gateways["gateway"].DesiredVersion}, Links: []model.LinkStatus{{LinkID: route.ID}}, Grants: []model.GrantStatus{{GrantID: grant.ID, Links: []model.GrantLinkUsage{{LinkID: route.ID, UploadBytes: 24, DownloadBytes: 42}}}}}
	payload, _ := json.Marshal(report)
	status := httptest.NewRequest(http.MethodPost, "/api/v1/status", bytes.NewReader(payload))
	status.Header.Set("Authorization", "Bearer gateway-secret")
	result := httptest.NewRecorder()
	server.Handler().ServeHTTP(result, status)
	if result.Code != http.StatusNoContent {
		t.Fatalf("report status %d: %s", result.Code, result.Body.String())
	}
	if !state.Snapshot().Grants[grant.ID].Published {
		t.Fatal("grant not published after Gateway applied")
	}
	report.Status.ExternalUpstreams = false
	payload, _ = json.Marshal(report)
	status = httptest.NewRequest(http.MethodPost, "/api/v1/status", bytes.NewReader(payload))
	status.Header.Set("Authorization", "Bearer gateway-secret")
	result = httptest.NewRecorder()
	server.Handler().ServeHTTP(result, status)
	if result.Code != http.StatusNoContent || state.Snapshot().Grants[grant.ID].Published {
		t.Fatalf("old Gateway kept external grant published: %d", result.Code)
	}
	report.Status.ExternalUpstreams = true
	payload, _ = json.Marshal(report)
	status = httptest.NewRequest(http.MethodPost, "/api/v1/status", bytes.NewReader(payload))
	status.Header.Set("Authorization", "Bearer gateway-secret")
	result = httptest.NewRecorder()
	server.Handler().ServeHTTP(result, status)
	if result.Code != http.StatusNoContent || !state.Snapshot().Grants[grant.ID].Published {
		t.Fatalf("upgraded Gateway did not republish external grant: %d", result.Code)
	}
	dashboard := httptest.NewRecorder()
	server.dashboard(dashboard, httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil))
	if !strings.Contains(dashboard.Body.String(), `"upstream_id":"`+upstream.ID+`"`) || strings.Contains(dashboard.Body.String(), source.URL) || strings.Contains(dashboard.Body.String(), "00000000-0000-4000-8000-000000000012") {
		t.Fatal("dashboard leaked upstream credentials or omitted route type")
	}
	subscription, err := BuildSubscription(state.Snapshot(), "user")
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := base64.StdEncoding.DecodeString(subscription)
	if !strings.Contains(string(plain), "vmess://") || strings.Contains(string(plain), "two.example") {
		t.Fatalf("wrong client subscription: %s", plain)
	}
	if len(state.Snapshot().UsageHistory) != 1 {
		t.Fatal("external route usage was not recorded")
	}
	previousEndpoint := state.Snapshot().Upstreams[upstream.ID].Endpoint
	previousVersion := state.Snapshot().Gateways["gateway"].DesiredVersion
	body.Store(first + "\n" + testVMessLink(t, "Tokyo renamed", "two.example", "00000000-0000-4000-8000-000000000012"))
	if err := server.refreshUpstream(context.Background(), upstream.ID); err != nil {
		t.Fatal(err)
	}
	renamed := state.Snapshot()
	if renamed.Upstreams[upstream.ID].Endpoint.Name != "Tokyo renamed" || renamed.Upstreams[upstream.ID].SelectedKey != upstreamKey(previousEndpoint) || renamed.Gateways["gateway"].DesiredVersion != previousVersion || !renamed.Grants[grant.ID].Published {
		t.Fatal("node rename changed routing or unpublished grant")
	}
	if err := state.Update(func(s *model.State) error {
		item := s.Upstreams[upstream.ID]
		item.SelectedKey, item.Endpoint = legacyUpstreamKey(previousEndpoint), previousEndpoint
		s.Upstreams[upstream.ID] = item
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	body.Store(first + "\n" + testVMessLink(t, "Tokyo renamed", "two.example", "00000000-0000-4000-8000-000000000012") + "\n" + testVMessLink(t, "Tokyo", "impostor.example", "00000000-0000-4000-8000-000000000012"))
	if err := server.refreshUpstream(context.Background(), upstream.ID); err != nil {
		t.Fatal(err)
	}
	migrated := state.Snapshot()
	if migrated.Upstreams[upstream.ID].SelectedKey != upstreamKey(previousEndpoint) || migrated.Upstreams[upstream.ID].Endpoint.Name != "Tokyo renamed" || migrated.Gateways["gateway"].DesiredVersion != previousVersion || !migrated.Grants[grant.ID].Published {
		t.Fatal("legacy selection did not survive node rename")
	}
	body.Store(`<html><a href="https://provider.example/login">Sign in</a></html>`)
	failedRefresh := postUpstreamForm("/admin/upstreams/"+upstream.ID+"/refresh", url.Values{}, server.refreshUpstreamHTTP)
	if failedRefresh.Code != http.StatusBadRequest {
		t.Fatalf("invalid subscription refresh returned %d", failedRefresh.Code)
	}
	stale := state.Snapshot()
	if stale.Upstreams[upstream.ID].Endpoint.UUID == "" || stale.Upstreams[upstream.ID].LastError == "" || !stale.Grants[grant.ID].Published {
		t.Fatal("invalid HTML subscription discarded last known node")
	}
	body.Store("temporary invalid subscription")
	if err := server.refreshUpstream(context.Background(), upstream.ID); err == nil {
		t.Fatal("invalid subscription refresh did not report failure")
	}
	stale = state.Snapshot()
	if stale.Upstreams[upstream.ID].Endpoint.UUID == "" || stale.Upstreams[upstream.ID].LastError == "" || !stale.Grants[grant.ID].Published {
		t.Fatal("transient subscription failure discarded last known node")
	}
	body.Store(first)
	if err := server.refreshUpstream(context.Background(), upstream.ID); err == nil {
		t.Fatal("missing selected node did not report failure")
	}
	snapshot := state.Snapshot()
	if snapshot.Upstreams[upstream.ID].Endpoint.UUID != "" || snapshot.Grants[grant.ID].Published {
		t.Fatal("missing selected node did not close the route")
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	request.Header.Set("Authorization", "Bearer gateway-secret")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	config = GatewayConfig{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &config); err != nil || len(config.Upstreams) != 0 || len(config.Grants) != 0 {
		t.Fatalf("removed node still in Gateway config: %s %v", recorder.Body.String(), err)
	}
}

func TestVMessImportRejectsUnsupportedTransport(t *testing.T) {
	link := testVMessLink(t, "Invalid", "host.example", "00000000-0000-4000-8000-000000000011")
	if endpoint, err := parseVMessURI(link); err != nil || endpoint.Network != "ws" || !endpoint.TLS || endpoint.ServerName != "cdn.example" {
		t.Fatalf("valid import failed: %+v %v", endpoint, err)
	}
	b, _ := json.Marshal(map[string]any{"ps": "Invalid", "add": "host.example", "port": 443, "id": "00000000-0000-4000-8000-000000000011", "aid": 0, "net": "grpc"})
	if _, err := parseVMessURI("vmess://" + base64.StdEncoding.EncodeToString(b)); err == nil {
		t.Fatal("unsupported transport accepted")
	}
	subscription := base64.StdEncoding.EncodeToString([]byte(link + "\n"))
	if nodes, err := parseVMessSubscription([]byte(subscription)); err != nil || len(nodes) != 1 || nodes[0].Address != "host.example" {
		t.Fatalf("base64 subscription not parsed: %+v %v", nodes, err)
	}
	if nodes, err := parseVMessSubscription([]byte("trojan://secret@host.example:443")); err != nil || len(nodes) != 0 {
		t.Fatalf("valid non-VMess subscription was rejected: %+v %v", nodes, err)
	}
}

func TestVMessImportRejectsUnrepresentedOptions(t *testing.T) {
	base := map[string]any{"v": "2", "ps": "WS TLS", "add": "edge.example", "port": "443", "id": "00000000-0000-4000-8000-000000000011", "aid": "0", "scy": "auto", "net": "ws", "type": "none", "host": "cdn.example", "path": "/ws", "tls": "tls", "sni": "", "alpn": "", "fp": "", "insecure": "0", "vcn": "", "pcs": ""}
	parse := func(values map[string]any) error {
		t.Helper()
		payload, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		_, err = parseVMessURI("vmess://" + base64.RawStdEncoding.EncodeToString(payload))
		return err
	}
	if err := parse(base); err != nil {
		t.Fatalf("sample VMess option shape rejected: %v", err)
	}
	for _, option := range []struct {
		field string
		value any
	}{{"type", "http"}, {"alpn", "h2"}, {"fp", "chrome"}, {"insecure", "1"}, {"unknown_transport", "value"}} {
		candidate := map[string]any{}
		for key, value := range base {
			candidate[key] = value
		}
		candidate[option.field] = option.value
		if err := parse(candidate); err == nil {
			t.Fatalf("unsupported VMess option %s accepted", option.field)
		}
	}
}

func TestVLESSRealityVisionLinkAndDuplicateSubscription(t *testing.T) {
	publicKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
	link := `vless\://00000000-0000-4000-8000-000000000021\@203.0.113.71:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www\.example.com&fp=chrome&pbk=` + publicKey + `&sid=e8f030&spx=%2F&type=tcp&headerType=none#Tokyo`
	endpoint, err := parseExternalURI(link)
	if err != nil || endpoint.Protocol != "vless" || endpoint.Network != "raw" || endpoint.ServerName != "www.example.com" || endpoint.ShortID != "e8f030" || endpoint.SpiderX != "/" {
		t.Fatalf("VLESS example shape rejected: %+v %v", endpoint, err)
	}
	vmess := testVMessLink(t, "WS TLS", "exit.example.com", "00000000-0000-4000-8000-000000000022")
	nodes, err := parseVMessSubscription([]byte(link + "\n" + link + "\n" + vmess))
	if err != nil || len(nodes) != 2 || len(subscriptionCandidates(nodes)) != 2 {
		t.Fatalf("mixed duplicate subscription: %+v %v", nodes, err)
	}
	if selected, err := selectedEndpoint(nodes, upstreamKey(endpoint)); err != nil || !sameVMessConnection(selected, endpoint) {
		t.Fatalf("duplicate node cannot be selected: %+v %v", selected, err)
	}
	if _, err := parseExternalURI(strings.Replace(link, "flow=xtls-rprx-vision", "flow=unknown", 1)); err == nil {
		t.Fatal("unsupported VLESS flow accepted")
	}
	if _, err := parseExternalURI(strings.Replace(link, "fp=chrome", "fp=not-a-fingerprint", 1)); err == nil {
		t.Fatal("unknown REALITY fingerprint accepted")
	}
}

func TestSubscriptionFetchRejectsPrivateAddress(t *testing.T) {
	link := testVMessLink(t, "Local", "localhost", "00000000-0000-4000-8000-000000000023")
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(link)) }))
	defer source.Close()
	if _, err := fetchVMessSubscription(context.Background(), source.URL, false); err == nil {
		t.Fatal("private subscription address accepted")
	}
	if nodes, err := fetchVMessSubscription(context.Background(), source.URL, true); err != nil || len(nodes) != 1 {
		t.Fatalf("explicit private source did not work: %+v %v", nodes, err)
	}
}
