package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/controller"
	"github.com/Purestreams/xmesh/internal/model"
)

func TestRealXrayUpstreamProbesSelectExactOutboundWithoutGrants(t *testing.T) {
	xray := os.Getenv("XMESH_TEST_XRAY")
	if xray == "" {
		t.Skip("XMESH_TEST_XRAY not set")
	}
	probeAddress := freeTCPAddress(t)
	config := controller.GatewayConfig{Gateway: model.Gateway{VMessDisabled: true}}
	var fixtures []*httptest.Server
	for i, exitIP := range []string{"8.8.8.8", "1.1.1.1"} {
		fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ip" {
				_, _ = fmt.Fprintln(w, exitIP)
			} else {
				_, _ = fmt.Fprintln(w, "Microsoft test response")
			}
		}))
		defer fixture.Close()
		fixtures = append(fixtures, fixture)
		address := freeTCPAddress(t)
		host, portText, _ := net.SplitHostPort(address)
		port, _ := strconv.Atoi(portText)
		uuid := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		upstreamJSON, _ := json.Marshal(map[string]any{
			"log":       map[string]string{"loglevel": "warning"},
			"inbounds":  []any{map[string]any{"listen": host, "port": port, "protocol": "vmess", "settings": map[string]any{"clients": []any{map[string]any{"id": uuid}}}}},
			"outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{"redirect": strings.TrimPrefix(fixture.URL, "https://"), "finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.0/8"}}}}}},
		})
		process := startTestXray(t, xray, upstreamJSON)
		defer stopTestProcess(process)
		waitTCP(t, address, 5*time.Second)
		config.Upstreams = append(config.Upstreams, controller.GatewayUpstreamConfig{AttachmentID: fmt.Sprintf("route-%d", i), Endpoint: model.VMessEndpoint{Address: host, Port: port, UUID: uuid, Network: "tcp"}})
	}
	base, err := buildXrayConfig(config, freeTCPAddress(t))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := addUpstreamProbeInbound(base, config, probeAddress, "node-secret")
	if err != nil {
		t.Fatal(err)
	}
	process := startTestXray(t, xray, payload)
	defer stopTestProcess(process)
	waitTCP(t, probeAddress, 5*time.Second)
	for i, upstream := range config.Upstreams {
		client, transport, err := upstreamProbeClient(probeAddress, "node-secret", upstream.AttachmentID)
		if err != nil {
			t.Fatal(err)
		}
		// Trust only the local HTTPS fixture, retaining certificate validation.
		transport.TLSClientConfig = fixtures[i].Client().Transport.(*http.Transport).TLSClientConfig.Clone()
		transport.TLSClientConfig.ServerName = "example.com"
		transport.TLSHandshakeTimeout = 2 * time.Second
		client.Timeout = 3 * time.Second
		results := probeUpstreamHTTP(context.Background(), client, nil)
		transport.CloseIdleConnections()
		if !results[0].Success || !results[1].Success || results[0].ExitIP != []string{"8.8.8.8", "1.1.1.1"}[i] {
			t.Fatalf("wrong real outbound %s: %+v", upstream.AttachmentID, results)
		}
	}
	client, transport, err := upstreamProbeClient(probeAddress, "wrong-secret", "route-0")
	if err != nil {
		t.Fatal(err)
	}
	results := probeUpstreamHTTP(context.Background(), client, nil)
	transport.CloseIdleConnections()
	if results[0].Success || results[1].Success {
		t.Fatal("unauthenticated probes reached an outbound")
	}
}
