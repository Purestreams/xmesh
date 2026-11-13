package controller

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Purestreams/xmesh/internal/model"
)

func TestSetGrantStateScopesAccessAndIsRetrySafe(t *testing.T) {
	server, state := testServer(t, securityState)
	before := state.Snapshot()
	response := postNodeForm("/admin/grants/grant/state", url.Values{"enabled": {"false"}}, server.setGrantState)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("disable: %d %s", response.Code, response.Body.String())
	}
	after := state.Snapshot()
	want := before.Grants["grant"]
	want.Enabled, want.Published = false, false
	want.AccessVersion = after.Gateways["g"].DesiredVersion
	if after.Grants["grant"] != want || after.Grants["other"] != before.Grants["other"] || after.Attachments["route"] != before.Attachments["route"] || after.Links["link"] != before.Links["link"] {
		t.Fatal("disabling access changed credentials, another user's grant or the shared route")
	}
	if after.Gateways["g"].DesiredVersion != before.Gateways["g"].DesiredVersion+1 {
		t.Fatal("Gateway was not notified")
	}
	payload, err := BuildSubscription(after, "u")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(plain) != 0 {
		t.Fatal("disabled access still appears in subscription", payload, err)
	}
	response = postNodeForm("/admin/grants/grant/state", url.Values{"enabled": {"false"}}, server.setGrantState)
	if response.Code != http.StatusSeeOther || state.Snapshot().Gateways["g"].DesiredVersion != after.Gateways["g"].DesiredVersion || state.Snapshot().Grants["grant"].Enabled {
		t.Fatal("retry inverted access or triggered another config update")
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if notice := location.Query().Get("notice"); !strings.Contains(notice, "already disabled") || strings.Contains(notice, "waiting for Gateway configuration") {
		t.Fatalf("no-op retry reported a new configuration change: %q", notice)
	}
	response = postNodeForm("/admin/grants/grant/state", url.Values{"enabled": {"true"}}, server.setGrantState)
	if response.Code != http.StatusSeeOther || !state.Snapshot().Grants["grant"].Enabled || state.Snapshot().Grants["grant"].Published {
		t.Fatal("restored access must await Gateway acknowledgement")
	}
}

func TestSetGrantStateBatchValidatesBeforeChangingAccess(t *testing.T) {
	server, state := testServer(t, securityState)
	post := func(values url.Values) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/admin/grants/state", strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		server.setGrantState(response, request)
		return response
	}
	before := state.Snapshot()
	for _, values := range []url.Values{
		{"enabled": {"false"}},
		{"enabled": {"invalid"}, "grant_id": {"grant"}},
		{"enabled": {"false"}, "grant_id": {"grant", "missing"}},
	} {
		response := post(values)
		if response.Code != http.StatusBadRequest || !reflect.DeepEqual(state.Snapshot(), before) {
			t.Fatal("invalid selection partially changed access", response.Code, response.Body.String())
		}
	}
	response := post(url.Values{"enabled": {"false"}, "grant_id": {"grant", "other", "grant"}})
	after := state.Snapshot()
	if response.Code != http.StatusSeeOther || after.Grants["grant"].Enabled || after.Grants["other"].Enabled || after.Gateways["g"].DesiredVersion != before.Gateways["g"].DesiredVersion+1 {
		t.Fatal("batch did not apply once per affected Gateway", response.Code, response.Body.String())
	}
}

func TestEditAndDeleteLink(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", RealityTarget: "old.example:443", RealityName: "old.example", DesiredVersion: 1}
		s.Agents["a"] = model.Agent{ID: "a", DesiredVersion: 1}
		s.Attachments["n"] = model.Attachment{ID: "n", GatewayID: "g", AgentID: "a", Enabled: true}
		s.Links["l"] = model.Link{ID: "l", Name: "Link", AttachmentID: "n", URL: "reality://edge.example:8443/tunnel", Enabled: true}
		s.Grants["r"] = model.Grant{ID: "r", AttachmentID: "n", Enabled: true, Published: true}
		return nil
	})
	response := postNodeForm("/admin/links/l/edit", url.Values{"name": {"New Link"}, "url": {"reality://new-edge.example:8443/tunnel"}, "reality_target": {"new.example:443"}}, server.editLink)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("edit link: %d %s", response.Code, response.Body.String())
	}
	snapshot := state.Snapshot()
	if snapshot.Links["l"].URL != "reality://new-edge.example:8443/tunnel" || snapshot.Gateways["g"].RealityTarget != "new.example:443" || snapshot.Agents["a"].DesiredVersion <= 1 {
		t.Fatal("Link or REALITY target was not updated")
	}
	response = postNodeForm("/admin/links/l/delete", url.Values{"confirm_name": {"wrong"}}, server.deleteLink)
	if response.Code != http.StatusBadRequest || len(state.Snapshot().Links) != 1 {
		t.Fatal("wrong confirmation deleted Link")
	}
	response = postNodeForm("/admin/links/l/delete", url.Values{"confirm_name": {"New Link"}}, server.deleteLink)
	if response.Code != http.StatusSeeOther || len(state.Snapshot().Links) != 0 || state.Snapshot().Grants["r"].Published {
		t.Fatal("last Link deletion did not unpublish grant")
	}
}

func TestDeleteAssignmentAndGrant(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", DesiredVersion: 1}
		s.Agents["a"] = model.Agent{ID: "a", DesiredVersion: 1}
		s.Attachments["n"] = model.Attachment{ID: "n", GatewayID: "g", AgentID: "a"}
		s.Links["l"] = model.Link{ID: "l", AttachmentID: "n"}
		s.Grants["r"] = model.Grant{ID: "r", AttachmentID: "n"}
		return nil
	})
	response := postNodeForm("/admin/grants/r/delete", url.Values{"confirm_id": {"r"}}, server.deleteGrant)
	if response.Code != http.StatusSeeOther || len(state.Snapshot().Grants) != 0 {
		t.Fatal("grant not deleted")
	}
	response = postNodeForm("/admin/attachments/n/delete", url.Values{"confirm_id": {"n"}}, server.deleteAttachment)
	snapshot := state.Snapshot()
	if response.Code != http.StatusSeeOther || len(snapshot.Attachments) != 0 || len(snapshot.Links) != 0 || len(snapshot.Gateways) != 1 || len(snapshot.Agents) != 1 {
		t.Fatalf("assignment deletion left references: %+v", snapshot)
	}
}
