package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
)

func completedProbe(now time.Time, request string) model.UpstreamProbeStatus {
	return model.UpstreamProbeStatus{AttachmentID: "route", Revision: 4, RequestID: request, StartedAt: now.Add(-time.Second), CompletedAt: now, Targets: []model.UpstreamProbeTarget{
		{URL: model.UpstreamIPProbeURL, Success: true, HTTPStatus: 200, DurationMillis: 123, ExitIP: "155.248.187.71"},
		{URL: model.UpstreamMicrosoftProbeURL, Success: true, HTTPStatus: 200, DurationMillis: 456},
	}}
}

func TestUpstreamProbeLifecycleThroughGatewayConfigAndReports(t *testing.T) {
	now := time.Now().UTC()
	server, store := testServer(t, func(s *model.State) error {
		s.Gateways["gw"] = model.Gateway{ID: "gw", Enabled: true, DesiredVersion: 4, CredentialHash: auth.SecretHash("gateway-secret")}
		s.Upstreams["up"] = model.VMessUpstream{ID: "up", Enabled: true, Endpoint: model.VMessEndpoint{UUID: "selected-node", Address: "exit.invalid"}}
		s.Attachments["route"] = model.Attachment{ID: "route", GatewayID: "gw", UpstreamID: "up", Enabled: true}
		s.NodeStatus["gw"] = model.NodeStatus{Online: true, Ready: true, XrayReady: true, ExternalUpstreams: true, UpstreamProbes: true, UpstreamProbeIntervalSeconds: 300, LastSeen: now, AppliedVersion: 4}
		return nil
	})
	server.now = func() time.Time { return now }
	report := func(result model.UpstreamProbeStatus) *httptest.ResponseRecorder {
		t.Helper()
		payload, _ := json.Marshal(StatusReport{Status: model.NodeStatus{Online: true, Ready: true, XrayReady: true, ExternalUpstreams: true, UpstreamProbes: true, UpstreamProbeIntervalSeconds: 300, AppliedVersion: 4}, UpstreamProbes: []model.UpstreamProbeStatus{result}})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/status", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer gateway-secret")
		out := httptest.NewRecorder()
		server.Handler().ServeHTTP(out, req)
		return out
	}
	readRoute := func() dashboardRoute {
		t.Helper()
		out := httptest.NewRecorder()
		server.dashboard(out, httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil))
		var data dashboardData
		if err := json.Unmarshal(out.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		for _, route := range data.Routes {
			if route.ID == "route" {
				return route
			}
		}
		t.Fatalf("missing route: %+v", data.Routes)
		return dashboardRoute{}
	}
	healthy := completedProbe(now, "")
	if got := report(healthy); got.Code != http.StatusNoContent {
		t.Fatalf("probe report: %d %s", got.Code, got.Body.String())
	}
	if route := readRoute(); route.State != "ready" || route.Probe.Targets[0].ExitIP != "155.248.187.71" || !route.CanProbe {
		t.Fatalf("healthy route: %+v", route)
	}
	if got := report(healthy); got.Code != http.StatusNoContent {
		t.Fatal(got.Body.String())
	}
	if len(store.Snapshot().LinkHistory["external:route"]) != 1 {
		t.Fatal("duplicate heartbeat sampled another measurement")
	}
	snapshot := store.Snapshot()
	snapshot.Attachments["route"].UpstreamProbe.Targets[0].ExitIP = "changed"
	if store.Snapshot().Attachments["route"].UpstreamProbe.Targets[0].ExitIP != "155.248.187.71" {
		t.Fatal("snapshot mutated stored probe")
	}
	manual := httptest.NewRequest(http.MethodPost, "/admin/attachments/route/probe", nil)
	manual.SetPathValue("id", "route")
	response := httptest.NewRecorder()
	server.requestUpstreamProbe(response, manual)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("manual test: %d %s", response.Code, response.Body.String())
	}
	state := store.Snapshot()
	request := state.Attachments["route"].ProbeRequestID
	if request == "" || state.Gateways["gw"].DesiredVersion != 4 {
		t.Fatal("manual test missing or unnecessarily restarted Gateway")
	}
	response = httptest.NewRecorder()
	server.requestUpstreamProbe(response, manual)
	if store.Snapshot().Attachments["route"].ProbeRequestID != request {
		t.Fatal("repeated click replaced pending request")
	}
	configRequest := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	configRequest.Header.Set("Authorization", "Bearer gateway-secret")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, configRequest)
	var config GatewayConfig
	if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Upstreams) != 1 || config.Upstreams[0].ProbeRequestID != request || len(config.Grants) != 0 {
		t.Fatal("probe was not delivered independently of grants")
	}
	if got := report(healthy); got.Code != http.StatusNoContent {
		t.Fatal(got.Body.String())
	}
	if readRoute().State == "ready" {
		t.Fatal("old request falsely verified newer manual test")
	}
	pending := completedProbe(now, request)
	pending.CompletedAt, pending.Targets = time.Time{}, nil
	if got := report(pending); got.Code != http.StatusNoContent {
		t.Fatal(got.Body.String())
	}
	if readRoute().State != "testing" {
		t.Fatal("running test not displayed")
	}
	failed := completedProbe(now, request)
	failed.Targets[1].Success, failed.Targets[1].HTTPStatus, failed.Targets[1].Error = false, 403, "HTTP 403"
	if got := report(failed); got.Code != http.StatusNoContent {
		t.Fatal(got.Body.String())
	}
	failureSince := store.Snapshot().Attachments["route"].UpstreamProbeFailureSince
	if failureSince.IsZero() {
		t.Fatal("failed probe did not start the continuous failure window")
	}
	now = now.Add(time.Minute)
	failed.StartedAt, failed.CompletedAt = now.Add(-time.Second), now
	if got := report(failed); got.Code != http.StatusNoContent {
		t.Fatal(got.Body.String())
	}
	if !store.Snapshot().Attachments["route"].UpstreamProbeFailureSince.Equal(failureSince) {
		t.Fatal("repeated failure reset the continuous failure window")
	}
	if readRoute().State != "probe_failed" {
		t.Fatal("partial website failure falsely marked route healthy")
	}
	now = now.Add(time.Minute)
	recovered := completedProbe(now, request)
	if got := report(recovered); got.Code != http.StatusNoContent {
		t.Fatal(got.Body.String())
	}
	if !store.Snapshot().Attachments["route"].UpstreamProbeFailureSince.IsZero() {
		t.Fatal("successful probe did not clear the failure window")
	}
	foreign := failed
	foreign.AttachmentID = "foreign"
	if err := store.Update(func(s *model.State) error {
		s.Attachments["foreign"] = model.Attachment{ID: "foreign", GatewayID: "other", UpstreamID: "up", Enabled: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := report(foreign); got.Code == http.StatusNoContent {
		t.Fatal("Gateway overwrote another Gateway's measurement")
	}
	if err := store.Update(func(s *model.State) error {
		g := s.Gateways["gw"]
		g.DesiredVersion = 5
		s.Gateways["gw"] = g
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := report(failed); got.Code != http.StatusNoContent {
		t.Fatal(got.Body.String())
	}
	if readRoute().Probe != nil {
		t.Fatal("old configuration result remained current")
	}
}

func TestUpstreamProbeRejectsUnsupportedOfflineAndDisabledRoutes(t *testing.T) {
	for _, test := range []struct {
		name                        string
		capability, online, enabled bool
	}{
		{"old-gateway", false, true, true}, {"offline", true, false, true}, {"disabled", true, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _ := testServer(t, func(s *model.State) error {
				s.Gateways["gw"] = model.Gateway{ID: "gw", Enabled: true}
				s.Upstreams["up"] = model.VMessUpstream{Enabled: true, Endpoint: model.VMessEndpoint{UUID: "node"}}
				s.Attachments["route"] = model.Attachment{ID: "route", GatewayID: "gw", UpstreamID: "up", Enabled: test.enabled}
				s.NodeStatus["gw"] = model.NodeStatus{UpstreamProbes: test.capability, Online: test.online, LastSeen: time.Now()}
				return nil
			})
			request := httptest.NewRequest(http.MethodPost, "/admin/attachments/route/probe", nil)
			request.SetPathValue("id", "route")
			response := httptest.NewRecorder()
			server.requestUpstreamProbe(response, request)
			if response.Code != http.StatusConflict {
				t.Fatalf("unsafe test request accepted: %d", response.Code)
			}
		})
	}
}
