package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
	"github.com/Purestreams/xmesh/internal/store"
)

func upgradeRecoveryFixture(t *testing.T, now *time.Time) (*Server, *store.Store) {
	t.Helper()
	s, state := testServer(t, func(st *model.State) error {
		st.Gateways["g"] = model.Gateway{ID: "g", Name: "Gateway", Enabled: true, CredentialHash: auth.SecretHash("g-node"), PublicHost: "edge.example", VMessPort: 8080}
		st.Agents["a"] = model.Agent{ID: "a", Name: "Agent", Enabled: true, CredentialHash: auth.SecretHash("a-node")}
		st.Attachments["r"] = model.Attachment{ID: "r", GatewayID: "g", AgentID: "a", Enabled: true}
		st.Links["l"] = model.Link{ID: "l", AttachmentID: "r", Name: "Test Link", Enabled: true, URL: "reality://edge.example:8443/tunnel"}
		for _, id := range []string{"g", "a"} {
			role := model.RoleAgent
			if id == "g" {
				role = model.RoleGateway
			}
			st.NodeStatus[id] = model.NodeStatus{NodeID: id, Role: role, Online: true, Ready: true, XrayReady: true, BinaryVersion: "v0.3.9", LastSeen: *now}
			st.LinkStatus[id+"/l"] = model.LinkStatus{LinkID: "l", ReporterNodeID: id, Ready: true, Online: true, Connections: 2, LastSeen: now.Add(-time.Second)}
			st.Updaters[id] = model.Updater{NodeID: id, Role: role, Mode: "docker", Arch: "amd64", CredentialHash: auth.SecretHash(id + "-helper"), Protocol: 1, LastSeen: *now}
		}
		st.UpgradeTasks["first"] = model.UpgradeTask{ID: "first", BatchID: "batch", NodeID: "g", Role: model.RoleGateway, Stage: "verifying", Attempt: 1, Mode: "docker", Arch: "amd64", BaselineLinks: []string{"l"}, TargetVersion: "v0.3.9", ExpiresAt: now.Add(time.Hour)}
		st.UpgradeTasks["second"] = model.UpgradeTask{ID: "second", BatchID: "batch", NodeID: "a", Role: model.RoleAgent, Stage: "pending", Mode: "docker", Arch: "amd64", TargetVersion: "v0.3.9", ExpiresAt: now.Add(time.Hour)}
		st.UpgradeBatches["batch"] = model.UpgradeBatch{ID: "batch", Stage: "running", TaskIDs: []string{"first", "second"}}
		return nil
	})
	s.now = func() time.Time { return *now }
	return s, state
}

func reportUpgradeStage(t *testing.T, s *Server, stage string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/updater/task/first/report", strings.NewReader(`{"stage":"`+stage+`","attempt":1}`))
	r.SetPathValue("id", "first")
	r.Header.Set("Authorization", "Bearer g-helper")
	w := httptest.NewRecorder()
	s.updaterReport(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("report %s: %d %s", stage, w.Code, w.Body.String())
	}
}

func reportRecoveryLink(t *testing.T, s *Server, id string, ready bool) {
	t.Helper()
	role := model.RoleAgent
	if id == "g" {
		role = model.RoleGateway
	}
	lastError := ""
	if !ready {
		lastError = "dial timeout"
	}
	data, err := json.Marshal(StatusReport{Status: model.NodeStatus{NodeID: id, Role: role, Online: true, Ready: true, XrayReady: true}, Links: []model.LinkStatus{{LinkID: "l", Online: true, Ready: ready, Connections: 2, LastError: lastError}}})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/status", strings.NewReader(string(data)))
	r.Header.Set("Authorization", "Bearer "+id+"-node")
	w := httptest.NewRecorder()
	s.nodeStatus(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("Link report from %s: %d %s", id, w.Code, w.Body.String())
	}
}

func claimRecoveryTask(s *Server) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/updater/task", nil)
	r.Header.Set("Authorization", "Bearer a-helper")
	w := httptest.NewRecorder()
	s.updaterTask(w, r)
	return w
}

func resumeRecoveryBatch(s *Server) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/admin/upgrades/first/resume", nil)
	r.SetPathValue("id", "first")
	w := httptest.NewRecorder()
	s.resumeUpgrade(w, r)
	return w
}

func TestUpgradeRecoveryWaitsForFreshReportsFromBothEnds(t *testing.T) {
	now := time.Now().UTC()
	s, state := upgradeRecoveryFixture(t, &now)
	reportUpgradeStage(t, s, "succeeded")
	batch := state.Snapshot().UpgradeBatches["batch"]
	if batch.Stage != "recovering" || batch.RecoveryTaskID != "first" || !batch.RecoveryDeadline.Equal(now.Add(upgradeLinkRecoveryTimeout)) {
		t.Fatalf("recovery not persisted: %+v", batch)
	}
	if w := claimRecoveryTask(s); w.Code != http.StatusNoContent {
		t.Fatalf("cached pre-upgrade reports released next task: %d", w.Code)
	}
	now = now.Add(10 * time.Second)
	reportUpgradeStage(t, s, "succeeded")
	if !state.Snapshot().UpgradeBatches["batch"].RecoveryDeadline.Equal(batch.RecoveryDeadline) {
		t.Fatal("duplicate success report extended recovery deadline")
	}
	reportRecoveryLink(t, s, "g", true)
	if state.Snapshot().UpgradeBatches["batch"].Stage != "recovering" {
		t.Fatal("only Gateway recovery released next task")
	}
	reportRecoveryLink(t, s, "a", true)
	if got := state.Snapshot().UpgradeBatches["batch"]; got.Stage != "running" || got.Error != "" || got.RecoveryTaskID != "" {
		t.Fatalf("healthy reports did not release batch: %+v", got)
	}
	if w := claimRecoveryTask(s); w.Code != http.StatusOK {
		t.Fatalf("next task not claimable after recovery: %d %s", w.Code, w.Body.String())
	}
	if state.Snapshot().UpgradeTasks["first"].Attempt != 1 {
		t.Fatal("successful node was upgraded again")
	}
}

func TestUpgradeRecoveryTimeoutPausesUntilExplicitHealthyResume(t *testing.T) {
	now := time.Now().UTC()
	s, state := upgradeRecoveryFixture(t, &now)
	reportUpgradeStage(t, s, "succeeded")
	now = now.Add(upgradeLinkRecoveryTimeout)
	reportRecoveryLink(t, s, "g", true)
	reportRecoveryLink(t, s, "a", false)
	batch := state.Snapshot().UpgradeBatches["batch"]
	if batch.Stage != "paused" || !strings.Contains(batch.Error, "Link l") {
		t.Fatalf("timeout did not pause: %+v", batch)
	}
	if w := resumeRecoveryBatch(s); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "agent a is not ready") || !strings.Contains(w.Body.String(), "dial timeout") {
		t.Fatalf("unhealthy peer could be resumed: %d %s", w.Code, w.Body.String())
	}
	reportRecoveryLink(t, s, "a", true)
	if state.Snapshot().UpgradeBatches["batch"].Stage != "paused" {
		t.Fatal("timed-out batch resumed without an explicit request")
	}
	if w := resumeRecoveryBatch(s); w.Code != http.StatusSeeOther {
		t.Fatalf("healthy batch could not resume: %d %s", w.Code, w.Body.String())
	}
	// Refresh the helper heartbeat as its last report preceded the wait window.
	if err := state.Update(func(st *model.State) error {
		up := st.Updaters["a"]
		up.LastSeen = now
		st.Updaters["a"] = up
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := claimRecoveryTask(s); w.Code != http.StatusOK {
		t.Fatalf("resumed batch did not continue: %d %s", w.Code, w.Body.String())
	}
}

func TestUpgradeRecoveryLastTaskCompletesBatch(t *testing.T) {
	now := time.Now().UTC()
	s, state := upgradeRecoveryFixture(t, &now)
	if err := state.Update(func(st *model.State) error {
		batch := st.UpgradeBatches["batch"]
		batch.TaskIDs = []string{"first"}
		st.UpgradeBatches["batch"] = batch
		delete(st.UpgradeTasks, "second")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reportUpgradeStage(t, s, "succeeded")
	reportRecoveryLink(t, s, "g", true)
	reportRecoveryLink(t, s, "a", true)
	if state.Snapshot().UpgradeBatches["batch"].Stage != "succeeded" {
		t.Fatal("last task waited forever without another task poll")
	}
}

func TestUpgradeRecoveryLastTaskTimeoutCheckedByHeartbeat(t *testing.T) {
	now := time.Now().UTC()
	s, state := upgradeRecoveryFixture(t, &now)
	if err := state.Update(func(st *model.State) error {
		batch := st.UpgradeBatches["batch"]
		batch.TaskIDs = []string{"first"}
		st.UpgradeBatches["batch"] = batch
		delete(st.UpgradeTasks, "second")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reportUpgradeStage(t, s, "succeeded")
	now = now.Add(upgradeLinkRecoveryTimeout)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/updater/heartbeat", strings.NewReader(`{"mode":"docker","arch":"amd64","version":"v0.3.4","protocol":1}`))
	r.Header.Set("Authorization", "Bearer g-helper")
	w := httptest.NewRecorder()
	s.updaterHeartbeat(w, r)
	if w.Code != http.StatusNoContent || state.Snapshot().UpgradeBatches["batch"].Stage != "paused" {
		t.Fatalf("heartbeat did not check final-task deadline: %d", w.Code)
	}
}

func TestUpgradeRecoveryRequiresEveryBaselineLinkAndFreshPeer(t *testing.T) {
	now := time.Now().UTC()
	s, state := upgradeRecoveryFixture(t, &now)
	reportUpgradeStage(t, s, "succeeded")
	base := state.Snapshot()
	for _, id := range []string{"g", "a"} {
		status := base.LinkStatus[id+"/l"]
		status.LastSeen = now
		base.LinkStatus[id+"/l"] = status
	}
	for _, scenario := range []struct {
		name   string
		change func(*model.State)
		want   string
	}{
		{"stale-agent", func(st *model.State) {
			status := st.LinkStatus["a/l"]
			status.LastSeen = now.Add(-46 * time.Second)
			st.LinkStatus["a/l"] = status
		}, "agent a report is older than 45 seconds"},
		{"offline-agent", func(st *model.State) {
			status := st.LinkStatus["a/l"]
			status.Online = false
			st.LinkStatus["a/l"] = status
		}, "agent a is offline"},
		{"missing-agent", func(st *model.State) { delete(st.LinkStatus, "a/l") }, "agent a has not reported"},
		{"disabled-link", func(st *model.State) { link := st.Links["l"]; link.Enabled = false; st.Links["l"] = link }, "Link configuration is unavailable"},
		{"second-link-broken", func(st *model.State) {
			st.Links["other"] = model.Link{ID: "other", AttachmentID: "r", Enabled: true}
			task := st.UpgradeTasks["first"]
			task.BaselineLinks = append(task.BaselineLinks, "other")
			st.UpgradeTasks["first"] = task
		}, "Link other"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			st := base.Clone()
			scenario.change(&st)
			if problem := s.upgradeRecoveryProblem(&st, st.UpgradeTasks["first"], now); !strings.Contains(problem, scenario.want) {
				t.Fatalf("recovery ignored %s: %q", scenario.name, problem)
			}
		})
	}
}

func TestUpgradeRecoveryUsesConfiguredOfflineThreshold(t *testing.T) {
	now := time.Now().UTC()
	s, state := upgradeRecoveryFixture(t, &now)
	st := state.Snapshot()
	task := st.UpgradeTasks["first"]
	task.UpdatedAt = now.Add(-3 * time.Minute)
	for _, id := range []string{"g/l", "a/l"} {
		status := st.LinkStatus[id]
		status.LastSeen = now.Add(-time.Minute)
		st.LinkStatus[id] = status
	}
	s.cfg.NodeOfflineAfterSeconds = 120
	if problem := s.upgradeRecoveryProblem(&st, task, now); problem != "" {
		t.Fatalf("fresh reports rejected with 120-second threshold: %s", problem)
	}
	s.cfg.NodeOfflineAfterSeconds = 30
	if problem := s.upgradeRecoveryProblem(&st, task, now); !strings.Contains(problem, "older than 30 seconds") {
		t.Fatalf("stale reports accepted with 30-second threshold: %s", problem)
	}
}

func TestUpgradeRecoveryResumeRechecksEarlierCompletedTasks(t *testing.T) {
	now := time.Now().UTC()
	s, state := upgradeRecoveryFixture(t, &now)
	reportUpgradeStage(t, s, "succeeded")
	if err := state.Update(func(st *model.State) error {
		st.Links["earlier"] = model.Link{ID: "earlier", AttachmentID: "r", Enabled: true}
		st.UpgradeTasks["prior"] = model.UpgradeTask{ID: "prior", BatchID: "batch", NodeID: "a", Role: model.RoleAgent, Stage: "succeeded", BaselineLinks: []string{"earlier"}, UpdatedAt: now.Add(-time.Minute)}
		batch := st.UpgradeBatches["batch"]
		batch.TaskIDs = []string{"prior", "first", "second"}
		batch.Stage, batch.Error = "paused", upgradeLinkRecoveryError+"l did not recover"
		st.UpgradeBatches["batch"] = batch
		for _, id := range []string{"g", "a"} {
			status := st.LinkStatus[id+"/l"]
			status.LastSeen = now
			st.LinkStatus[id+"/l"] = status
			status.LinkID = "earlier"
			status.Ready = id == "g"
			st.LinkStatus[id+"/earlier"] = status
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := resumeRecoveryBatch(s); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Link earlier") {
		t.Fatalf("resume ignored an earlier broken Link: %d %s", w.Code, w.Body.String())
	}
}

func TestUpgradeRecoveryCancellationAndFailureCannotResume(t *testing.T) {
	for _, phase := range []string{"cancel-before-success", "cancel-during-recovery", "failed", "rolled_back", "manual_intervention"} {
		t.Run(phase, func(t *testing.T) {
			now := time.Now().UTC()
			s, state := upgradeRecoveryFixture(t, &now)
			if phase == "cancel-during-recovery" {
				reportUpgradeStage(t, s, "succeeded")
			}
			if strings.HasPrefix(phase, "cancel") {
				r := httptest.NewRequest(http.MethodPost, "/admin/upgrades/second/cancel", nil)
				r.SetPathValue("id", "second")
				w := httptest.NewRecorder()
				s.cancelUpgrade(w, r)
				if w.Code != http.StatusSeeOther {
					t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
				}
				if phase == "cancel-before-success" {
					reportUpgradeStage(t, s, "succeeded")
				}
			} else {
				reportUpgradeStage(t, s, phase)
			}
			reportRecoveryLink(t, s, "g", true)
			reportRecoveryLink(t, s, "a", true)
			if state.Snapshot().UpgradeBatches["batch"].Stage != "paused" {
				t.Fatal("health reports overrode cancellation/failure")
			}
			if w := resumeRecoveryBatch(s); w.Code != http.StatusConflict {
				t.Fatalf("resumed cancelled or failed batch: %d", w.Code)
			}
		})
	}
}

func TestUpgradeRecoverySurvivesControllerRestart(t *testing.T) {
	now := time.Now().UTC()
	s, state := upgradeRecoveryFixture(t, &now)
	reportUpgradeStage(t, s, "succeeded")
	deadline := state.Snapshot().UpgradeBatches["batch"].RecoveryDeadline
	data, err := json.Marshal(state.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(s.cfg, reopened, s.logger)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return now }
	now = now.Add(10 * time.Second)
	if !reopened.Snapshot().UpgradeBatches["batch"].RecoveryDeadline.Equal(deadline) {
		t.Fatal("restart reset recovery deadline")
	}
	reportRecoveryLink(t, restarted, "g", true)
	reportRecoveryLink(t, restarted, "a", true)
	if w := claimRecoveryTask(restarted); w.Code != http.StatusOK {
		t.Fatalf("restart lost recovery state: %d %s", w.Code, w.Body.String())
	}
}

func TestUpgradeRecoveryLegacyResumeAndAccessControl(t *testing.T) {
	now := time.Now().UTC()
	s, state := upgradeRecoveryFixture(t, &now)
	reportUpgradeStage(t, s, "succeeded")
	if err := state.Update(func(st *model.State) error {
		batch := st.UpgradeBatches["batch"]
		batch.Stage, batch.Error = "paused", upgradeLinkRecoveryError+"l did not recover"
		batch.RecoveryTaskID, batch.RecoveryDeadline = "", time.Time{}
		st.UpgradeBatches["batch"] = batch
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reportRecoveryLink(t, s, "g", true)
	if w := resumeRecoveryBatch(s); w.Code != http.StatusConflict {
		t.Fatal("legacy batch bypassed missing fresh Agent report")
	}
	reportRecoveryLink(t, s, "a", true)
	makeRequest := func(cookie, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/admin/upgrades/first/resume", strings.NewReader(url.Values{"csrf": {csrf}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := makeRequest("", ""); w.Header().Get("Location") != "/login" {
		t.Fatalf("resume does not require admin authentication: %d", w.Code)
	}
	cookie := auth.SignSession(s.cfg.adminSessionKey(), "admin", now.Add(time.Hour))
	if w := makeRequest(cookie, ""); w.Code != http.StatusForbidden {
		t.Fatalf("resume does not require CSRF: %d", w.Code)
	}
	panel := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	s.Handler().ServeHTTP(panel, r)
	if panel.Code != http.StatusOK || !strings.Contains(panel.Body.String(), `action="/admin/upgrades/first/resume"`) {
		t.Fatalf("legacy batch resume button missing: %d", panel.Code)
	}
	if w := makeRequest(cookie, auth.Derive(s.cfg.sessionKey(), "csrf", cookie)); w.Code != http.StatusSeeOther || w.Header().Get("Location") == "/login" {
		t.Fatalf("authenticated legacy resume failed: %d %s", w.Code, w.Body.String())
	}
}

func TestUpgradeRecoveryResumeRejectsExpiredPendingTask(t *testing.T) {
	now := time.Now().UTC()
	s, state := upgradeRecoveryFixture(t, &now)
	reportUpgradeStage(t, s, "succeeded")
	now = now.Add(upgradeLinkRecoveryTimeout)
	reportRecoveryLink(t, s, "a", false)
	reportRecoveryLink(t, s, "g", true)
	reportRecoveryLink(t, s, "a", true)
	if err := state.Update(func(st *model.State) error {
		task := st.UpgradeTasks["second"]
		task.ExpiresAt = now
		st.UpgradeTasks["second"] = task
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := resumeRecoveryBatch(s); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "expired") {
		t.Fatalf("resumed expired pending task: %d %s", w.Code, w.Body.String())
	}
}

// Opt-in local fixture used by tests/upgrade-recovery.browser.cjs.
func TestUpgradeRecoveryBrowserFixture(t *testing.T) {
	address := os.Getenv("XMESH_UPGRADE_BROWSER_ADDR")
	if address == "" {
		t.Skip("set XMESH_UPGRADE_BROWSER_ADDR for browser tests")
	}
	now := time.Now().UTC()
	s, state := upgradeRecoveryFixture(t, &now)
	reportUpgradeStage(t, s, "succeeded")
	if err := state.Update(func(st *model.State) error {
		batch := st.UpgradeBatches["batch"]
		batch.Stage, batch.Error = "paused", upgradeLinkRecoveryError+"l did not recover"
		batch.RecoveryTaskID, batch.RecoveryDeadline = "", time.Time{}
		st.UpgradeBatches["batch"] = batch
		for id, status := range st.LinkStatus {
			status.LastSeen = now
			st.LinkStatus[id] = status
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.now = time.Now
	s.cfg.PublicURL = "http://" + address
	t.Logf("Upgrade recovery fixture: http://%s", address)
	if err := http.ListenAndServe(address, s.Handler()); err != nil {
		t.Fatal(err)
	}
}
