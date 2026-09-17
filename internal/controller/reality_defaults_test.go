package controller

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"xmesh/internal/model"
)

func TestRealityRegionDefaultsAndExistingTarget(t *testing.T) {
	for _, tc := range []struct{ region, target, want string }{
		{"cn", "", "api.bilibili.com:443"},
		{"overseas", "", "www.swift.com:443"},
		{"cn", "custom.example:443", "custom.example:443"},
	} {
		t.Run(tc.region+tc.target, func(t *testing.T) {
			state := model.NewState()
			state.Gateways["g"] = model.Gateway{ID: "g", Region: tc.region}
			if _, _, err := provisionReality(&state, "g", tc.target, "reality://edge.example:8443/tunnel"); err != nil {
				t.Fatal(err)
			}
			if state.Gateways["g"].RealityTarget != tc.want {
				t.Fatal("incorrect region default")
			}
			gateway := state.Gateways["g"]
			gateway.Region = "overseas"
			state.Gateways["g"] = gateway
			if _, _, err := provisionReality(&state, "g", "", "reality://edge.example:8443/tunnel"); err != nil {
				t.Fatal(err)
			}
			if state.Gateways["g"].RealityTarget != tc.want {
				t.Fatal("region change overwrote existing target")
			}
		})
	}
	state := model.NewState()
	state.Gateways["g"] = model.Gateway{ID: "g"}
	if _, _, err := provisionReality(&state, "g", "", "reality://edge.example:8443/tunnel"); err == nil {
		t.Fatal("unknown region silently guessed")
	}
}

func TestAssignMixedRegionsUsesIndividualDefaults(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Agents["a"] = model.Agent{ID: "a", Enabled: true}
		for _, region := range []string{"cn", "overseas"} {
			s.Gateways[region] = model.Gateway{ID: region, Region: region, Name: region, PublicHost: region + ".example", Enabled: true}
		}
		return nil
	})
	response := postNodeForm("/admin/agents/a/assign-gateways", url.Values{"gateway_id": {"cn", "overseas"}}, server.assignGateways)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("assignment: %d %s", response.Code, response.Body.String())
	}
	for _, region := range []string{"cn", "overseas"} {
		if state.Snapshot().Gateways[region].RealityTarget != defaultRealityTarget(region) {
			t.Fatalf("wrong target for %s", region)
		}
	}
}

func TestGatewayRegionSavedAndLegacyEditPreservesRegion(t *testing.T) {
	server, state := testServer(t, nil)
	values := url.Values{"name": {"Gateway"}, "public_host": {"edge.example"}, "region": {"cn"}, "vmess_port": {"8080"}, "vmess_path": {"/proxy"}}
	request := httptest.NewRequest(http.MethodPost, "/admin/gateways", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.createGateway(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", response.Code)
	}
	var id string
	for key, gateway := range state.Snapshot().Gateways {
		id = key
		if gateway.Region != "cn" {
			t.Fatal("region not persisted")
		}
	}
	values.Del("region")
	response = postNodeForm("/admin/gateways/"+id+"/edit", values, server.editGateway)
	if response.Code != http.StatusSeeOther || state.Snapshot().Gateways[id].Region != "cn" {
		t.Fatal("legacy edit erased region")
	}
	values.Set("region", "unknown")
	response = postNodeForm("/admin/gateways/"+id+"/edit", values, server.editGateway)
	if response.Code != http.StatusBadRequest || state.Snapshot().Gateways[id].Region != "cn" {
		t.Fatal("invalid region accepted")
	}
}
