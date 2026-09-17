package controller

import (
	"net/http"
	"net/url"
	"testing"

	"xmesh/internal/model"
)

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
