package controller

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"xmesh/internal/auth"
	"xmesh/internal/model"
)

func TestQuickSetupRealityDistributesOnlyPublicKeyToAgent(t *testing.T) {
	server, store := testServer(t, nil)
	form := url.Values{"gateway_name": {"Gateway"}, "agent_name": {"Agent"}, "public_host": {"203.0.113.10"}, "vmess_port": {"8080"}, "vmess_path": {"/proxy"}, "allowed_cidrs": {"127.0.0.0/8"}, "link_url": {"reality://203.0.113.10:8443/tunnel"}, "reality_target": {"www.example.com:443"}}
	request := httptest.NewRequest(http.MethodPost, "/admin/quick-setup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.quickSetup(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("quick setup status=%d: %s", response.Code, response.Body.String())
	}
	snapshot := store.Snapshot()
	var agentID, privateKey, publicKey string
	for id := range snapshot.Agents {
		agentID = id
	}
	for _, gateway := range snapshot.Gateways {
		privateKey, publicKey = gateway.RealityPrivateKey, gateway.RealityPublicKey
	}
	if privateKey == "" || publicKey == "" {
		t.Fatal("quick setup did not generate REALITY key pair")
	}
	if err := store.Update(func(state *model.State) error {
		agent := state.Agents[agentID]
		agent.CredentialHash = auth.SecretHash("agent-secret")
		state.Agents[agentID] = agent
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	request.Header.Set("Authorization", "Bearer agent-secret")
	response = httptest.NewRecorder()
	server.nodeConfig(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("agent config status=%d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), privateKey) {
		t.Fatal("Gateway REALITY private key leaked to Agent")
	}
	var config AgentConfig
	if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Links) != 1 || config.Links[0].RealityPublicKey != publicKey || config.Links[0].RealityUUID == "" || config.Links[0].RealityShortID == "" {
		t.Fatalf("incomplete Agent REALITY config: %#v", config.Links)
	}
}

func TestProvisionRealityGatewayAndLink(t *testing.T) {
	state := model.NewState()
	state.Gateways["gateway"] = model.Gateway{ID: "gateway"}
	uuid, shortID, err := provisionReality(&state, "gateway", "www.example.com:443", "reality://203.0.113.10:8443/tunnel")
	if err != nil {
		t.Fatal(err)
	}
	gateway := state.Gateways["gateway"]
	if gateway.RealityName != "www.example.com" || gateway.RealityTarget != "www.example.com:443" || len(shortID) != 16 || uuid == "" {
		t.Fatalf("incomplete REALITY credentials: %#v", gateway)
	}
	if key, err := base64.RawURLEncoding.DecodeString(gateway.RealityPrivateKey); err != nil || len(key) != 32 {
		t.Fatal("invalid private key")
	}
	if key, err := base64.RawURLEncoding.DecodeString(gateway.RealityPublicKey); err != nil || len(key) != 32 {
		t.Fatal("invalid public key")
	}
	secondUUID, secondShortID, err := provisionReality(&state, "gateway", "www.example.com:443", "reality://203.0.113.10:8443/tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if secondUUID == uuid || secondShortID == shortID || state.Gateways["gateway"].RealityPrivateKey != gateway.RealityPrivateKey {
		t.Fatal("link credentials were reused or gateway key changed")
	}
}

func TestProvisionRealityRejectsInvalidEndpoints(t *testing.T) {
	for _, tc := range []struct{ target, url string }{
		{"www.example.com:443", "reality://203.0.113.10:8443/tunnel?unexpected=query"},
		{"www.example.com:443", "reality://203.0.113.10:8443"},
		{"www.example.com:80", "reality://203.0.113.10:8443/tunnel"},
		{"127.0.0.1:443", "reality://203.0.113.10:8443/tunnel"},
	} {
		state := model.NewState()
		state.Gateways["gateway"] = model.Gateway{ID: "gateway"}
		if _, _, err := provisionReality(&state, "gateway", tc.target, tc.url); err == nil {
			t.Fatalf("accepted target=%q url=%q", tc.target, tc.url)
		}
	}
}
