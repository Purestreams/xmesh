package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
)

func TestEditGatewayRealityPortUpdatesOnlyAutomaticLinks(t *testing.T) {
	server, db := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Name: "Edge", PublicHost: "edge.example", VMessPort: 8080, VMessPath: "/proxy", Enabled: true, CredentialHash: auth.SecretHash("gateway-secret"), DesiredVersion: 3}
		s.Agents["a"] = model.Agent{ID: "a", Enabled: true, CredentialHash: auth.SecretHash("agent-secret"), DesiredVersion: 7}
		s.Attachments["route"] = model.Attachment{ID: "route", GatewayID: "g", AgentID: "a", Enabled: true}
		s.Links["auto"] = model.Link{ID: "auto", AttachmentID: "route", URL: "reality://edge.example:8443/tunnel", AutoURL: true, Enabled: true}
		s.Links["legacy"] = model.Link{ID: "legacy", AttachmentID: "route", URL: "reality://edge.example:8443/tunnel"}
		s.Links["manual"] = model.Link{ID: "manual", AttachmentID: "route", URL: "reality://edge.example:8443/tunnel", URLOverride: true}
		s.Links["wss"] = model.Link{ID: "wss", AttachmentID: "route", URL: "wss://edge.example/tunnel"}
		s.Grants["grant"] = model.Grant{ID: "grant", AttachmentID: "route", Published: true}
		return nil
	})
	values := url.Values{"name": {"Edge"}, "public_host": {"edge.example"}, "vmess_port": {"8080"}, "vmess_path": {"/proxy"}, "reality_port": {"9443"}}
	edit := func() *httptest.ResponseRecorder {
		return postNodeForm("/admin/gateways/g/edit", values, server.editGateway)
	}
	before := db.Snapshot()
	if got := edit(); got.Code != http.StatusBadRequest || !reflect.DeepEqual(db.Snapshot(), before) {
		t.Fatalf("older Gateway changed unexpectedly: %d %s", got.Code, got.Body)
	}
	if err := db.Update(func(s *model.State) error { s.NodeStatus["g"] = model.NodeStatus{RealityPortControl: true}; return nil }); err != nil {
		t.Fatal(err)
	}
	before = db.Snapshot()
	for _, invalid := range []string{"0", "65536", "8080"} {
		values.Set("reality_port", invalid)
		if got := edit(); got.Code != http.StatusBadRequest || !reflect.DeepEqual(db.Snapshot(), before) {
			t.Fatalf("invalid port %s changed configuration: %d %s", invalid, got.Code, got.Body)
		}
	}
	values.Set("reality_port", "9443")
	if got := edit(); got.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d %s", got.Code, got.Body)
	}
	after := db.Snapshot()
	if after.Gateways["g"].RealityPort != 9443 || after.Gateways["g"].DesiredVersion != 4 || after.Agents["a"].DesiredVersion != 7 || !after.Grants["grant"].Published {
		t.Fatal("port change did not stay pending until Gateway confirmation, or changed user publication")
	}
	for _, id := range []string{"auto", "legacy"} {
		if after.Links[id].URL != "reality://edge.example:9443/tunnel" || !after.Links[id].AutoURL {
			t.Fatalf("%s did not follow the Gateway: %+v", id, after.Links[id])
		}
	}
	for _, id := range []string{"manual", "wss"} {
		if after.Links[id] != before.Links[id] {
			t.Fatalf("manual Link %s changed", id)
		}
	}
	if got := edit(); got.Code != http.StatusSeeOther || db.Snapshot().Gateways["g"].DesiredVersion != 4 || db.Snapshot().Agents["a"].DesiredVersion != 7 {
		t.Fatal("saving the same port advanced node versions")
	}
	getConfig := func(secret string, config any) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
		request.Header.Set("Authorization", "Bearer "+secret)
		response := httptest.NewRecorder()
		server.nodeConfig(response, request)
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), config) != nil {
			t.Fatalf("cannot fetch node config: %d %s", response.Code, response.Body)
		}
	}
	report := func(applied uint64, ready bool) {
		t.Helper()
		payload, _ := json.Marshal(StatusReport{Status: model.NodeStatus{AppliedVersion: applied, XrayReady: ready, RealityPortControl: true}})
		request := httptest.NewRequest(http.MethodPost, "/api/v1/status", bytes.NewReader(payload))
		request.Header.Set("Authorization", "Bearer gateway-secret")
		response := httptest.NewRecorder()
		server.nodeStatus(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("cannot report node status: %d %s", response.Code, response.Body)
		}
	}
	var gatewayConfig GatewayConfig
	getConfig("gateway-secret", &gatewayConfig)
	if gatewayConfig.Gateway.RealityPort != 9443 || gatewayConfig.Links[0].URL != "reality://edge.example:8443/tunnel" {
		t.Fatal("Gateway did not receive new listener port with the old dial URL")
	}
	report(3, true)
	report(4, false)
	var agentConfig AgentConfig
	getConfig("agent-secret", &agentConfig)
	if agentConfig.Revision != 7 || len(agentConfig.Links) != 1 || agentConfig.Links[0].URL != "reality://edge.example:8443/tunnel" {
		t.Fatal("Agent switched before Gateway was ready")
	}
	report(4, true)
	report(4, true)
	getConfig("agent-secret", &agentConfig)
	if agentConfig.Revision != 8 || agentConfig.Links[0].URL != "reality://edge.example:9443/tunnel" {
		t.Fatal("confirmed port did not propagate once to Agent")
	}
	values.Set("reality_port", "")
	if got := edit(); got.Code != http.StatusSeeOther || db.Snapshot().Gateways["g"].RealityPort != 0 || db.Snapshot().Links["auto"].URL != "reality://edge.example:8443/tunnel" {
		t.Fatal("clearing the port did not restore local/default behavior")
	}
	pending := db.Snapshot().Gateways["g"].DesiredVersion
	for _, enabled := range []string{"false", "true"} {
		if got := postNodeForm("/admin/gateways/g/toggle", url.Values{"enabled": {enabled}}, server.toggleGateway); got.Code != http.StatusSeeOther {
			t.Fatalf("set Gateway enabled=%s: %d %s", enabled, got.Code, got.Body)
		}
		if enabled == "false" {
			values.Set("reality_port", "10443")
			if got := edit(); got.Code != http.StatusSeeOther {
				t.Fatalf("edit disabled Gateway: %d %s", got.Code, got.Body)
			}
		}
	}
	report(pending, true)
	getConfig("agent-secret", &agentConfig)
	if agentConfig.Revision != 8 || agentConfig.Links[0].URL != "reality://edge.example:9443/tunnel" {
		t.Fatal("old acknowledgement released an unconfirmed replacement port")
	}
	report(db.Snapshot().Gateways["g"].DesiredVersion, true)
	getConfig("agent-secret", &agentConfig)
	if agentConfig.Revision != 9 || agentConfig.Links[0].URL != "reality://edge.example:10443/tunnel" {
		t.Fatal("latest acknowledgement did not release the replacement port")
	}
}

func postNodeForm(path string, values url.Values, handler http.HandlerFunc) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	parts := strings.Split(path, "/")
	request.SetPathValue("id", parts[3])
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func TestEditGatewayAndAgent(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Name: "old gateway", PublicHost: "old.example", VMessPort: 8080, VMessPath: "/proxy", DesiredVersion: 2}
		s.Agents["a"] = model.Agent{ID: "a", Name: "old agent", DesiredVersion: 3}
		return nil
	})
	response := postNodeForm("/admin/gateways/g/edit", url.Values{"name": {"HK-Mega"}, "public_host": {"edge.example"}, "vmess_port": {"8086"}, "vmess_path": {"/speedtest"}}, server.editGateway)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("gateway edit: %d %s", response.Code, response.Body.String())
	}
	response = postNodeForm("/admin/agents/a/edit", url.Values{"name": {"Tencent-Beijing-10M"}, "allowed_cidrs": {"0.0.0.0/0"}}, server.editAgent)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("agent edit: %d %s", response.Code, response.Body.String())
	}
	snapshot := state.Snapshot()
	if g := snapshot.Gateways["g"]; g.Name != "HK-Mega" || g.VMessPort != 8086 || g.VMessPath != "/speedtest" || g.DesiredVersion != 3 {
		t.Fatalf("gateway not updated: %+v", g)
	}
	if a := snapshot.Agents["a"]; a.Name != "Tencent-Beijing-10M" || a.DesiredVersion != 4 {
		t.Fatalf("agent not updated: %+v", a)
	}
	response = postNodeForm("/admin/gateways/g/edit", url.Values{"name": {"bad"}, "public_host": {"edge.example"}, "vmess_port": {"65536"}, "vmess_path": {"/proxy"}}, server.editGateway)
	if response.Code != http.StatusBadRequest || state.Snapshot().Gateways["g"].Name != "HK-Mega" {
		t.Fatal("invalid port changed gateway")
	}
}

func TestDeleteGatewayCascadesOnlyItsRoutes(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["g1"] = model.Gateway{ID: "g1", Name: "remove"}
		s.Gateways["g2"] = model.Gateway{ID: "g2", Name: "keep", DesiredVersion: 1}
		s.Agents["remote"] = model.Agent{ID: "remote", DesiredVersion: 1}
		s.Attachments["n1"] = model.Attachment{ID: "n1", GatewayID: "g1", AgentID: "remote"}
		s.Attachments["n3"] = model.Attachment{ID: "n3", GatewayID: "g2", AgentID: "remote"}
		for _, id := range []string{"n1", "n3"} {
			s.Links[id] = model.Link{ID: id, AttachmentID: id}
			s.Grants[id] = model.Grant{ID: id, AttachmentID: id}
		}
		s.Enrollments["e"] = model.Enrollment{ID: "e", NodeID: "g1"}
		s.NodeStatus["g1"] = model.NodeStatus{NodeID: "g1"}
		s.LinkStatus["g1/n1"] = model.LinkStatus{LinkID: "n1"}
		s.GrantStatus["g1/n1"] = model.GrantStatus{GrantID: "n1"}
		return nil
	})
	response := postNodeForm("/admin/gateways/g1/delete", url.Values{"confirm_name": {"wrong"}}, server.deleteGateway)
	if response.Code != http.StatusBadRequest || len(state.Snapshot().Gateways) != 2 {
		t.Fatal("incorrect confirmation deleted gateway")
	}
	response = postNodeForm("/admin/gateways/g1/delete", url.Values{"confirm_name": {"remove"}}, server.deleteGateway)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d %s", response.Code, response.Body.String())
	}
	snapshot := state.Snapshot()
	if len(snapshot.Gateways) != 1 || len(snapshot.Agents) != 1 || len(snapshot.Attachments) != 1 || len(snapshot.Links) != 1 || len(snapshot.Grants) != 1 || len(snapshot.Enrollments) != 0 || len(snapshot.NodeStatus) != 0 || len(snapshot.LinkStatus) != 0 || len(snapshot.GrantStatus) != 0 {
		t.Fatalf("stale references after delete: %+v", snapshot)
	}
	if snapshot.Gateways["g2"].DesiredVersion != 1 || snapshot.Agents["remote"].DesiredVersion != 2 {
		t.Fatal("surviving nodes not notified")
	}
}

func TestDeleteAgentPreservesGateway(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Name: "gateway", DesiredVersion: 1}
		s.Agents["a"] = model.Agent{ID: "a", Name: "agent"}
		s.Attachments["n"] = model.Attachment{ID: "n", GatewayID: "g", AgentID: "a"}
		s.Links["l"] = model.Link{ID: "l", AttachmentID: "n"}
		s.Grants["r"] = model.Grant{ID: "r", AttachmentID: "n"}
		return nil
	})
	response := postNodeForm("/admin/agents/a/delete", url.Values{"confirm_name": {"agent"}}, server.deleteAgent)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("delete agent: %d %s", response.Code, response.Body.String())
	}
	snapshot := state.Snapshot()
	if len(snapshot.Gateways) != 1 || snapshot.Gateways["g"].DesiredVersion != 2 || len(snapshot.Agents) != 0 || len(snapshot.Attachments) != 0 || len(snapshot.Links) != 0 || len(snapshot.Grants) != 0 {
		t.Fatalf("agent deletion left references: %+v", snapshot)
	}
}

func TestGatewayEditHoldsSubscriptionAndUpdatesGeneratedRealityAddress(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Name: "Gateway", PublicHost: "old.example", VMessPort: 8080, VMessPath: "/proxy", DesiredVersion: 1}
		s.Agents["a"] = model.Agent{ID: "a", Name: "Agent", DesiredVersion: 1}
		s.Attachments["n"] = model.Attachment{ID: "n", GatewayID: "g", AgentID: "a", Enabled: true}
		s.Links["l"] = model.Link{ID: "l", AttachmentID: "n", URL: "reality://old.example:8443/tunnel", Enabled: true}
		s.Grants["r"] = model.Grant{ID: "r", AttachmentID: "n", Enabled: true, Published: true}
		return nil
	})
	response := postNodeForm("/admin/gateways/g/edit", url.Values{"name": {"Gateway"}, "public_host": {"new.example"}, "vmess_port": {"8086"}, "vmess_path": {"/proxy"}}, server.editGateway)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d %s", response.Code, response.Body.String())
	}
	snapshot := state.Snapshot()
	if snapshot.Links["l"].URL != "reality://new.example:8443/tunnel" || snapshot.Agents["a"].DesiredVersion != 2 || snapshot.Grants["r"].Published {
		t.Fatalf("stale route or prematurely published grant: %+v", snapshot)
	}
}

func TestMetadataAndPolicySavesOnlyAdvanceAffectedRuntimeVersions(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Name: "Gateway", PublicHost: "edge.example", VMessPort: 8080, VMessPath: "/proxy", DesiredVersion: 1}
		s.Agents["a"] = model.Agent{ID: "a", Name: "Agent", AllowedCIDRs: []string{"127.0.0.0/8", "10.0.0.0/8"}, DesiredVersion: 1}
		s.Attachments["n"] = model.Attachment{ID: "n", GatewayID: "g", AgentID: "a", Enabled: true}
		s.Links["l"] = model.Link{ID: "l", Name: "Link", AttachmentID: "n", URL: "wss://edge.example/tunnel", TLSVerify: true, Priority: 10, Weight: 1, Connections: 2, MaxStreams: 16, Enabled: true}
		s.Grants["r"] = model.Grant{ID: "r", AttachmentID: "n", Enabled: true, Published: true}
		return nil
	})
	save := func(path string, values url.Values, handler http.HandlerFunc) {
		t.Helper()
		response := postNodeForm(path, values, handler)
		if response.Code != http.StatusSeeOther {
			t.Fatalf("save %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	versions := func(gateway, agent uint64) {
		t.Helper()
		snapshot := state.Snapshot()
		if snapshot.Gateways["g"].DesiredVersion != gateway || snapshot.Agents["a"].DesiredVersion != agent {
			t.Fatalf("unexpected versions: gateway=%d agent=%d", snapshot.Gateways["g"].DesiredVersion, snapshot.Agents["a"].DesiredVersion)
		}
	}
	gateway := url.Values{"name": {"Renamed gateway"}, "region": {"overseas"}, "public_host": {"edge.example"}, "vmess_port": {"8080"}, "vmess_path": {"/proxy"}}
	agent := url.Values{"name": {"Renamed agent"}, "allowed_cidrs": {"10.0.0.1/8,127.0.0.0/8,10.0.0.0/8"}}
	link := url.Values{"name": {"Renamed link"}, "url": {"wss://edge.example/tunnel"}, "tls_verify": {"on"}}
	policy := url.Values{"priority": {"10"}, "weight": {"1"}, "connections": {"2"}, "max_streams": {"16"}}
	for range 2 {
		save("/admin/gateways/g/edit", gateway, server.editGateway)
		save("/admin/agents/a/edit", agent, server.editAgent)
		save("/admin/links/l/edit", link, server.editLink)
		save("/admin/links/l/policy", policy, server.updateLinkPolicy)
		versions(1, 1)
	}
	if !state.Snapshot().Grants["r"].Published || state.Snapshot().Gateways["g"].Name != "Renamed gateway" || state.Snapshot().Agents["a"].Name != "Renamed agent" || state.Snapshot().Links["l"].Name != "Renamed link" {
		t.Fatal("metadata save did not persist names or unnecessarily withdrew the subscription")
	}
	policy.Set("weight", "3")
	policy.Set("max_streams", "8")
	save("/admin/links/l/policy", policy, server.updateLinkPolicy)
	versions(2, 1)
	policy.Set("connections", "3")
	save("/admin/links/l/policy", policy, server.updateLinkPolicy)
	versions(2, 2)
	agent.Set("denied_cidrs", "127.0.0.0/8")
	save("/admin/agents/a/edit", agent, server.editAgent)
	versions(2, 3)
	link.Set("url", "wss://new.example/tunnel")
	save("/admin/links/l/edit", link, server.editLink)
	versions(3, 4)
	gateway.Set("vmess_port", "8081")
	save("/admin/gateways/g/edit", gateway, server.editGateway)
	versions(4, 4)
	if state.Snapshot().Grants["r"].Published {
		t.Fatal("connection change left a stale subscription published")
	}
}
