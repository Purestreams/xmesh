package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/model"
)

func TestAssignMultipleGatewaysToAgent(t *testing.T) {
	server, store := testServer(t, func(state *model.State) error {
		state.Agents["a"] = model.Agent{ID: "a", Name: "Agent", Enabled: true}
		for _, id := range []string{"g1", "g2"} {
			state.Gateways[id] = model.Gateway{ID: id, Name: id, PublicHost: id + ".example", Enabled: true}
		}
		return nil
	})
	values := url.Values{"gateway_id": {"g1", "g2"}, "reality_target": {"target.example:443"}}
	request := httptest.NewRequest(http.MethodPost, "/admin/agents/a/assign-gateways", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetPathValue("id", "a")
	recorder := httptest.NewRecorder()
	server.assignGateways(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	state := store.Snapshot()
	if len(state.Attachments) != 2 || len(state.Links) != 2 || state.Gateways["g1"].RealityPrivateKey == "" || state.Gateways["g2"].RealityPrivateKey == "" {
		t.Fatalf("incomplete batch assignment: %#v", state)
	}
	for _, link := range state.Links {
		if !strings.HasPrefix(link.URL, "reality://") || link.RealityUUID == "" || link.RealityShortID == "" {
			t.Fatalf("invalid REALITY Link: %#v", link)
		}
	}
}

func TestBatchAssignmentIsAtomicOnInvalidTarget(t *testing.T) {
	server, store := testServer(t, func(state *model.State) error {
		state.Agents["a"] = model.Agent{ID: "a", Name: "Agent", Enabled: true}
		state.Gateways["g"] = model.Gateway{ID: "g", Name: "Gateway", PublicHost: "gateway.example", Enabled: true}
		return nil
	})
	values := url.Values{"gateway_id": {"g"}, "reality_target": {"invalid"}}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetPathValue("id", "a")
	recorder := httptest.NewRecorder()
	server.assignGateways(recorder, request)
	if recorder.Code != http.StatusBadRequest || len(store.Snapshot().Attachments) != 0 {
		t.Fatalf("partial assignment after failure: %d %#v", recorder.Code, store.Snapshot().Attachments)
	}
}

func TestOpenSubscriptionForSelectedRoutesOnly(t *testing.T) {
	server, store := testServer(t, func(state *model.State) error {
		state.Users["u"] = model.User{ID: "u", Enabled: true}
		state.Agents["a"] = model.Agent{ID: "a", Enabled: true}
		for _, id := range []string{"g1", "g2"} {
			state.Gateways[id] = model.Gateway{ID: id, Enabled: true}
			state.Attachments[id] = model.Attachment{ID: id, GatewayID: id, AgentID: "a", Enabled: true}
			state.Links[id] = model.Link{ID: id, AttachmentID: id, Enabled: true}
		}
		return nil
	})
	values := url.Values{"attachment_id": {"g1"}}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetPathValue("id", "u")
	recorder := httptest.NewRecorder()
	server.openSubscription(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	state := store.Snapshot()
	if len(state.Grants) != 1 {
		t.Fatalf("grants: %#v", state.Grants)
	}
	for _, grant := range state.Grants {
		if grant.AttachmentID != "g1" || grant.Published {
			t.Fatalf("wrong or prematurely published grant: %#v", grant)
		}
	}
}

func TestCredentialRotationKeepsOldCredentialOnlyUntilNewStatus(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Enabled: true, CredentialHash: auth.SecretHash("old-secret")}
		s.Enrollments["e"] = model.Enrollment{ID: "e", NodeID: "g", Role: model.RoleGateway, TokenHash: auth.SecretHash("rotate-token"), ExpiresAt: time.Unix(1_800_000_000, 0).Add(30 * time.Minute)}
		return nil
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/enroll", strings.NewReader(`{"token":"rotate-token"}`))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("enroll status %d: %s", recorder.Code, recorder.Body.String())
	}
	var response EnrollmentResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	configRequest := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	configRequest.Header.Set("Authorization", "Bearer old-secret")
	configRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(configRecorder, configRequest)
	if configRecorder.Code != http.StatusOK {
		t.Fatalf("old credential lost before cutover: %d", configRecorder.Code)
	}
	statusRequest := httptest.NewRequest(http.MethodPost, "/api/v1/status", strings.NewReader(`{"status":{"node_id":"g","role":"gateway","binary_version":"v-next"}}`))
	statusRequest.Header.Set("Authorization", "Bearer "+response.Credential)
	statusRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(statusRecorder, statusRequest)
	if statusRecorder.Code != http.StatusNoContent || state.Snapshot().Gateways["g"].PreviousCredentialHash != "" {
		t.Fatalf("new credential did not revoke old: %d", statusRecorder.Code)
	}
	selfRequest := httptest.NewRequest(http.MethodGet, "/api/v1/self/status", nil)
	selfRequest.Header.Set("Authorization", "Bearer "+response.Credential)
	selfRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(selfRecorder, selfRequest)
	if selfRecorder.Code != http.StatusOK || !strings.Contains(selfRecorder.Body.String(), `"binary_version":"v-next"`) {
		t.Fatalf("new version not reported: %d %s", selfRecorder.Code, selfRecorder.Body.String())
	}
	configRecorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(configRecorder, configRequest)
	if configRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("old credential still accepted: %d", configRecorder.Code)
	}
}

func TestEnrollmentRejectsWrongExpectedNodeBeforeCredentialRotation(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["g1"] = model.Gateway{ID: "g1", Enabled: true, CredentialHash: auth.SecretHash("old-secret")}
		s.Enrollments["e"] = model.Enrollment{ID: "e", NodeID: "g1", Role: model.RoleGateway, TokenHash: auth.SecretHash("rotate-token"), ExpiresAt: time.Unix(1_800_000_000, 0).Add(30 * time.Minute)}
		return nil
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/enroll", strings.NewReader(`{"token":"rotate-token","role":"gateway","node_id":"g2"}`))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("wrong node was accepted: %d", recorder.Code)
	}
	snapshot := state.Snapshot()
	if snapshot.Gateways["g1"].CredentialHash != auth.SecretHash("old-secret") || !snapshot.Enrollments["e"].UsedAt.IsZero() {
		t.Fatal("wrong-node token changed credential or consumed enrollment")
	}
}
