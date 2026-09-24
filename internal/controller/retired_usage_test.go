package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/model"
)

func TestDeletedExternalRouteAcceptsFinalUsageWithoutBlockingOtherGrants(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["gateway"] = model.Gateway{ID: "gateway", Enabled: true, DesiredVersion: 1, CredentialHash: auth.SecretHash("gateway-secret")}
		s.Users["old-user"] = model.User{ID: "old-user", Enabled: true}
		s.Users["live-user"] = model.User{ID: "live-user", Enabled: true}
		for _, route := range []struct{ id, user string }{{"old", "old-user"}, {"live", "live-user"}} {
			s.Upstreams[route.id] = model.VMessUpstream{ID: route.id, Name: route.id + " exit", Enabled: true}
			s.Attachments[route.id] = model.Attachment{ID: route.id, GatewayID: "gateway", UpstreamID: route.id, Enabled: true}
			s.Grants[route.id] = model.Grant{ID: route.id, UserID: route.user, AttachmentID: route.id, Enabled: true}
		}
		return nil
	})
	now := time.Now().UTC()
	server.now = func() time.Time { return now }
	if err := state.Update(func(s *model.State) error {
		if err := sampleGrantUsage(s, "gateway-process", "old", []model.GrantLinkUsage{{LinkID: "old", UploadBytes: 5}}, now); err != nil {
			return err
		}
		removeAttachment(s, "old")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Snapshot().RetiredGrants["old"]; !ok {
		t.Fatal("deleted authorization has no temporary accounting identity")
	}
	post := func(applied uint64, includeOld bool) int {
		t.Helper()
		report := StatusReport{Status: model.NodeStatus{InstanceID: "gateway-process", Ready: true, XrayReady: true, ExternalUpstreams: true, AppliedVersion: applied}, Links: []model.LinkStatus{{LinkID: "live"}}, Grants: []model.GrantStatus{{GrantID: "live", Links: []model.GrantLinkUsage{{LinkID: "live", UploadBytes: 30}}}}}
		if includeOld {
			report.Links = append(report.Links, model.LinkStatus{LinkID: "old"})
			report.Grants = append(report.Grants, model.GrantStatus{GrantID: "old", Links: []model.GrantLinkUsage{{LinkID: "old", UploadBytes: 12}}})
		}
		payload, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/status", bytes.NewReader(payload))
		request.Header.Set("Authorization", "Bearer gateway-secret")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response.Code
	}
	if code := post(1, true); code != http.StatusNoContent {
		t.Fatalf("final old-process report rejected: %d", code)
	}
	snapshot := state.Snapshot()
	if snapshot.UsageHistory["old-user/old"][0].UploadBytes != 12 || snapshot.UsageHistory["live-user/live"][0].UploadBytes != 30 || snapshot.UsageLabels["old-user/old"] != "old exit" {
		t.Fatalf("usage was not recorded for both routes: %+v", snapshot.UsageHistory)
	}
	if code := post(1, true); code != http.StatusNoContent {
		t.Fatalf("repeated final report rejected: %d", code)
	}
	if got := state.Snapshot().UsageHistory["old-user/old"][0].UploadBytes; got != 12 {
		t.Fatalf("repeated report counted twice: %d", got)
	}
	if code := post(2, false); code != http.StatusNoContent {
		t.Fatalf("new revision report rejected: %d", code)
	}
	snapshot = state.Snapshot()
	if len(snapshot.RetiredGrants) != 0 || len(snapshot.RetiredLinks) != 0 || snapshot.UsageHistory["old-user/old"][0].UploadBytes != 12 {
		t.Fatal("retired accounting identity was not cleaned after the new revision")
	}
}

func TestDeletedAgentLinkAcceptsFinalGatewayUsage(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["gateway"] = model.Gateway{ID: "gateway", Enabled: true, DesiredVersion: 1, CredentialHash: auth.SecretHash("gateway-secret")}
		s.Agents["agent"] = model.Agent{ID: "agent", Enabled: true, DesiredVersion: 1, CredentialHash: auth.SecretHash("agent-secret")}
		s.Users["user"] = model.User{ID: "user", Enabled: true}
		s.Attachments["route"] = model.Attachment{ID: "route", GatewayID: "gateway", AgentID: "agent", Enabled: true}
		s.Links["link"] = model.Link{ID: "link", Name: "old Link", AttachmentID: "route", Enabled: true}
		s.Grants["grant"] = model.Grant{ID: "grant", UserID: "user", AttachmentID: "route", Enabled: true}
		return nil
	})
	now := time.Now().UTC()
	server.now = func() time.Time { return now }
	if err := state.Update(func(s *model.State) error {
		removeLink(s, "link")
		bumpGateway(s, "gateway")
		bumpAgent(s, "agent")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	post := func(credential string, applied uint64, final bool) int {
		t.Helper()
		report := StatusReport{Status: model.NodeStatus{InstanceID: credential, AppliedVersion: applied, XrayReady: true, Ready: true}}
		if final {
			report.Links = []model.LinkStatus{{LinkID: "link"}}
			if credential == "gateway-secret" {
				report.Grants = []model.GrantStatus{{GrantID: "grant", Links: []model.GrantLinkUsage{{LinkID: "link", UploadBytes: 9}}}}
			}
		}
		payload, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/status", bytes.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+credential)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response.Code
	}
	if code := post("gateway-secret", 1, true); code != http.StatusNoContent {
		t.Fatalf("Gateway final Link report rejected: %d", code)
	}
	if code := post("agent-secret", 1, true); code != http.StatusNoContent {
		t.Fatalf("Agent final Link report rejected: %d", code)
	}
	if got := state.Snapshot().UsageHistory["user/link"][0].UploadBytes; got != 9 {
		t.Fatalf("retired Link traffic lost: %d", got)
	}
	if code := post("gateway-secret", 2, false); code != http.StatusNoContent {
		t.Fatalf("Gateway new revision rejected: %d", code)
	}
	if code := post("agent-secret", 2, false); code != http.StatusNoContent || len(state.Snapshot().RetiredLinks) != 0 {
		t.Fatalf("retired Link was not cleared after both nodes applied: %d", code)
	}
}
