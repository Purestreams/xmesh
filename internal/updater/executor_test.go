package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"xmesh/internal/model"
)

func TestComposeSwitchRetainsHostSettingsButDropsBuild(t *testing.T) {
	old := "services:\n  xmesh:\n    build: ./image\n    image: xmesh-local:v1\n    network_mode: host\n    volumes:\n      - ./config:/etc/xmesh:ro\n"
	next, err := composeImage(old, "xmesh-upgrade:job")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(next, "build:") || !strings.Contains(next, "image: xmesh-upgrade:job") || !strings.Contains(next, "network_mode: host") || !strings.Contains(next, "./config:/etc/xmesh:ro") {
		t.Fatalf("unsafe Compose rewrite: %s", next)
	}
	if _, err := composeImage("services: {}", "old"); err == nil {
		t.Fatal("missing image accepted")
	}
}

func TestChangedVersionReportsTaskFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires durable directory sync on Linux")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "node.json"), []byte(`{"credential":"node"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var stages []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/self/status" {
			_ = json.NewEncoder(w).Encode(model.NodeStatus{NodeID: "n", Role: model.RoleAgent, BinaryVersion: "v2.0.0", LastSeen: time.Now()})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/report") {
			var report struct {
				Stage string `json:"stage"`
			}
			_ = json.NewDecoder(r.Body).Decode(&report)
			stages = append(stages, report.Stage)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	c := client{cfg: Config{ControllerURL: server.URL, NodeID: "n", Role: "agent", Credential: "helper", Mode: "docker", InstallDir: root, WorkDir: filepath.Join(root, "jobs")}, http: server.Client()}
	task := model.UpgradeTask{ID: "task", NodeID: "n", Role: model.RoleAgent, FromVersion: "v1.0.0", TargetVersion: "v2.0.0", Attempt: 1}
	err := execute(context.Background(), c, task)
	if err == nil {
		t.Fatal("changed version was accepted")
	}
	if len(stages) != 1 || stages[0] != "failed" {
		t.Fatalf("preflight failure not reported: %v, error: %v", stages, err)
	}
}
