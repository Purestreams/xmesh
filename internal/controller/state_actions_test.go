package controller

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/Purestreams/xmesh/internal/model"
)

func TestNetworkStateActionsAreRetrySafeAndKeepLegacyToggles(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		handler func(*Server) http.HandlerFunc
		enabled func(model.State) bool
	}{
		{"gateway", "/admin/gateways/g/toggle", func(s *Server) http.HandlerFunc { return s.toggleGateway }, func(s model.State) bool { return s.Gateways["g"].Enabled }},
		{"agent", "/admin/agents/a/toggle", func(s *Server) http.HandlerFunc { return s.toggleAgent }, func(s model.State) bool { return s.Agents["a"].Enabled }},
		{"route", "/admin/attachments/route/toggle", func(s *Server) http.HandlerFunc { return s.toggleAttachment }, func(s model.State) bool { return s.Attachments["route"].Enabled }},
		{"link", "/admin/links/link/toggle", func(s *Server) http.HandlerFunc { return s.toggleLink }, func(s model.State) bool { return s.Links["link"].Enabled }},
		{"upstream", "/admin/upstreams/exit/toggle", func(s *Server) http.HandlerFunc { return s.toggleUpstream }, func(s model.State) bool { return s.Upstreams["exit"].Enabled }},
		{"embedded agent", "/admin/gateways/g/agent", func(s *Server) http.HandlerFunc { return s.toggleEmbeddedAgent }, func(s model.State) bool { return s.Gateways["g"].AgentEnabled }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, store := testServer(t, func(s *model.State) error {
				if err := securityState(s); err != nil {
					return err
				}
				gateway := s.Gateways["g"]
				gateway.AgentEnabled = true
				s.Gateways["g"] = gateway
				id := model.GatewayAgentID("g")
				s.Agents[id] = model.Agent{ID: id, GatewayID: "g", Enabled: true, DesiredVersion: 1}
				s.Upstreams["exit"] = model.VMessUpstream{ID: "exit", Enabled: true}
				s.Gateways["unrelated"] = model.Gateway{ID: "unrelated", Enabled: true, DesiredVersion: 7}
				return nil
			})
			original := store.Snapshot()
			post := func(values url.Values) {
				t.Helper()
				response := postNodeForm(tc.path, values, tc.handler(server))
				if response.Code != http.StatusSeeOther {
					t.Fatalf("action: %d %s", response.Code, response.Body.String())
				}
			}
			for _, value := range []string{"false", "true"} {
				post(url.Values{"enabled": {value}})
				first := store.Snapshot()
				if tc.enabled(first) != (value == "true") || first.Gateways["unrelated"].DesiredVersion != 7 {
					t.Fatal("action did not set the requested state or changed an unrelated node")
				}
				for id, grant := range first.Grants {
					before := original.Grants[id]
					if !grant.Enabled || grant.VMessUUID != before.VMessUUID || grant.SOCKSPassword != before.SOCKSPassword || grant.HY2Auth != before.HY2Auth {
						t.Fatal("network action changed user selections or credentials")
					}
				}
				post(url.Values{"enabled": {value}})
				retried := store.Snapshot()
				first.Revision = retried.Revision
				if !reflect.DeepEqual(first, retried) {
					t.Fatal("retry reversed the action, changed credentials or issued another configuration update")
				}
			}
			for _, values := range []url.Values{{"enabled": {""}}, {"enabled": {"invalid"}}, {"enabled": {"true", "false"}}} {
				before := store.Snapshot()
				response := postNodeForm(tc.path, values, tc.handler(server))
				if response.Code != http.StatusBadRequest || !reflect.DeepEqual(before, store.Snapshot()) {
					t.Fatal("invalid desired state changed configuration")
				}
			}
			post(nil)
			if tc.enabled(store.Snapshot()) {
				t.Fatal("legacy toggle no longer disables")
			}
			post(nil)
			if !tc.enabled(store.Snapshot()) {
				t.Fatal("legacy toggle no longer enables")
			}
		})
	}
}

func TestDashboardDistinguishesConfiguredAgentStateFromGatewayAvailability(t *testing.T) {
	server, _ := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Enabled: false, AgentEnabled: true}
		id := model.GatewayAgentID("g")
		s.Agents[id] = model.Agent{ID: id, GatewayID: "g", Enabled: true}
		return nil
	})
	found := false
	for _, node := range dashboardRequest(t, server).Nodes {
		if node.Role == "Agent" {
			found = true
			if node.Enabled || !node.ConfiguredEnabled {
				t.Fatal("physical Gateway outage changed the Agent's configured state")
			}
		}
	}
	if !found {
		t.Fatal("embedded Agent missing from dashboard")
	}
}
