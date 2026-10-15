package gateway

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Purestreams/xmesh/internal/controller"
	"github.com/Purestreams/xmesh/internal/model"
)

func TestRealityGatewayConfigAcceptedByXray(t *testing.T) {
	binary := os.Getenv("XMESH_TEST_XRAY")
	if binary == "" {
		t.Skip("XMESH_TEST_XRAY not set")
	}
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	config := controller.GatewayConfig{Gateway: model.Gateway{VMessPort: 18000, VMessPath: "/proxy", RealityTarget: "example.com:443", RealityName: "example.com", RealityPrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes())}, Links: []controller.GatewayLinkConfig{{Link: model.Link{ID: "link", Enabled: true, RealityUUID: "00000000-0000-4000-8000-000000000001", RealityShortID: "0123456789abcdef"}}}}
	for _, backend := range []string{"127.0.0.1:18081", "localhost:18081", "backend.example:18081", "[::1]:18081", ":18081", "0.0.0.0:18081", "[::]:18081"} {
		t.Run(backend, func(t *testing.T) {
			b, err := buildXrayConfigWithReality(config, "127.0.0.1:18080", "127.0.0.1:18443", backend)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "reality.json")
			if err := os.WriteFile(path, b, 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command(binary, "run", "-test", "-config", path).CombinedOutput()
			if err != nil {
				t.Fatalf("Xray rejected gateway REALITY config: %v\n%s", err, output)
			}
		})
	}
}

func TestRealityTunnelBackendRedirectIsFixed(t *testing.T) {
	for _, test := range []struct{ backend, redirect, ip string }{
		{"127.0.0.1:18081", "127.0.0.1:18081", "127.0.0.1"},
		{"localhost:18081", "localhost:18081", ""},
		{"backend.example:18081", "backend.example:18081", ""},
		{"[::1]:18081", "[::1]:18081", "::1"},
		{":18081", "127.0.0.1:18081", "127.0.0.1"},
		{"0.0.0.0:18081", "127.0.0.1:18081", "127.0.0.1"},
		{"[::]:18081", "[::1]:18081", "::1"},
	} {
		t.Run(test.backend, func(t *testing.T) {
			config := controller.GatewayConfig{Gateway: model.Gateway{VMessPort: 8080, VMessPath: "/proxy", RealityPrivateKey: "key"}, Links: []controller.GatewayLinkConfig{{Link: model.Link{ID: "link", Enabled: true, RealityUUID: "uuid", RealityShortID: "short-id"}}}}
			payload, err := buildXrayConfigWithReality(config, "127.0.0.1:18080", "127.0.0.1:18443", test.backend)
			if err != nil {
				t.Fatal(err)
			}
			var rendered xrayConfig
			if err := json.Unmarshal(payload, &rendered); err != nil {
				t.Fatal(err)
			}
			for _, outbound := range rendered.Outbounds {
				if outbound.Tag != "xmesh-reality-local" {
					continue
				}
				if outbound.Settings.Redirect != test.redirect || len(outbound.Settings.FinalRules) != 1 {
					t.Fatal("tunnel outbound lost its fixed backend")
				}
				rule := outbound.Settings.FinalRules[0]
				if rule.Action != "allow" || rule.Network != "tcp" || rule.Port != "18081" {
					t.Fatal("tunnel exception lost its network or port constraint")
				}
				if test.ip == "" && len(rule.IP) != 0 || test.ip != "" && (len(rule.IP) != 1 || rule.IP[0] != test.ip) {
					t.Fatalf("invalid tunnel IP rule: %v", rule.IP)
				}
				return
			}
			t.Fatal("missing tunnel outbound")
		})
	}
}

func TestBuildXrayConfigUsesOneVMessInboundAndPerGrantRoutes(t *testing.T) {
	config := controller.GatewayConfig{Gateway: model.Gateway{VMessPort: 8080, VMessPath: "/proxy"}, Grants: []model.Grant{{ID: "b", VMessUUID: "uuid-b", SOCKSUsername: "b", SOCKSPassword: "pb", Enabled: true}, {ID: "a", VMessUUID: "uuid-a", SOCKSUsername: "a", SOCKSPassword: "pa", Enabled: true}}}
	b, err := buildXrayConfig(config, "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	var decoded xrayConfig
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Inbounds) != 1 || decoded.Inbounds[0].Port != 8080 || decoded.Inbounds[0].StreamSettings.Security != "none" || decoded.Inbounds[0].StreamSettings.Network != "ws" {
		t.Fatalf("unexpected inbound %#v", decoded.Inbounds)
	}
	if len(decoded.Inbounds[0].Settings.Clients) != 2 || len(decoded.Outbounds) != 3 || len(decoded.Routing.Rules) != 3 {
		t.Fatalf("incomplete Xray config: %s", b)
	}
	if decoded.Inbounds[0].Settings.Clients[0].Email != "grant-a" {
		t.Fatal("grants are not stable-sorted")
	}
}

func TestGeneratedConfigAcceptedByXray(t *testing.T) {
	binary := os.Getenv("XMESH_TEST_XRAY")
	if binary == "" {
		t.Skip("XMESH_TEST_XRAY not set")
	}
	config := controller.GatewayConfig{Gateway: model.Gateway{VMessPort: 18000, VMessPath: "/proxy"}, Grants: []model.Grant{{ID: "grant", VMessUUID: "00000000-0000-4000-8000-000000000001", SOCKSUsername: "grant", SOCKSPassword: "password", Enabled: true}}}
	b, err := buildXrayConfig(config, "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "xray.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(binary, "run", "-test", "-config", path).CombinedOutput()
	if err != nil {
		t.Fatalf("Xray rejected generated config: %v\n%s\nconfig:\n%s", err, output, b)
	}
}

func TestXrayRenderingIgnoresCollectionOrderWithoutMutatingConfig(t *testing.T) {
	config := controller.GatewayConfig{
		Gateway:   model.Gateway{VMessPort: 8080, VMessPath: "/proxy", RealityPrivateKey: "key", RealityTarget: "example.com:443", RealityName: "example.com"},
		Grants:    []model.Grant{{ID: "b", AttachmentID: "route-b", Enabled: true, VMessUUID: "uuid-b"}, {ID: "a", AttachmentID: "route-a", Enabled: true, VMessUUID: "uuid-a"}},
		Links:     []controller.GatewayLinkConfig{{Link: model.Link{ID: "b", Enabled: true, RealityUUID: "link-b", RealityShortID: "bb"}}, {Link: model.Link{ID: "a", Enabled: true, RealityUUID: "link-a", RealityShortID: "aa"}}},
		Upstreams: []controller.GatewayUpstreamConfig{{AttachmentID: "route-b", Endpoint: model.VMessEndpoint{Address: "b.example", Port: 443, UUID: "upstream-b", Network: "ws"}}, {AttachmentID: "route-a", Endpoint: model.VMessEndpoint{Address: "a.example", Port: 443, UUID: "upstream-a", Network: "ws"}}},
	}
	first, err := buildXrayConfigWithReality(config, "127.0.0.1:18080", "127.0.0.1:18443", "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	if config.Grants[0].ID != "b" || config.Links[0].ID != "b" || config.Upstreams[0].AttachmentID != "route-b" {
		t.Fatal("rendering reordered caller-owned collections")
	}
	config.Grants[0], config.Grants[1] = config.Grants[1], config.Grants[0]
	config.Links[0], config.Links[1] = config.Links[1], config.Links[0]
	config.Upstreams[0], config.Upstreams[1] = config.Upstreams[1], config.Upstreams[0]
	second, err := buildXrayConfigWithReality(config, "127.0.0.1:18080", "127.0.0.1:18443", "127.0.0.1:18081")
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("collection order changed the running payload: %v", err)
	}
}
