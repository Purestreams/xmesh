package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
)

// Isolated browser fixture; its Gateway reports synthetic test results.
func TestUpstreamProbeBrowserFixture(t *testing.T) {
	address := os.Getenv("XMESH_PROBE_BROWSER_TEST_ADDR")
	if address == "" {
		t.Skip("set XMESH_PROBE_BROWSER_TEST_ADDR for browser tests")
	}
	now := time.Now().UTC()
	server, store := testServer(t, func(s *model.State) error {
		s.Gateways["gateway"] = model.Gateway{ID: "gateway", Name: "Test Gateway", Enabled: true, DesiredVersion: 1, VMessPort: 8080, CredentialHash: auth.SecretHash("probe-fixture-secret")}
		s.NodeStatus["gateway"] = model.NodeStatus{Online: true, Ready: true, XrayReady: true, AppliedVersion: 1, UpstreamProbes: true, ExternalUpstreams: true, UpstreamProbeIntervalSeconds: 300, LastSeen: now}
		s.Upstreams["upstream"] = model.VMessUpstream{ID: "upstream", Name: "Test external exit", Enabled: true, Endpoint: model.VMessEndpoint{UUID: "fixture-only", Address: "exit.example", Name: "Synthetic test node"}}
		s.Attachments["route"] = model.Attachment{ID: "route", GatewayID: "gateway", UpstreamID: "upstream", Enabled: true}
		return nil
	})
	server.now = time.Now
	server.cfg.PublicURL = "http://" + address
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		lastRequest, initialized := "", false
		var result model.UpstreamProbeStatus
		for range ticker.C {
			attachment := store.Snapshot().Attachments["route"]
			if !initialized || attachment.ProbeRequestID != lastRequest {
				lastRequest, initialized = attachment.ProbeRequestID, true
				result = model.UpstreamProbeStatus{AttachmentID: "route", Revision: 1, RequestID: lastRequest, StartedAt: time.Now().UTC()}
			} else if result.CompletedAt.IsZero() {
				result.CompletedAt = time.Now().UTC()
				result.Targets = []model.UpstreamProbeTarget{
					{URL: model.UpstreamIPProbeURL, Success: true, HTTPStatus: 200, DurationMillis: 123, ExitIP: "155.248.187.71"},
					{URL: model.UpstreamMicrosoftProbeURL, Success: true, HTTPStatus: 200, DurationMillis: 456},
				}
				if lastRequest != "" {
					result.Targets[1].Success = false
					result.Targets[1].HTTPStatus = 403
					result.Targets[1].Error = "HTTP 403"
				}
			}
			body, _ := json.Marshal(StatusReport{Status: model.NodeStatus{Online: true, Ready: true, XrayReady: true, AppliedVersion: 1, ExternalUpstreams: true, UpstreamProbes: true, UpstreamProbeIntervalSeconds: 300}, UpstreamProbes: []model.UpstreamProbeStatus{result}})
			req, _ := http.NewRequest(http.MethodPost, "http://"+address+"/api/v1/status", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer probe-fixture-secret")
			req.Header.Set("Content-Type", "application/json")
			client := &http.Client{Timeout: 2 * time.Second}
			if response, err := client.Do(req); err == nil {
				_ = response.Body.Close()
			}
		}
	}()
	t.Logf("Probe browser fixture: http://%s", address)
	if err := http.ListenAndServe(address, server.Handler()); err != nil {
		t.Fatal(err)
	}
}
