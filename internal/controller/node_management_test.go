package controller

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"xmesh/internal/model"
)

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
