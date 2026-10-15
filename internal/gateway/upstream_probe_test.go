package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/infra/conf/serial"

	"github.com/Purestreams/xmesh/internal/controller"
	"github.com/Purestreams/xmesh/internal/model"
	"github.com/Purestreams/xmesh/internal/runtimecfg"
)

type probeRoundTripper func(*http.Request) (*http.Response, error)

func (f probeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func probeTestConfig() controller.GatewayConfig {
	return controller.GatewayConfig{Revision: 7, Gateway: model.Gateway{ID: "gw", Enabled: true, VMessPort: 8080}, Upstreams: []controller.GatewayUpstreamConfig{
		{AttachmentID: "one", Endpoint: model.VMessEndpoint{Address: "one.invalid", Port: 443, UUID: "00000000-0000-4000-8000-000000000021", Network: "tcp"}},
		{AttachmentID: "two", Endpoint: model.VMessEndpoint{Address: "two.invalid", Port: 443, UUID: "00000000-0000-4000-8000-000000000022", Network: "tcp"}},
	}}
}

func TestUpstreamProbeUsesPrivateAuthenticatedRoutesWithoutGrants(t *testing.T) {
	config := probeTestConfig()
	base, err := buildXrayConfig(config, "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := addUpstreamProbeInbound(base, config, "127.0.0.1:18083", "node-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serial.LoadJSONConfig(bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	var rendered xrayConfig
	if err := json.Unmarshal(payload, &rendered); err != nil {
		t.Fatal(err)
	}
	inbound := rendered.Inbounds[len(rendered.Inbounds)-1]
	if inbound.Listen != "127.0.0.1" || inbound.Settings.Auth != "password" || len(inbound.Settings.Accounts) != 2 {
		t.Fatal("probe listener is not isolated and authenticated")
	}
	if rendered.Outbounds[0].Protocol != "blackhole" {
		t.Fatal("unknown probes can fall back to direct")
	}
	for i, upstream := range config.Upstreams {
		user, pass := upstreamProbeAuth("node-secret", upstream.AttachmentID)
		rule := rendered.Routing.Rules[i]
		if rule.User[0] != user || rule.OutboundTag != "upstream-"+upstream.AttachmentID || inbound.Settings.Accounts[i].Pass != pass {
			t.Fatal("probe does not select its exact outbound")
		}
	}
	config.Upstreams[0].ProbeRequestID = "manual-request"
	again, err := addUpstreamProbeInbound(base, config, "127.0.0.1:18083", "node-secret")
	if err != nil || !bytes.Equal(payload, again) {
		t.Fatal("manual testing changes the running Xray configuration")
	}
	for _, address := range []string{"0.0.0.0:18083", "localhost:18083", "127.0.0.1:0"} {
		if _, err := addUpstreamProbeInbound(base, config, address, "secret"); err == nil {
			t.Fatalf("accepted unsafe listener %q", address)
		}
	}
}

func TestRunningXrayConfigIncludesProbeListener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xray.json")
	r := New(runtimecfg.Config{Credential: "node-secret", Gateway: runtimecfg.Gateway{SOCKSListen: "127.0.0.1:18080", UpstreamProbeListen: "127.0.0.1:18083", XrayConfigPath: path}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := r.writeXrayConfig(probeTestConfig()); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rendered xrayConfig
	if err := json.Unmarshal(payload, &rendered); err != nil {
		t.Fatal(err)
	}
	for _, inbound := range rendered.Inbounds {
		if inbound.Tag == "xmesh-upstream-probe" && inbound.Listen == "127.0.0.1" && inbound.Port == 18083 {
			return
		}
	}
	t.Fatal("running Xray config omitted the upstream probe listener")
}

func TestUpstreamHTTPSResultsRequireBothSitesAndValidExitIP(t *testing.T) {
	for _, test := range []struct {
		name, ip          string
		microsoft         int
		ipOK, microsoftOK bool
	}{
		{"healthy", "155.248.187.71\n", 200, true, true},
		{"private-ip", "127.0.0.1", 200, false, true},
		{"invalid-ip", "<html>blocked</html>", 200, false, true},
		{"site-blocked", "155.248.187.71", 403, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			seen := []string{}
			client := &http.Client{Transport: probeRoundTripper(func(req *http.Request) (*http.Response, error) {
				seen = append(seen, req.URL.String())
				if req.Header.Get("User-Agent") != "XMesh-Upstream-Probe/1.0" {
					t.Fatal("missing custom ip.sb user agent")
				}
				body, status := test.ip, 200
				if req.URL.String() == model.UpstreamMicrosoftProbeURL {
					body, status = "<html>Microsoft</html>", test.microsoft
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
			})}
			result := probeUpstreamHTTP(context.Background(), client, nil)
			if len(seen) != 2 || result[0].Success != test.ipOK || result[1].Success != test.microsoftOK {
				t.Fatalf("wrong results: %+v", result)
			}
			if !test.ipOK && result[0].ExitIP != "" {
				t.Fatal("invalid IP became an exit address")
			}
			if !test.microsoftOK && result[1].Error != "HTTP 403" {
				t.Fatalf("lost response error: %+v", result[1])
			}
		})
	}
}

func TestUpstreamAutomaticManualAndPeriodicScheduling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := New(runtimecfg.Config{Credential: "secret", Gateway: runtimecfg.Gateway{UpstreamProbeListen: "127.0.0.1:1", UpstreamProbeInterval: runtimecfg.Duration(time.Minute)}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.config = probeTestConfig()
	r.xrayReady.Store(true)
	r.xrayAppliedRevision.Store(6)
	r.reconcileUpstreamProbes(ctx)
	if len(r.upstreamProbeSnapshot(r.config)) != 0 {
		t.Fatal("tested before configuration applied")
	}
	r.xrayAppliedRevision.Store(7)
	r.reconcileUpstreamProbes(ctx)
	waitProbes := func() []model.UpstreamProbeStatus {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
			results := r.upstreamProbeSnapshot(r.config)
			if len(results) == 2 && !results[0].CompletedAt.IsZero() && !results[1].CompletedAt.IsZero() {
				return results
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("probes did not complete")
		return nil
	}
	first := waitProbes()
	r.reconcileUpstreamProbes(ctx)
	if got := waitProbes(); !got[0].StartedAt.Equal(first[0].StartedAt) {
		t.Fatal("heartbeat duplicated automatic tests")
	}
	r.mu.Lock()
	r.config.Upstreams[0].ProbeRequestID = "manual"
	r.mu.Unlock()
	r.reconcileUpstreamProbes(ctx)
	manual := waitProbes()
	if manual[0].RequestID != "manual" || !manual[1].StartedAt.Equal(first[1].StartedAt) {
		t.Fatal("manual request tested unrelated route")
	}
	r.probeMu.Lock()
	expired := r.probeResults["one"]
	expired.CompletedAt = time.Now().Add(-2 * time.Minute)
	r.probeResults["one"] = expired
	r.probeMu.Unlock()
	r.reconcileUpstreamProbes(ctx)
	periodic := waitProbes()
	if !periodic[0].StartedAt.After(manual[0].StartedAt) {
		t.Fatal("scheduled test did not run")
	}
	r.mu.Lock()
	r.config.Gateway.Enabled = false
	r.mu.Unlock()
	r.reconcileUpstreamProbes(ctx)
	if len(r.upstreamProbeSnapshot(r.config)) != 0 {
		t.Fatal("disabled Gateway retained active probe work")
	}
}
