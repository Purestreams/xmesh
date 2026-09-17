package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"xmesh/internal/controller"
	"xmesh/internal/model"
)

func TestEnrollReplacePreservesExistingNodeSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	original := []byte(`{"role":"gateway","node_id":"g","credential":"old","gateway":{"reality_listen":"127.0.0.1:9443"}}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request controller.EnrollmentRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Role != model.RoleGateway || request.NodeID != "g" {
			t.Errorf("missing expected node identity in enrollment: %#v %v", request, err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(controller.EnrollmentResponse{Role: model.RoleGateway, NodeID: "g", Credential: "new"})
	}))
	defer server.Close()
	if err := enrollNode([]string{"--controller", server.URL, "--role", "gateway", "--token", "one-time", "--replace", "--output", path}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(b, &config); err != nil {
		t.Fatal(err)
	}
	if config["credential"] != "new" || config["node_id"] != "g" || config["gateway"].(map[string]any)["reality_listen"] != "127.0.0.1:9443" {
		t.Fatalf("unexpected rotated config: %s", b)
	}
}

func TestEnrollReplaceRejectsDifferentNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	original := []byte(`{"role":"gateway","node_id":"g","credential":"old"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(controller.EnrollmentResponse{Role: model.RoleGateway, NodeID: "different", Credential: "new"})
	}))
	defer server.Close()
	if err := enrollNode([]string{"--controller", server.URL, "--role", "gateway", "--token", "one-time", "--replace", "--output", path}); err == nil {
		t.Fatal("different node unexpectedly replaced existing identity")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != string(original) {
		t.Fatalf("existing identity changed: %s %v", b, err)
	}
}
