package controller

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/model"
)

func TestUpgradePrefetchPinsVersionAcrossControllerSettingChange(t *testing.T) {
	version := "v0.3.3"
	assets := map[string][]byte{}
	var manifest strings.Builder
	for _, name := range releaseAssets(version)[1:] {
		payload := []byte("content of " + name)
		assets[name] = payload
		sum := sha256.Sum256(payload)
		fmt.Fprintf(&manifest, "%x  %s\n", sum, name)
	}
	assets["SHA256SUMS"] = []byte(manifest.String())
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/"+version+"/")
		if data, ok := assets[name]; ok {
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	s, state := testServer(t, func(st *model.State) error {
		st.Agents["a"] = model.Agent{ID: "a", Enabled: true}
		st.NodeStatus["a"] = model.NodeStatus{NodeID: "a", Role: model.RoleAgent, BinaryVersion: "v0.3.2", LastSeen: time.Now()}
		st.Updaters = map[string]model.Updater{"a": {NodeID: "a", Role: model.RoleAgent, CredentialHash: auth.SecretHash("updater"), Mode: "docker", Arch: "amd64", Protocol: 1, LastSeen: time.Now()}}
		return nil
	})
	s.now = time.Now
	s.cfg.ReleaseBaseURL = upstream.URL
	s.cfg.ReleaseDir = t.TempDir()
	s.cfg.ReleaseVersion = version
	s.releaseHTTPClient = upstream.Client()
	form := url.Values{"node_id": {"a"}, "version": {version}}
	r := httptest.NewRequest(http.MethodPost, "/admin/upgrades", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.createUpgrade(w, r)
	if w.Code != 303 {
		t.Fatalf("create upgrade: %d %s", w.Code, w.Body.String())
	}
	var task model.UpgradeTask
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, candidate := range state.Snapshot().UpgradeTasks {
			task = candidate
		}
		if task.Stage == "pending" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if task.Stage != "pending" || task.SHA256 == "" || task.TargetVersion != version {
		t.Fatalf("prefetch did not pin release: %+v", task)
	}
	s.cfg.ReleaseVersion = "v0.3.4"
	request := httptest.NewRequest(http.MethodGet, "/releases/"+version+"/"+task.Asset, nil)
	request.SetPathValue("version", version)
	request.SetPathValue("asset", task.Asset)
	w = httptest.NewRecorder()
	s.releaseAsset(w, request)
	if w.Code != 200 || w.Body.String() != string(assets[task.Asset]) {
		t.Fatalf("pinned release unavailable after setting change: %d %s", w.Code, w.Body.String())
	}
}

func TestUpdaterClaimIsNodeScopedAndIdempotent(t *testing.T) {
	s, state := testServer(t, func(st *model.State) error {
		st.Gateways["g"] = model.Gateway{ID: "g", Enabled: true}
		st.Agents["a"] = model.Agent{ID: "a", Enabled: true}
		st.Updaters = map[string]model.Updater{
			"g": {NodeID: "g", Role: model.RoleGateway, CredentialHash: auth.SecretHash("gateway-updater"), Mode: "docker", Arch: "amd64", Protocol: 1, LastSeen: time.Now()},
			"a": {NodeID: "a", Role: model.RoleAgent, CredentialHash: auth.SecretHash("agent-updater"), Mode: "docker", Arch: "amd64", Protocol: 1, LastSeen: time.Now()},
		}
		st.UpgradeTasks = map[string]model.UpgradeTask{
			"first":  {ID: "first", BatchID: "batch", NodeID: "g", Role: model.RoleGateway, Mode: "docker", Arch: "amd64", Stage: "pending", Asset: "xmesh-v2-linux-amd64.tar.gz", SHA256: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Hour)},
			"second": {ID: "second", BatchID: "batch", NodeID: "a", Role: model.RoleAgent, Mode: "docker", Arch: "amd64", Stage: "pending", Asset: "xmesh-v2-linux-amd64.tar.gz", SHA256: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Hour)},
		}
		st.UpgradeBatches = map[string]model.UpgradeBatch{"batch": {ID: "batch", Stage: "running", TaskIDs: []string{"first", "second"}}}
		return nil
	})
	s.now = time.Now
	claim := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/updater/task", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.updaterTask(w, r)
		return w
	}
	if got := claim("node-credential").Code; got != 401 {
		t.Fatalf("node credential claimed task: %d", got)
	}
	if got := claim("agent-updater").Code; got != 204 {
		t.Fatalf("later batch member claimed early: %d", got)
	}
	for i := 0; i < 2; i++ {
		if got := claim("gateway-updater").Code; got != 200 {
			t.Fatalf("gateway claim %d: %d", i, got)
		}
	}
	if got := state.Snapshot().UpgradeTasks["first"].Attempt; got != 1 {
		t.Fatalf("repeat claim incremented attempt: %d", got)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/updater/task/first/report", strings.NewReader(`{"stage":"succeeded","attempt":1}`))
	r.SetPathValue("id", "first")
	r.Header.Set("Authorization", "Bearer agent-updater")
	w := httptest.NewRecorder()
	s.updaterReport(w, r)
	if w.Code != 409 {
		t.Fatalf("other node completed gateway task: %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/updater/task/first/report", strings.NewReader(`{"stage":"succeeded","attempt":1}`))
	r.SetPathValue("id", "first")
	r.Header.Set("Authorization", "Bearer gateway-updater")
	w = httptest.NewRecorder()
	s.updaterReport(w, r)
	if w.Code != 204 {
		t.Fatalf("gateway completion: %d %s", w.Code, w.Body.String())
	}
	if got := claim("agent-updater").Code; got != 200 {
		t.Fatalf("next batch member not released: %d", got)
	}
}

func TestUpdaterPairTokenIsOneUseAndSeparateFromNodeCredential(t *testing.T) {
	s, state := testServer(t, func(st *model.State) error {
		st.Agents["a"] = model.Agent{ID: "a", Enabled: true, CredentialHash: auth.SecretHash("node-secret")}
		st.Updaters = map[string]model.Updater{"a": {NodeID: "a", Role: model.RoleAgent, CredentialHash: auth.SecretHash("old-helper")}}
		return nil
	})
	s.cfg.ReleaseBaseURL = "https://github.com/example/xmesh/releases/download"
	s.cfg.ReleaseVersion = "v0.3.3"
	s.cfg.ReleaseDir = t.TempDir()
	r := httptest.NewRequest(http.MethodPost, "/admin/updaters/agent/a/pair", nil)
	r.SetPathValue("role", "agent")
	r.SetPathValue("id", "a")
	w := httptest.NewRecorder()
	s.pairUpdater(w, r)
	if w.Code != 200 {
		t.Fatalf("pair token: %d %s", w.Code, w.Body.String())
	}
	token := strings.Split(strings.Split(w.Body.String(), "\n")[0], ": ")[1]
	pair := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/updater/pair", strings.NewReader(`{"token":"`+token+`","node_id":"a","role":"agent"}`))
		w := httptest.NewRecorder()
		s.updaterPair(w, r)
		return w
	}
	if got := pair().Code; got != 200 {
		t.Fatalf("first pairing: %d", got)
	}
	oldRequest := httptest.NewRequest(http.MethodGet, "/api/v1/updater/task", nil)
	oldRequest.Header.Set("Authorization", "Bearer old-helper")
	if _, ok := s.authenticateUpdater(oldRequest); !ok {
		t.Fatal("old helper invalidated before new config was saved")
	}
	retry := pair()
	if retry.Code != 200 {
		t.Fatalf("pair token not reusable after local save failure: %d", retry.Code)
	}
	var replacement struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &replacement); err != nil || replacement.Credential == "" {
		t.Fatalf("invalid replacement credential: %v", err)
	}
	newRequest := httptest.NewRequest(http.MethodGet, "/api/v1/updater/task", nil)
	newRequest.Header.Set("Authorization", "Bearer "+replacement.Credential)
	if _, ok := s.authenticateUpdater(newRequest); !ok {
		t.Fatal("new helper could not activate after saving config")
	}
	if _, ok := s.authenticateUpdater(oldRequest); ok {
		t.Fatal("old helper remained valid after replacement activated")
	}
	if got := pair().Code; got != 401 {
		t.Fatalf("pair token reused after activation: %d", got)
	}
	if state.Snapshot().Updaters["a"].CredentialHash == state.Snapshot().Agents["a"].CredentialHash {
		t.Fatal("updater shares node credential")
	}
}

func TestUpgradeRequiresFreshNodeVersion(t *testing.T) {
	for _, status := range []model.NodeStatus{
		{},
		{NodeID: "a", Role: model.RoleAgent, BinaryVersion: "v0.3.2", LastSeen: time.Now().Add(-time.Minute)},
		{NodeID: "a", Role: model.RoleAgent, LastSeen: time.Now()},
	} {
		t.Run(status.BinaryVersion+status.LastSeen.String(), func(t *testing.T) {
			s, state := testServer(t, func(st *model.State) error {
				st.Agents["a"] = model.Agent{ID: "a", Enabled: true}
				st.NodeStatus["a"] = status
				st.Updaters = map[string]model.Updater{"a": {NodeID: "a", Role: model.RoleAgent, CredentialHash: auth.SecretHash("helper"), Mode: "docker", Arch: "amd64", Protocol: 1, LastSeen: time.Now()}}
				return nil
			})
			s.now = time.Now
			s.cfg.ReleaseBaseURL = "https://example.com/releases"
			s.cfg.ReleaseDir = t.TempDir()
			s.cfg.ReleaseVersion = "v0.3.3"
			r := httptest.NewRequest(http.MethodPost, "/admin/upgrades", strings.NewReader(url.Values{"node_id": {"a"}, "version": {"v0.3.3"}}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			s.createUpgrade(w, r)
			if w.Code == http.StatusSeeOther || len(state.Snapshot().UpgradeTasks) != 0 {
				t.Fatalf("accepted node without fresh version: %d", w.Code)
			}
		})
	}
}

func TestDeleteNodeKeepsActiveUpgradeAndIdentity(t *testing.T) {
	for _, role := range []model.Role{model.RoleGateway, model.RoleAgent} {
		for _, stage := range []string{"preparing", "pending", "switching", "uncertain"} {
			t.Run(string(role)+"/"+stage, func(t *testing.T) {
				s, state := testServer(t, func(st *model.State) error {
					st.Updaters = map[string]model.Updater{}
					st.UpgradeTasks = map[string]model.UpgradeTask{}
					if role == model.RoleGateway {
						st.Gateways["n"] = model.Gateway{ID: "n", Name: "node"}
					} else {
						st.Agents["n"] = model.Agent{ID: "n", Name: "node"}
					}
					st.Updaters["n"] = model.Updater{NodeID: "n", Role: role}
					st.UpgradeTasks["task"] = model.UpgradeTask{ID: "task", NodeID: "n", Stage: stage}
					return nil
				})
				r := httptest.NewRequest(http.MethodPost, "/delete", strings.NewReader(url.Values{"confirm_name": {"node"}}.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.SetPathValue("id", "n")
				w := httptest.NewRecorder()
				if role == model.RoleGateway {
					s.deleteGateway(w, r)
				} else {
					s.deleteAgent(w, r)
				}
				if w.Code == http.StatusSeeOther {
					t.Fatal("deleted node during active upgrade")
				}
				snapshot := state.Snapshot()
				if !nodeExists(&snapshot, role, "n") || snapshot.Updaters["n"].NodeID != "n" || snapshot.UpgradeTasks["task"].Stage != stage {
					t.Fatal("delete changed active upgrade state")
				}
			})
		}
	}
}

func TestClaimedUpgradeRemainsAvailableAfterDeadline(t *testing.T) {
	now := time.Now().UTC()
	s, state := testServer(t, func(st *model.State) error {
		st.Updaters = map[string]model.Updater{}
		st.UpgradeTasks = map[string]model.UpgradeTask{}
		st.Agents["a"] = model.Agent{ID: "a", Enabled: true}
		st.Updaters["a"] = model.Updater{NodeID: "a", Role: model.RoleAgent, CredentialHash: auth.SecretHash("helper"), Mode: "docker", Arch: "amd64", Protocol: 1, LastSeen: now}
		st.UpgradeTasks["task"] = model.UpgradeTask{ID: "task", NodeID: "a", Role: model.RoleAgent, Mode: "docker", Arch: "amd64", Stage: "switching", Attempt: 1, ExpiresAt: now.Add(-time.Hour)}
		return nil
	})
	s.now = func() time.Time { return now }
	r := httptest.NewRequest(http.MethodGet, "/api/v1/updater/task", nil)
	r.Header.Set("Authorization", "Bearer helper")
	w := httptest.NewRecorder()
	s.updaterTask(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"stage":"switching"`) {
		t.Fatalf("claimed task lost after deadline: %d %s", w.Code, w.Body.String())
	}
	if state.Snapshot().UpgradeTasks["task"].Attempt != 1 {
		t.Fatal("recovery claim incremented attempt")
	}
}

func TestPairingBlocksUpgradeUntilActivation(t *testing.T) {
	now := time.Now().UTC()
	s, state := testServer(t, func(st *model.State) error {
		st.Agents["a"] = model.Agent{ID: "a", Enabled: true}
		st.NodeStatus["a"] = model.NodeStatus{NodeID: "a", Role: model.RoleAgent, BinaryVersion: "v0.3.2", LastSeen: now}
		st.Updaters = map[string]model.Updater{"a": {NodeID: "a", Role: model.RoleAgent, CredentialHash: auth.SecretHash("old-helper"), PairTokenHash: auth.SecretHash("pair"), PairExpiresAt: now.Add(time.Hour), Mode: "docker", Arch: "amd64", Protocol: 1, LastSeen: now}}
		return nil
	})
	s.now = func() time.Time { return now }
	s.cfg.ReleaseVersion = "v0.3.3"
	s.cfg.ReleaseBaseURL = "https://example.com/releases"
	s.cfg.ReleaseDir = t.TempDir()
	r := httptest.NewRequest(http.MethodPost, "/admin/upgrades", strings.NewReader(url.Values{"node_id": {"a"}, "version": {"v0.3.3"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.createUpgrade(w, r)
	if w.Code == http.StatusSeeOther || len(state.Snapshot().UpgradeTasks) != 0 {
		t.Fatalf("created task while pairing was pending: %d %s", w.Code, w.Body.String())
	}
	newRequest := httptest.NewRequest(http.MethodPost, "/api/v1/updater/pair", strings.NewReader(`{"token":"pair","node_id":"a","role":"agent"}`))
	w = httptest.NewRecorder()
	s.updaterPair(w, newRequest)
	if w.Code != http.StatusOK {
		t.Fatalf("pair: %d %s", w.Code, w.Body.String())
	}
	var paired struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &paired); err != nil {
		t.Fatal(err)
	}
	activate := httptest.NewRequest(http.MethodGet, "/api/v1/updater/task", nil)
	activate.Header.Set("Authorization", "Bearer "+paired.Credential)
	if _, ok := s.authenticateUpdater(activate); !ok {
		t.Fatal("new helper did not activate")
	}
	w = httptest.NewRecorder()
	s.createUpgrade(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("task still blocked after pairing activated: %d %s", w.Code, w.Body.String())
	}
}

func TestManualUpgradeReservesNodeAgainstRemoteTask(t *testing.T) {
	now := time.Now().UTC()
	s, state := testServer(t, func(st *model.State) error {
		st.Agents["a"] = model.Agent{ID: "a", Enabled: true}
		st.NodeStatus["a"] = model.NodeStatus{NodeID: "a", Role: model.RoleAgent, BinaryVersion: "v0.3.2", LastSeen: now}
		st.Updaters = map[string]model.Updater{"a": {NodeID: "a", Role: model.RoleAgent, CredentialHash: auth.SecretHash("helper"), Mode: "docker", Arch: "amd64", Protocol: 1, LastSeen: now}}
		st.UpgradeTasks = map[string]model.UpgradeTask{"queued": {ID: "queued", NodeID: "a", Stage: "pending"}}
		return nil
	})
	s.now = func() time.Time { return now }
	start := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/updater/manual", strings.NewReader(`{"id":"`+id+`","from_version":"v0.3.2","target_version":"v0.3.3","sha256":"`+strings.Repeat("a", 64)+`","mode":"docker","arch":"amd64"}`))
		r.Header.Set("Authorization", "Bearer helper")
		w := httptest.NewRecorder()
		s.startManualUpgrade(w, r)
		return w
	}
	if w := start("manual-task-123"); w.Code != http.StatusConflict {
		t.Fatalf("manual upgrade overlapped queued task: %d %s", w.Code, w.Body.String())
	}
	if err := state.Update(func(st *model.State) error {
		task := st.UpgradeTasks["queued"]
		task.Stage = "cancelled"
		st.UpgradeTasks["queued"] = task
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := start("manual-task-123"); w.Code != http.StatusOK {
		t.Fatalf("manual reservation failed: %d %s", w.Code, w.Body.String())
	}
	if w := start("manual-task-123"); w.Code != http.StatusOK {
		t.Fatalf("idempotent manual retry failed: %d %s", w.Code, w.Body.String())
	}
	if w := start("manual-task-456"); w.Code != http.StatusConflict {
		t.Fatalf("second manual upgrade overlapped first: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/updater/task", nil)
	r.Header.Set("Authorization", "Bearer helper")
	w := httptest.NewRecorder()
	s.updaterTask(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("daemon claimed manual task: %d %s", w.Code, w.Body.String())
	}
}
