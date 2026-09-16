package gateway

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xmesh/internal/controller"
	"xmesh/internal/model"
)

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
	if len(decoded.Inbounds[0].Settings.Clients) != 2 || len(decoded.Outbounds) != 2 || len(decoded.Routing.Rules) != 2 {
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
