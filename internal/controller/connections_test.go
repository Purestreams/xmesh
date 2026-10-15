package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
)

func connectionValue(agent, gateway string) string {
	value, _ := json.Marshal([2]string{agent, gateway})
	return string(value)
}

func connectionRequest(server *Server, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/admin/assign-connections", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.assignConnections(w, r)
	return w
}

func connectionTestState(state *model.State) error {
	for _, id := range []string{"a1", "a2"} {
		state.Agents[id] = model.Agent{ID: id, Name: id, Enabled: true}
	}
	state.Gateways["cn"] = model.Gateway{ID: "cn", Name: "China", PublicHost: "cn.example", Region: "cn", Enabled: true}
	state.Gateways["overseas"] = model.Gateway{ID: "overseas", Name: "Overseas", PublicHost: "overseas.example", Region: "overseas", Enabled: true}
	state.Attachments["active"] = model.Attachment{ID: "active", AgentID: "a1", GatewayID: "cn", Enabled: true}
	state.Links["active"] = model.Link{ID: "active", Name: "Existing active", AttachmentID: "active", Enabled: true}
	// A disabled extra Link must remain disabled when this pair is already active.
	state.Links["extra"] = model.Link{ID: "extra", AttachmentID: "active", Enabled: false}
	state.Attachments["disabled"] = model.Attachment{ID: "disabled", AgentID: "a1", GatewayID: "overseas", Enabled: false}
	state.Links["disabled"] = model.Link{ID: "disabled", Name: "Existing disabled", AttachmentID: "disabled", Enabled: false, RealityUUID: "preserve-uuid", RealityShortID: "abcd"}
	state.Grants["existing"] = model.Grant{ID: "existing", AttachmentID: "disabled", Enabled: false}
	return nil
}

func TestPendingConnectionsIncludesMissingAndDisabledButNotOfflineActive(t *testing.T) {
	server, store := testServer(t, connectionTestState)
	if err := store.Update(func(state *model.State) error {
		state.Gateways["off"] = model.Gateway{ID: "off", Name: "Disabled Gateway", Enabled: false}
		state.Upstreams["external"] = model.VMessUpstream{ID: "external", Enabled: false}
		state.Attachments["external"] = model.Attachment{ID: "external", GatewayID: "cn", UpstreamID: "external", Enabled: false}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	candidates := pendingConnections(store.Snapshot())
	if len(candidates) != 5 {
		t.Fatalf("want 5 pending pairs, got %#v", candidates)
	}
	available := 0
	for _, candidate := range candidates {
		if candidate.AgentID == "a1" && candidate.GatewayID == "cn" {
			t.Fatal("offline but active pair was included")
		}
		if candidate.Available {
			available++
		}
		if candidate.GatewayID == "off" && candidate.Available {
			t.Fatal("disabled Gateway is selectable")
		}
	}
	if available != 3 {
		t.Fatalf("want 3 available pairs, got %d", available)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.SignSession(server.cfg.adminSessionKey(), "admin", server.now().Add(time.Hour))})
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "全部未开启连接") || !strings.Contains(w.Body.String(), "开启所选连接") {
		t.Fatalf("pending tab was not rendered: %d", w.Code)
	}
}

func TestAssignConnectionsAcrossAgentsPreservesExistingLinksTargetsAndGrants(t *testing.T) {
	server, store := testServer(t, connectionTestState)
	if err := store.Update(func(state *model.State) error {
		gateway := state.Gateways["cn"]
		gateway.RealityTarget = "existing.example:443"
		state.Gateways["cn"] = gateway
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := store.Snapshot()
	values := url.Values{"connection": {
		connectionValue("a1", "cn"), connectionValue("a1", "overseas"),
		connectionValue("a2", "cn"), connectionValue("a2", "overseas"), connectionValue("a2", "cn"),
	}}
	w := connectionRequest(server, values)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("assign: %d %s", w.Code, w.Body.String())
	}
	after := store.Snapshot()
	if len(after.Attachments) != 4 || len(after.Links) != 5 || !after.Attachments["disabled"].Enabled || !after.Links["disabled"].Enabled {
		t.Fatalf("incomplete assignment: routes=%d links=%d", len(after.Attachments), len(after.Links))
	}
	if after.Links["disabled"].RealityUUID != "preserve-uuid" || after.Links["disabled"].RealityShortID != "abcd" || after.Links["extra"].Enabled {
		t.Fatal("existing Link settings or unselected disabled Link changed")
	}
	if after.Gateways["cn"].RealityTarget != "existing.example:443" || after.Gateways["overseas"].RealityTarget != "www.swift.com:443" {
		t.Fatal("existing target or individual Gateway default was not preserved")
	}
	if !reflect.DeepEqual(before.Grants, after.Grants) || len(pendingConnections(after)) != 0 {
		t.Fatal("grants changed or connections are still pending")
	}
	location, _ := url.Parse(w.Header().Get("Location"))
	if !strings.Contains(location.Query().Get("notice"), "新增 2，重新启用 1，已开启 1") {
		t.Fatalf("wrong deduplicated counts: %s", location)
	}
	// Retrying must not create duplicates, enable extra Links or bump node versions.
	w = connectionRequest(server, values)
	retry := store.Snapshot()
	if w.Code != http.StatusSeeOther || !reflect.DeepEqual(after.Links, retry.Links) || !reflect.DeepEqual(after.Gateways, retry.Gateways) || !reflect.DeepEqual(after.Agents, retry.Agents) {
		t.Fatal("retry changed existing connections")
	}
}

func TestAssignConnectionsRollsBackWholeBatch(t *testing.T) {
	for _, invalid := range []string{"missing-node", "disabled-node", "missing-target", "invalid-target", "invalid-pair", "empty"} {
		t.Run(invalid, func(t *testing.T) {
			server, store := testServer(t, connectionTestState)
			if err := store.Update(func(state *model.State) error {
				state.Gateways["manual"] = model.Gateway{ID: "manual", Name: "Manual", PublicHost: "manual.example", Enabled: invalid != "disabled-node"}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			form := url.Values{"connection": {connectionValue("a2", "cn"), connectionValue("a2", "manual")}}
			switch invalid {
			case "missing-node":
				form["connection"][1] = connectionValue("missing", "cn")
			case "invalid-target":
				form.Set("reality_target", "invalid")
			case "invalid-pair":
				form["connection"][1] = `["a2","cn","extra"]`
			case "empty":
				form.Del("connection")
			}
			before := store.Snapshot()
			w := connectionRequest(server, form)
			if w.Code != http.StatusBadRequest || !reflect.DeepEqual(before, store.Snapshot()) {
				t.Fatalf("batch was partially persisted: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestAssignConnectionsRequiresAdminAndCSRF(t *testing.T) {
	server, store := testServer(t, connectionTestState)
	cookie := auth.SignSession(server.cfg.adminSessionKey(), "admin", server.now().Add(time.Hour))
	for _, authenticated := range []bool{false, true} {
		before := store.Snapshot()
		form := url.Values{"connection": {connectionValue("a2", "cn")}}
		r := httptest.NewRequest(http.MethodPost, "/admin/assign-connections", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if !reflect.DeepEqual(before, store.Snapshot()) || (authenticated && w.Code != http.StatusForbidden) || (!authenticated && w.Header().Get("Location") != "/login") {
			t.Fatalf("unprotected bulk assignment: %d", w.Code)
		}
	}
}
