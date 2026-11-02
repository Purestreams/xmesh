package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
)

func TestEmbeddedAgentLifecycleAndAuthentication(t *testing.T) {
	s, store := testServer(t, func(state *model.State) error {
		state.Gateways["g"] = model.Gateway{ID: "g", Name: "host", Enabled: true, CredentialHash: auth.SecretHash("gateway-secret")}
		state.Gateways["other"] = model.Gateway{ID: "other", Enabled: true, CredentialHash: auth.SecretHash("other-secret")}
		return nil
	})
	toggle := func() {
		r := httptest.NewRequest("POST", "/admin/gateways/g/agent", nil)
		r.SetPathValue("id", "g")
		w := httptest.NewRecorder()
		s.toggleEmbeddedAgent(w, r)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("toggle: %d %s", w.Code, w.Body.String())
		}
	}
	toggle()
	id := model.GatewayAgentID("g")
	state := store.Snapshot()
	if a := state.Agents[id]; !a.Enabled || a.GatewayID != "g" || a.CredentialHash != "" || len(standaloneAgents(state)) != 0 || nodeExists(&state, model.RoleAgent, id) {
		t.Fatalf("embedded exit treated as a separate installation: %+v", a)
	}
	if err := store.Update(func(state *model.State) error {
		state.Users["u"] = model.User{ID: "u", Enabled: true}
		for _, gateway := range []string{"g", "other"} {
			state.Attachments[gateway] = model.Attachment{ID: gateway, GatewayID: gateway, AgentID: id, Enabled: true}
			state.Links[gateway] = model.Link{ID: gateway, AttachmentID: gateway, URL: "reality://" + gateway + ":8443/tunnel", Enabled: true}
			state.Grants[gateway] = model.Grant{ID: gateway, UserID: "u", AttachmentID: gateway, Enabled: true, Published: true}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fetch := func(secret string, embedded bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/config", nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		if embedded {
			r.Header.Set("X-XMesh-Embedded-Agent", "1")
		}
		w := httptest.NewRecorder()
		s.nodeConfig(w, r)
		return w
	}
	var cfg AgentConfig
	if w := fetch("gateway-secret", true); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &cfg) != nil || cfg.Agent.ID != id || len(cfg.Links) != 2 || len(cfg.GrantIDs) != 2 {
		t.Fatalf("missing own or remote Gateway routes: %d %s", w.Code, w.Body.String())
	}
	if w := fetch("other-secret", true); strings.Contains(w.Body.String(), "gateway-secret") || strings.Contains(w.Body.String(), `"id":"`+id+`"`) {
		t.Fatal("another Gateway obtained the embedded Agent configuration")
	}
	if w := fetch("gateway-secret", false); !strings.Contains(w.Body.String(), `"gateway":`) || strings.Contains(w.Body.String(), `"agent":`) {
		t.Fatal("ordinary Gateway requests changed roles")
	}
	for _, reportedID := range []string{id, "other"} {
		payload, _ := json.Marshal(StatusReport{Status: model.NodeStatus{NodeID: reportedID, Role: model.RoleAgent, Online: true}, Links: []model.LinkStatus{{LinkID: "other", Online: true, Ready: true}}})
		r := httptest.NewRequest("POST", "/api/v1/status", bytes.NewReader(payload))
		r.Header.Set("Authorization", "Bearer gateway-secret")
		r.Header.Set("X-XMesh-Embedded-Agent", "1")
		w := httptest.NewRecorder()
		s.nodeStatus(w, r)
		if reportedID == id && w.Code != 204 || reportedID != id && w.Code != 403 {
			t.Fatalf("wrong status identity accepted or rejected: %s %d %s", reportedID, w.Code, w.Body.String())
		}
	}
	form := url.Values{"role": {"agent"}, "node_id": {id}}
	r := httptest.NewRequest("POST", "/admin/enrollments", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.createEnrollment(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("embedded Agent enrollment was allowed: %d", w.Code)
	}
	toggle()
	cfg = AgentConfig{}
	if w := fetch("gateway-secret", true); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &cfg) != nil || cfg.Agent.Enabled || len(cfg.Links) != 0 {
		t.Fatalf("disabled exit retained routes: %s", w.Body.String())
	}
	state = store.Snapshot()
	for _, grant := range state.Grants {
		if grant.Published || routeAvailable(&state, state.Attachments[grant.AttachmentID]) {
			t.Fatal("disabled embedded exit stayed published")
		}
	}
	toggle()
	r = httptest.NewRequest("POST", "/admin/gateways/g/delete", strings.NewReader("confirm_name=host"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("id", "g")
	w = httptest.NewRecorder()
	s.deleteGateway(w, r)
	state = store.Snapshot()
	if w.Code != 303 || len(state.Agents) != 0 || len(state.Attachments) != 0 || len(state.Grants) != 0 {
		t.Fatalf("orphaned embedded exit after deleting parent: %d %+v", w.Code, state.Agents)
	}
}

func TestEmbeddedExitParticipatesInParentUpgradeRecovery(t *testing.T) {
	now := time.Now().UTC()
	s, _ := testServer(t, nil)
	state := model.NewState()
	id := model.GatewayAgentID("owner")
	state.Agents[id] = model.Agent{ID: id, GatewayID: "owner"}
	state.Attachments["remote"] = model.Attachment{ID: "remote", GatewayID: "remote", AgentID: id, Enabled: true}
	state.Links["l"] = model.Link{ID: "l", AttachmentID: "remote", Enabled: true}
	state.Users["u"] = model.User{ID: "u"}
	state.Grants["grant"] = model.Grant{ID: "grant", UserID: "u", AttachmentID: "remote"}
	if impact := nodeImpacts(state)["owner"]; impact.Links != 1 || impact.Users != 1 || impact.Grants != 1 {
		t.Fatalf("parent upgrade omitted its exit users: %+v", impact)
	}
	task := model.UpgradeTask{NodeID: "owner", BaselineLinks: []string{"l"}, UpdatedAt: now}
	for _, endpoint := range []string{"remote", id} {
		state.LinkStatus[endpoint+"/l"] = model.LinkStatus{LinkID: "l", Online: true, Ready: true, LastSeen: now.Add(time.Second)}
	}
	if problem := s.upgradeRecoveryProblem(&state, task, now.Add(2*time.Second)); problem != "" {
		t.Fatal(problem)
	}
	delete(state.LinkStatus, id+"/l")
	if problem := s.upgradeRecoveryProblem(&state, task, now.Add(2*time.Second)); problem == "" {
		t.Fatal("parent upgrade ignored its embedded exit's missing report")
	}
}

func TestEmbeddedAgentParentAvailabilityAcrossManagementViews(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	id := model.GatewayAgentID("owner")
	s, store := testServer(t, func(state *model.State) error {
		state.Gateways["owner"] = model.Gateway{ID: "owner", Enabled: true, AgentEnabled: true}
		for _, gateway := range []string{"remote", "spare"} {
			state.Gateways[gateway] = model.Gateway{ID: gateway, Enabled: true, PublicHost: gateway + ".example"}
		}
		state.Agents[id] = model.Agent{ID: id, GatewayID: "owner", Enabled: true}
		state.Attachments["route"] = model.Attachment{ID: "route", GatewayID: "remote", AgentID: id, Enabled: true}
		state.Links["link"] = model.Link{ID: "link", AttachmentID: "route", Enabled: true}
		state.Monitor = model.MonitorSettings{Enabled: true, Nodes: map[string]string{"remote": "Ingress", id: "Exit"}, Links: map[string]string{"link": "Route"}}
		for _, endpoint := range []string{"remote", id} {
			state.NodeStatus[endpoint] = model.NodeStatus{NodeID: endpoint, Online: true, Ready: true, XrayReady: true, LastSeen: now}
			state.LinkStatus[endpoint+"/link"] = model.LinkStatus{LinkID: "link", Online: true, Ready: true, LastSeen: now}
		}
		return nil
	})
	check := func(enabled bool) {
		t.Helper()
		data := dashboardRequest(t, s)
		for _, node := range data.Nodes {
			if node.ID == id && node.Enabled != enabled {
				t.Fatalf("embedded node enabled=%v, want %v", node.Enabled, enabled)
			}
		}
		if len(data.Routes) != 1 || data.Routes[0].Enabled != enabled || data.Routes[0].Selectable != enabled || (data.Routes[0].State == "disabled") == enabled {
			t.Fatalf("inconsistent dashboard route: %+v", data.Routes)
		}
		monitor := readMonitor(t, s)
		for _, node := range monitor.Nodes {
			if node.Name == "Exit" && (node.State == "disabled") == enabled {
				t.Fatalf("monitor exit state=%s, enabled=%v", node.State, enabled)
			}
		}
		if len(monitor.Links) != 1 || (monitor.Links[0].State == "disabled") == enabled {
			t.Fatalf("inconsistent monitor route: %+v", monitor.Links)
		}
		state := store.Snapshot()
		if routeAvailable(&state, state.Attachments["route"]) != enabled {
			t.Fatal("management disagrees with route authorization")
		}
		found := false
		for _, candidate := range pendingConnections(state) {
			if candidate.AgentID == id && candidate.GatewayID == "spare" {
				found = true
				if candidate.Available != enabled {
					t.Fatalf("pending connection available=%v, want %v", candidate.Available, enabled)
				}
			}
		}
		if !found {
			t.Fatal("missing pending remote connection")
		}
		if !enabled {
			w := connectionRequest(s, url.Values{"connection": {connectionValue(id, "spare")}})
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Agent "+id+" unavailable") {
				t.Fatalf("disabled exit accepted connection assignment: %d %s", w.Code, w.Body.String())
			}
		}
	}
	check(true)
	for _, toggle := range []func(http.ResponseWriter, *http.Request){s.toggleGateway, s.toggleEmbeddedAgent} {
		for _, enabled := range []bool{false, true} {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.SetPathValue("id", "owner")
			w := httptest.NewRecorder()
			toggle(w, r)
			if w.Code != http.StatusSeeOther {
				t.Fatalf("toggle: %d %s", w.Code, w.Body.String())
			}
			check(enabled)
		}
	}
}
