package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"xmesh/internal/auth"
	"xmesh/internal/model"
)

func TestAgentConfigOrderStableAcrossPolls(t *testing.T) {
	server, _ := testServer(t, func(s *model.State) error {
		s.Agents["a"] = model.Agent{ID: "a", Enabled: true, CredentialHash: auth.SecretHash("credential")}
		for _, id := range []string{"b", "a"} {
			s.Gateways[id] = model.Gateway{ID: id}
			s.Attachments[id] = model.Attachment{ID: id, AgentID: "a", GatewayID: id, Enabled: true}
			s.Links[id] = model.Link{ID: id, AttachmentID: id, Enabled: true}
			s.Users[id] = model.User{ID: id, Enabled: true}
			s.Grants[id] = model.Grant{ID: id, UserID: id, AttachmentID: id, Enabled: true}
		}
		return nil
	})
	var first string
	for i := 0; i < 50; i++ {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("config status %d: %s", response.Code, response.Body.String())
		}
		if i == 0 {
			first = response.Body.String()
		} else if response.Body.String() != first {
			t.Fatal("unchanged Agent configuration changed between polls")
		}
	}
	var config AgentConfig
	if err := json.Unmarshal([]byte(first), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Links) != 2 || config.Links[0].ID != "a" || config.Links[1].ID != "b" || len(config.GrantIDs) != 2 || config.GrantIDs[0] != "a" || config.GrantIDs[1] != "b" {
		t.Fatalf("Agent config not sorted: %+v", config)
	}
}

func TestGatewayConfigOrderStableAcrossPolls(t *testing.T) {
	server, _ := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Enabled: true, CredentialHash: auth.SecretHash("credential")}
		for _, id := range []string{"b", "a"} {
			s.Agents[id] = model.Agent{ID: id, Enabled: true}
			s.Attachments[id] = model.Attachment{ID: id, AgentID: id, GatewayID: "g", Enabled: true}
			s.Links[id] = model.Link{ID: id, AttachmentID: id, Enabled: true}
			s.Users[id] = model.User{ID: id, Enabled: true}
			s.Grants[id] = model.Grant{ID: id, UserID: id, AttachmentID: id, Enabled: true}
		}
		return nil
	})
	var first string
	for i := 0; i < 50; i++ {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("config status %d: %s", response.Code, response.Body.String())
		}
		if i == 0 {
			first = response.Body.String()
		} else if response.Body.String() != first {
			t.Fatal("unchanged Gateway configuration changed between polls")
		}
	}
	var config GatewayConfig
	if err := json.Unmarshal([]byte(first), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Links) != 2 || config.Links[0].ID != "a" || config.Links[1].ID != "b" || len(config.Grants) != 2 || config.Grants[0].ID != "a" || config.Grants[1].ID != "b" {
		t.Fatalf("Gateway config not sorted: %+v", config)
	}
}
