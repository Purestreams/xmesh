package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/infra/conf/serial"

	"xmesh/internal/controller"
	"xmesh/internal/model"
	"xmesh/internal/runtimecfg"
)

func TestVLESSRealityVisionOutboundConfig(t *testing.T) {
	publicKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
	endpoint := model.VMessEndpoint{Protocol: "vless", Address: "exit.example", Port: 443, UUID: "00000000-0000-4000-8000-000000000021", Network: "raw", ServerName: "www.example.com", Flow: "xtls-rprx-vision", PublicKey: publicKey, ShortID: "e8f030", Fingerprint: "chrome", SpiderX: "/"}
	config := controller.GatewayConfig{Gateway: model.Gateway{VMessPort: 8080, VMessPath: "/proxy"}, Upstreams: []controller.GatewayUpstreamConfig{{AttachmentID: "route", Endpoint: endpoint}}, Grants: []model.Grant{{ID: "grant", AttachmentID: "route", VMessUUID: "00000000-0000-4000-8000-000000000022", Enabled: true}}}
	payload, err := buildXrayConfig(config, "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	var rendered xrayConfig
	if err := json.Unmarshal(payload, &rendered); err != nil {
		t.Fatal(err)
	}
	if len(rendered.Outbounds) != 2 || rendered.Outbounds[1].Protocol != "vless" || rendered.Outbounds[1].Settings.Flow != "xtls-rprx-vision" || rendered.Outbounds[1].StreamSettings.Security != "reality" {
		t.Fatalf("wrong VLESS outbound: %+v", rendered.Outbounds)
	}
	if _, err := serial.LoadJSONConfig(bytes.NewReader(payload)); err != nil {
		t.Fatalf("locked Xray version rejected VLESS config: %v", err)
	}
	config.Upstreams[0].Endpoint.Fingerprint = "not-a-fingerprint"
	invalid, err := buildXrayConfig(config, "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serial.LoadJSONConfig(bytes.NewReader(invalid)); err == nil {
		t.Fatal("locked Xray version accepted an unknown REALITY fingerprint")
	}
}

func TestInvalidRevisionKeepsWorkingXrayConfig(t *testing.T) {
	old := controller.GatewayConfig{Revision: 1, Gateway: model.Gateway{ID: "gateway", VMessPort: 8080, VMessPath: "/proxy"}}
	invalid := controller.GatewayConfig{Revision: 2, Gateway: old.Gateway, Upstreams: []controller.GatewayUpstreamConfig{{AttachmentID: "route", Endpoint: model.VMessEndpoint{Protocol: "unsupported", UUID: "00000000-0000-4000-8000-000000000021"}}}}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(invalid) }))
	defer source.Close()
	runtime := New(runtimecfg.Config{NodeID: "gateway", ControllerURL: source.URL, Credential: "secret", Gateway: runtimecfg.Gateway{SOCKSListen: "127.0.0.1:18080", XrayConfigPath: filepath.Join(t.TempDir(), "xray.json")}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runtime.config = old
	runtime.xrayReady.Store(true)
	if err := runtime.refresh(context.Background()); err == nil {
		t.Fatal("invalid candidate revision was accepted")
	}
	if runtime.config.Revision != old.Revision || !runtime.xrayReady.Load() || len(runtime.xrayApply) != 0 {
		t.Fatal("invalid revision replaced or stopped the working Xray config")
	}
}

func TestFailedFinalReportKeepsRetiredUsageForRetry(t *testing.T) {
	var attempts atomic.Int32
	var accepted controller.StatusReport
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&accepted); err != nil {
			t.Errorf("decode report: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer source.Close()
	runtime := New(runtimecfg.Config{NodeID: "gateway", ControllerURL: source.URL, Credential: "secret"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runtime.config = controller.GatewayConfig{Revision: 2, Gateway: model.Gateway{ID: "gateway"}}
	runtime.pendingStats[1] = pendingStatsConfig{config: controller.GatewayConfig{Revision: 1, Upstreams: []controller.GatewayUpstreamConfig{{AttachmentID: "old-route"}}, Grants: []model.Grant{{ID: "old-grant", AttachmentID: "old-route"}}}, expiresAt: time.Now().Add(time.Hour)}
	runtime.grantCounters("old-grant").upload.Add(12)
	runtime.grantLinkCounters("old-grant", "old-route").upload.Add(12)
	if err := runtime.report(context.Background()); err == nil || len(runtime.pendingStats) != 1 {
		t.Fatal("failed report discarded pending usage")
	}
	if err := runtime.report(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runtime.pendingStats) != 0 || len(accepted.Grants) != 1 || accepted.Grants[0].GrantID != "old-grant" || len(accepted.Grants[0].Links) != 1 || accepted.Grants[0].Links[0].UploadBytes != 12 {
		t.Fatalf("retry did not deliver retired usage: %+v", accepted.Grants)
	}
}

func TestExpiredPendingUsageDoesNotBlockStatus(t *testing.T) {
	var accepted controller.StatusReport
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&accepted); err != nil {
			t.Errorf("decode report: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer source.Close()
	runtime := New(runtimecfg.Config{NodeID: "gateway", ControllerURL: source.URL, Credential: "secret"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runtime.config = controller.GatewayConfig{Revision: 2, Gateway: model.Gateway{ID: "gateway"}}
	runtime.pendingStats[1] = pendingStatsConfig{config: controller.GatewayConfig{Revision: 1, Upstreams: []controller.GatewayUpstreamConfig{{AttachmentID: "old-route"}}, Grants: []model.Grant{{ID: "old-grant", AttachmentID: "old-route"}}}, expiresAt: time.Now().Add(-time.Second)}
	runtime.grantLinkCounters("old-grant", "old-route").upload.Add(12)
	if err := runtime.report(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runtime.pendingStats) != 0 || len(accepted.Grants) != 0 || len(accepted.Links) != 0 {
		t.Fatalf("expired pending revision was reported: %+v", accepted)
	}
}
