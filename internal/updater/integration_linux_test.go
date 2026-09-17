//go:build linux

package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xmesh/internal/model"
)

func TestDockerUpgradeAndRollback(t *testing.T) {
	for _, tc := range []struct {
		name, role, target string
		fail, local        bool
	}{
		{"gateway_success", "gateway", "v2", false, false},
		{"agent_rollback", "agent", "vbad", true, false},
		{"gateway_local_success", "gateway", "v2.0.0", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			installDir := filepath.Join(root, "install")
			for _, name := range []string{"config", "image", "data", "updater"} {
				if err := os.MkdirAll(filepath.Join(installDir, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			oldCompose := "services:\n  xmesh:\n    build: ./image\n    image: xmesh-local:v1\n    network_mode: host\n    volumes:\n      - ./config:/etc/xmesh:ro\n      - ./data:/var/lib/xmesh\n"
			files := map[string]string{"compose.yaml": oldCompose, "image/xmesh": "old-xmesh", "image/xray": "old-xray", "image/Dockerfile": "FROM scratch\nCOPY xmesh /usr/local/bin/xmesh\nCOPY xray /usr/local/bin/xray\n", "config/node.json": `{"credential":"node"}`}
			for name, value := range files {
				if err := os.WriteFile(filepath.Join(installDir, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			stateFile := filepath.Join(root, "container-state")
			if err := os.WriteFile(stateFile, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "bin")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			fakeDocker := `#!/bin/sh
set -eu
case "$1:$2" in
  compose:ps) echo container-id ;;
  inspect:-f)
    case "$3" in '{{.Image}}') echo sha256:old ;; *) case "$(cat "$FAKE_STATE")" in stopped) echo false;; *) echo true;; esac ;; esac ;;
  tag:*) exit 0 ;;
  build:*) exit 0 ;;
  compose:up)
    if grep -q 'xmesh-upgrade:' compose.yaml; then
      if [ "$FAKE_FAIL" = true ]; then printf 'stopped' >"$FAKE_STATE"; exit 1; fi
      printf 'new' >"$FAKE_STATE"
    elif grep -q 'xmesh-rollback:' compose.yaml; then
      printf 'old' >"$FAKE_STATE"
    fi ;;
  *) echo "unexpected docker $*" >&2; exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeDocker), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_STATE", stateFile)
			if tc.fail {
				t.Setenv("FAKE_FAIL", "true")
			} else {
				t.Setenv("FAKE_FAIL", "false")
			}
			archiveDir := filepath.Join(root, "archive")
			if err := os.MkdirAll(archiveDir, 0700); err != nil {
				t.Fatal(err)
			}
			binary := "#!/bin/sh\nif [ \"$1\" = version ]; then echo " + tc.target + "; fi\n"
			for name, value := range map[string]string{"xmesh": binary, "xray": "#!/bin/sh\nexit 0\n"} {
				if err := os.WriteFile(filepath.Join(archiveDir, name), []byte(value), 0755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("tar", "-C", archiveDir, "-czf", filepath.Join(root, "release.tar.gz"), "xmesh", "xray")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("make archive: %v %s", err, out)
			}
			archive, err := os.ReadFile(filepath.Join(root, "release.tar.gz"))
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(archive)
			var stages []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/v1/updater/manual":
					var request struct {
						ID string `json:"id"`
					}
					_ = json.NewDecoder(r.Body).Decode(&request)
					json.NewEncoder(w).Encode(model.UpgradeTask{ID: request.ID, Manual: true, NodeID: "n", Role: model.Role(tc.role), FromVersion: "v1", TargetVersion: tc.target, Asset: "xmesh-" + tc.target + "-linux-amd64.tar.gz", SHA256: hex.EncodeToString(digest[:]), Mode: "docker", Arch: "amd64", Attempt: 1})
				case strings.HasPrefix(r.URL.Path, "/releases/"):
					w.Write(archive)
				case r.URL.Path == "/api/v1/self/status":
					if r.Header.Get("Authorization") != "Bearer node" {
						http.Error(w, "unauthorized", 401)
						return
					}
					state, _ := os.ReadFile(stateFile)
					v, instance := "v1", "old-instance"
					if string(state) == "new" {
						v, instance = tc.target, "new-instance"
					}
					json.NewEncoder(w).Encode(model.NodeStatus{NodeID: "n", Role: model.Role(tc.role), BinaryVersion: v, InstanceID: instance, Ready: true, XrayReady: true, LastSeen: time.Now().UTC()})
				case strings.Contains(r.URL.Path, "/report"):
					var report struct {
						Stage string `json:"stage"`
					}
					_ = json.NewDecoder(r.Body).Decode(&report)
					stages = append(stages, report.Stage)
					w.WriteHeader(204)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cfg := Config{ControllerURL: server.URL, NodeID: "n", Role: tc.role, Credential: "updater", Mode: "docker", InstallDir: installDir, WorkDir: filepath.Join(root, "jobs")}
			c := client{cfg: cfg, http: server.Client()}
			c.http.Timeout = 90 * time.Second
			task := model.UpgradeTask{ID: tc.name, NodeID: "n", Role: model.Role(tc.role), FromVersion: "v1", TargetVersion: tc.target, Asset: "xmesh-" + tc.target + "-linux-amd64.tar.gz", SHA256: hex.EncodeToString(digest[:]), Mode: "docker", Arch: "amd64", Stage: "running", Attempt: 1, ExpiresAt: time.Now().Add(time.Hour)}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
			defer cancel()
			if tc.local {
				err = localUpgrade(ctx, cfg, tc.target, filepath.Join(root, "release.tar.gz"), task.SHA256, server.Client())
			} else {
				err = execute(ctx, c, task)
			}
			state, _ := os.ReadFile(stateFile)
			if tc.fail {
				if err == nil || string(state) != "old" || !strings.Contains(strings.Join(stages, ","), "rolled_back") {
					t.Fatalf("rollback: err=%v state=%s stages=%v", err, state, stages)
				}
			} else {
				if err != nil || string(state) != "new" || (!tc.local && !strings.Contains(strings.Join(stages, ","), "succeeded")) {
					t.Fatalf("upgrade: err=%v state=%s stages=%v", err, state, stages)
				}
				got, _ := os.ReadFile(filepath.Join(installDir, "image", "xmesh"))
				if !bytes.Equal(got, []byte(binary)) {
					t.Fatal("successful upgrade did not update image source")
				}
			}
		})
	}
}

func TestLocalUpgradeRejectsUnusableNodeStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status model.NodeStatus
	}{
		{"stale", model.NodeStatus{NodeID: "n", Role: model.RoleAgent, BinaryVersion: "v1.0.0", LastSeen: time.Now().Add(-time.Minute)}},
		{"empty_version", model.NodeStatus{NodeID: "n", Role: model.RoleAgent, LastSeen: time.Now()}},
		{"wrong_role", model.NodeStatus{NodeID: "n", Role: model.RoleGateway, BinaryVersion: "v1.0.0", LastSeen: time.Now()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "config", "node.json"), []byte(`{"credential":"node"}`), 0600); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(tc.status) }))
			defer server.Close()
			cfg := Config{ControllerURL: server.URL, NodeID: "n", Role: "agent", Credential: "helper", Mode: "docker", InstallDir: root, WorkDir: filepath.Join(root, "jobs")}
			err := localUpgrade(context.Background(), cfg, "v2.0.0", filepath.Join(root, "release.tar.gz"), strings.Repeat("a", 64), server.Client())
			if err == nil || !strings.Contains(err.Error(), "fresh version status") {
				t.Fatalf("unsafe local upgrade was accepted: %v", err)
			}
			entries, err := os.ReadDir(cfg.WorkDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.IsDir() {
					t.Fatalf("upgrade job created for invalid status: %s", entry.Name())
				}
			}
		})
	}
}

func TestLostManualReservationResponseRecoversAsFailed(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "node.json"), []byte(`{"credential":"node"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var reservedID, reportedStage string
	manualCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/self/status":
			_ = json.NewEncoder(w).Encode(model.NodeStatus{NodeID: "n", Role: model.RoleAgent, BinaryVersion: "v1.0.0", LastSeen: time.Now()})
		case r.URL.Path == "/api/v1/updater/manual":
			var req struct{ ID, FromVersion, TargetVersion, SHA256, Mode, Arch string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			if reservedID != "" && reservedID != req.ID {
				t.Errorf("retry changed reservation ID")
			}
			reservedID = req.ID
			manualCalls++
			if manualCalls == 1 {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
				return
			}
			_ = json.NewEncoder(w).Encode(model.UpgradeTask{ID: req.ID, Manual: true, NodeID: "n", Role: model.RoleAgent, FromVersion: "v1.0.0", TargetVersion: "v2.0.0", SHA256: req.SHA256, Mode: "docker", Arch: "amd64", Attempt: 1, Stage: "running"})
		case strings.HasSuffix(r.URL.Path, "/report"):
			var report struct {
				Stage string `json:"stage"`
			}
			_ = json.NewDecoder(r.Body).Decode(&report)
			reportedStage = report.Stage
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := Config{ControllerURL: server.URL, NodeID: "n", Role: "agent", Credential: "helper", Mode: "docker", InstallDir: root, WorkDir: filepath.Join(root, "jobs")}
	if err := localUpgrade(context.Background(), cfg, "v2.0.0", filepath.Join(root, "release.tar.gz"), strings.Repeat("a", 64), server.Client()); err == nil {
		t.Fatal("lost reservation response was treated as success")
	}
	if reservedID == "" || manualCalls != 1 {
		t.Fatalf("reservation missing: id=%s calls=%d", reservedID, manualCalls)
	}
	if err := recoverJobs(context.Background(), client{cfg: cfg, http: server.Client()}); err != nil {
		t.Fatal(err)
	}
	if manualCalls != 2 || reportedStage != "failed" {
		t.Fatalf("orphaned manual task: calls=%d stage=%s", manualCalls, reportedStage)
	}
}

func TestSystemdUpgradeAndRollback(t *testing.T) {
	for _, tc := range []struct {
		name, role, target string
		fail, local        bool
	}{
		{"agent_success", "agent", "v2", false, false},
		{"gateway_rollback", "gateway", "vbad", true, false},
		{"agent_local_success", "agent", "v2.0.0", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			oldXmesh, oldXray, oldConfig, oldService := systemdXmeshPath, systemdXrayPath, systemdNodeConfigPath, systemdService
			systemdXmeshPath = filepath.Join(root, "bin", "xmesh")
			systemdXrayPath = filepath.Join(root, "lib", "xray")
			systemdNodeConfigPath = filepath.Join(root, "etc", "node.json")
			systemdService = "test-xmesh.service"
			t.Cleanup(func() {
				systemdXmeshPath, systemdXrayPath, systemdNodeConfigPath, systemdService = oldXmesh, oldXray, oldConfig, oldService
			})
			for _, path := range []string{systemdXmeshPath, systemdXrayPath, systemdNodeConfigPath} {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
			}
			oldBinary := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo v1; fi\n")
			if err := os.WriteFile(systemdXmeshPath, oldBinary, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(systemdXrayPath, []byte("old-xray"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(systemdNodeConfigPath, []byte(`{"credential":"node"}`), 0600); err != nil {
				t.Fatal(err)
			}
			stateFile := filepath.Join(root, "service-state")
			if err := os.WriteFile(stateFile, []byte("on"), 0600); err != nil {
				t.Fatal(err)
			}
			fake := `#!/bin/sh
set -eu
case "$1" in
  is-active) [ "$(cat "$FAKE_STATE")" = on ] ;;
  stop) printf off >"$FAKE_STATE" ;;
  start) if [ "$("$FAKE_XMESH_PATH" version)" = vbad ]; then exit 1; fi; printf on >"$FAKE_STATE" ;;
  *) exit 1 ;;
esac
`
			fakeDir := filepath.Join(root, "fake")
			if err := os.MkdirAll(fakeDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fakeDir, "systemctl"), []byte(fake), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_STATE", stateFile)
			t.Setenv("FAKE_XMESH_PATH", systemdXmeshPath)
			archiveDir := filepath.Join(root, "archive")
			if err := os.MkdirAll(archiveDir, 0700); err != nil {
				t.Fatal(err)
			}
			newBinary := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo " + tc.target + "; fi\n")
			if err := os.WriteFile(filepath.Join(archiveDir, "xmesh"), newBinary, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(archiveDir, "xray"), []byte("new-xray"), 0755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("tar", "-C", archiveDir, "-czf", filepath.Join(root, "release.tar.gz"), "xmesh", "xray")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("make archive: %v %s", err, out)
			}
			archive, err := os.ReadFile(filepath.Join(root, "release.tar.gz"))
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(archive)
			var stages []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/v1/updater/manual":
					var request struct {
						ID string `json:"id"`
					}
					_ = json.NewDecoder(r.Body).Decode(&request)
					json.NewEncoder(w).Encode(model.UpgradeTask{ID: request.ID, Manual: true, NodeID: "n", Role: model.Role(tc.role), FromVersion: "v1", TargetVersion: tc.target, Asset: "xmesh-" + tc.target + "-linux-amd64.tar.gz", SHA256: hex.EncodeToString(digest[:]), Mode: "systemd", Arch: "amd64", Attempt: 1})
				case strings.HasPrefix(r.URL.Path, "/releases/"):
					w.Write(archive)
				case r.URL.Path == "/api/v1/self/status":
					out, err := exec.Command(systemdXmeshPath, "version").Output()
					if err != nil {
						http.Error(w, "process stopped", 503)
						return
					}
					v := strings.TrimSpace(string(out))
					instance := "old-instance"
					if v != "v1" {
						instance = "new-instance"
					}
					json.NewEncoder(w).Encode(model.NodeStatus{NodeID: "n", Role: model.Role(tc.role), BinaryVersion: v, InstanceID: instance, Ready: true, XrayReady: true, LastSeen: time.Now().UTC()})
				case strings.Contains(r.URL.Path, "/report"):
					var report struct {
						Stage string `json:"stage"`
					}
					_ = json.NewDecoder(r.Body).Decode(&report)
					stages = append(stages, report.Stage)
					w.WriteHeader(204)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cfg := Config{ControllerURL: server.URL, NodeID: "n", Role: tc.role, Credential: "updater", Mode: "systemd", WorkDir: filepath.Join(root, "jobs")}
			c := client{cfg: cfg, http: server.Client()}
			c.http.Timeout = 90 * time.Second
			task := model.UpgradeTask{ID: tc.name, NodeID: "n", Role: model.Role(tc.role), FromVersion: "v1", TargetVersion: tc.target, Asset: "xmesh-" + tc.target + "-linux-amd64.tar.gz", SHA256: hex.EncodeToString(digest[:]), Mode: "systemd", Arch: "amd64", Stage: "running", Attempt: 1, ExpiresAt: time.Now().Add(time.Hour)}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
			defer cancel()
			if tc.local {
				err = localUpgrade(ctx, cfg, tc.target, filepath.Join(root, "release.tar.gz"), task.SHA256, server.Client())
			} else {
				err = execute(ctx, c, task)
			}
			got, _ := os.ReadFile(systemdXmeshPath)
			if tc.fail {
				if err == nil || !bytes.Equal(got, oldBinary) || !strings.Contains(strings.Join(stages, ","), "rolled_back") {
					t.Fatalf("rollback: err=%v binary=%s stages=%v", err, got, stages)
				}
			} else {
				if err != nil || !bytes.Equal(got, newBinary) || (!tc.local && !strings.Contains(strings.Join(stages, ","), "succeeded")) {
					t.Fatalf("upgrade: err=%v binary=%s stages=%v", err, got, stages)
				}
			}
		})
	}
}

// Run with XMESH_REAL_DOCKER_TEST=1 inside a disposable Docker CLI container
// with the local daemon socket mounted. Business containers never receive it.
func TestRealDockerComposeUpgradeAndRollback(t *testing.T) {
	if os.Getenv("XMESH_REAL_DOCKER_TEST") != "1" {
		t.Skip("requires the Docker CLI and a disposable daemon test environment")
	}
	for _, tc := range []struct {
		name, role, target string
		fail               bool
	}{
		{"gateway_success", "gateway", "v2", false},
		{"agent_rollback", "agent", "vbad", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			id := fmt.Sprintf("%x", time.Now().UnixNano())
			installDir := filepath.Join(root, "install-"+id)
			for _, name := range []string{"config", "image", "data", "updater"} {
				if err := os.MkdirAll(filepath.Join(installDir, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			oldTag := "xmesh-real-old:" + id
			oldBinary := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo v1; exit 0; fi\nsleep 300\n")
			if err := os.WriteFile(filepath.Join(installDir, "image", "xmesh"), oldBinary, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(installDir, "image", "xray"), []byte("old-xray"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(installDir, "image", "Dockerfile"), []byte("FROM debian:bookworm-slim\nCOPY xmesh /usr/local/bin/xmesh\nCOPY xray /usr/local/lib/xmesh/xray\nENTRYPOINT [\"/usr/local/bin/xmesh\"]\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(installDir, "config", "node.json"), []byte(`{"credential":"node"}`), 0600); err != nil {
				t.Fatal(err)
			}
			compose := "services:\n  xmesh:\n    build: ./image\n    image: " + oldTag + "\n    restart: no\n    command: [\"run\"]\n"
			if err := os.WriteFile(filepath.Join(installDir, "compose.yaml"), []byte(compose), 0644); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if err := command(ctx, filepath.Join(installDir, "image"), "docker", "build", "--tag", oldTag, "."); err != nil {
				t.Fatal(err)
			}
			if err := command(ctx, installDir, "docker", "compose", "up", "-d", "--no-build"); err != nil {
				t.Fatal(err)
			}
			taskID := "real" + id
			t.Cleanup(func() {
				cleanupCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
				defer done()
				_ = command(cleanupCtx, installDir, "docker", "compose", "down", "--remove-orphans")
				for _, tag := range []string{oldTag, "xmesh-upgrade:" + taskID, "xmesh-rollback:" + taskID} {
					_ = command(cleanupCtx, "", "docker", "image", "rm", tag)
				}
			})
			oldContainer, err := composeContainer(ctx, installDir)
			if err != nil {
				t.Fatal(err)
			}
			archiveDir := filepath.Join(root, "archive")
			if err := os.MkdirAll(archiveDir, 0700); err != nil {
				t.Fatal(err)
			}
			newBinary := "#!/bin/sh\nif [ \"$1\" = version ]; then echo " + tc.target + "; exit 0; fi\n"
			if tc.fail {
				newBinary += "exit 1\n"
			} else {
				newBinary += "sleep 300\n"
			}
			if err := os.WriteFile(filepath.Join(archiveDir, "xmesh"), []byte(newBinary), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(archiveDir, "xray"), []byte("new-xray"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := command(ctx, archiveDir, "tar", "-czf", filepath.Join(root, "release.tar.gz"), "xmesh", "xray"); err != nil {
				t.Fatal(err)
			}
			archive, err := os.ReadFile(filepath.Join(root, "release.tar.gz"))
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(archive)
			var stages []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/releases/"):
					w.Write(archive)
				case r.URL.Path == "/api/v1/self/status":
					if r.Header.Get("Authorization") != "Bearer node" {
						http.Error(w, "unauthorized", 401)
						return
					}
					container, err := composeContainer(r.Context(), installDir)
					if err != nil {
						http.Error(w, "container unavailable", 503)
						return
					}
					out, err := exec.CommandContext(r.Context(), "docker", "exec", container, "/usr/local/bin/xmesh", "version").Output()
					if err != nil {
						http.Error(w, "process unavailable", 503)
						return
					}
					json.NewEncoder(w).Encode(model.NodeStatus{NodeID: "n", Role: model.Role(tc.role), BinaryVersion: strings.TrimSpace(string(out)), InstanceID: container, Ready: true, XrayReady: true, LastSeen: time.Now().UTC()})
				case strings.Contains(r.URL.Path, "/report"):
					var report struct {
						Stage string `json:"stage"`
					}
					_ = json.NewDecoder(r.Body).Decode(&report)
					stages = append(stages, report.Stage)
					w.WriteHeader(204)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cfg := Config{ControllerURL: server.URL, NodeID: "n", Role: tc.role, Credential: "updater", Mode: "docker", InstallDir: installDir, WorkDir: filepath.Join(root, "jobs")}
			c := client{cfg: cfg, http: server.Client()}
			c.http.Timeout = 2 * time.Minute
			task := model.UpgradeTask{ID: taskID, NodeID: "n", Role: model.Role(tc.role), FromVersion: "v1", TargetVersion: tc.target, Asset: "xmesh-" + tc.target + "-linux-amd64.tar.gz", SHA256: hex.EncodeToString(digest[:]), Mode: "docker", Arch: "amd64", Stage: "running", Attempt: 1, ExpiresAt: time.Now().Add(time.Hour)}
			err = execute(ctx, c, task)
			current, containerErr := composeContainer(ctx, installDir)
			if tc.fail {
				if err == nil || containerErr != nil || current == "" || !strings.Contains(strings.Join(stages, ","), "rolled_back") {
					t.Fatalf("real rollback: err=%v container=%s containerErr=%v stages=%v", err, current, containerErr, stages)
				}
				if version, err := localVersion(ctx, cfg); err != nil || version != "v1" {
					t.Fatalf("old image did not recover: %q %v", version, err)
				}
			} else {
				if err != nil || containerErr != nil || current == oldContainer || !strings.Contains(strings.Join(stages, ","), "succeeded") {
					t.Fatalf("real upgrade: err=%v container=%s old=%s stages=%v", err, current, oldContainer, stages)
				}
				if version, err := localVersion(ctx, cfg); err != nil || version != tc.target {
					t.Fatalf("new image version: %q %v", version, err)
				}
			}
		})
	}
}
