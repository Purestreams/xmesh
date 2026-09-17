package controller

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/identity"
	"xmesh/internal/model"
)

const updaterProtocol = 1

type updaterView struct {
	State, Mode, Arch, Version string
	LastSeen                   time.Time
}

func updaterViews(state model.State, now time.Time) map[string]updaterView {
	result := map[string]updaterView{}
	for id := range state.Gateways {
		result[id] = updaterView{State: "未配对"}
	}
	for id := range state.Agents {
		result[id] = updaterView{State: "未配对"}
	}
	for id, up := range state.Updaters {
		stage := "未配对"
		if up.CredentialHash != "" {
			stage = "离线"
			if now.Sub(up.LastSeen) < 45*time.Second {
				stage = "在线"
			}
		}
		result[id] = updaterView{State: stage, Mode: up.Mode, Arch: up.Arch, Version: up.Version, LastSeen: up.LastSeen}
	}
	return result
}
func sortedUpgradeTasks(state model.State) []model.UpgradeTask {
	tasks := make([]model.UpgradeTask, 0, len(state.UpgradeTasks))
	for _, task := range state.UpgradeTasks {
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.After(tasks[j].CreatedAt) })
	return tasks
}

func nodeExists(state *model.State, role model.Role, id string) bool {
	if role == model.RoleGateway {
		_, ok := state.Gateways[id]
		return ok
	}
	if role == model.RoleAgent {
		_, ok := state.Agents[id]
		return ok
	}
	return false
}

func activeUpgrade(stage string) bool {
	return stage == "preparing" || stage == "pending" || stage == "running" || stage == "uncertain" || stage == "downloading" || stage == "prepared" || stage == "switching" || stage == "verifying" || stage == "rolling_back"
}

func pairingInProgress(up model.Updater, now time.Time) bool {
	return up.PairTokenHash != "" && now.Before(up.PairExpiresAt)
}

func (s *Server) pairUpdater(w http.ResponseWriter, r *http.Request) {
	if !s.releaseEnabled() || !isReleaseAsset(s.cfg.ReleaseVersion, "install-updater.sh") {
		http.Error(w, "configure a release that includes the node updater first", 409)
		return
	}
	role, id := model.Role(r.PathValue("role")), r.PathValue("id")
	token, err := identity.Token(32)
	if err != nil {
		http.Error(w, "token generation failed", 500)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		if !nodeExists(state, role, id) {
			return errors.New("node not found")
		}
		for _, task := range state.UpgradeTasks {
			if task.NodeID == id && activeUpgrade(task.Stage) {
				return errors.New("updater has an active task")
			}
		}
		if state.Updaters == nil {
			state.Updaters = map[string]model.Updater{}
		}
		up := state.Updaters[id]
		up.NodeID, up.Role, up.PairTokenHash, up.PairExpiresAt = id, role, auth.SecretHash(token), s.now().Add(30*time.Minute).UTC()
		up.PendingCredentialHash = ""
		state.Updaters[id] = up
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "Pair token (expires in 30 minutes): %s\nCopy one command to the node host. The token is included and the existing node identity is preserved.\n\nsystemd:\n%s\n\nDocker Compose (edit --install-dir if needed):\n%s\n", token, s.updaterInstallCommand(role, id, "systemd", token), s.updaterInstallCommand(role, id, "docker", token))
}

func (s *Server) updaterInstallCommand(role model.Role, id, mode, token string) string {
	base := strings.TrimSuffix(s.cfg.PublicURL, "/") + "/releases/" + s.cfg.ReleaseVersion
	args := ""
	if mode == "docker" {
		args = " --install-dir " + shellQuote("/opt/xmesh-docker-"+string(role))
	}
	return fmt.Sprintf("(set -eu; work=$(mktemp -d); trap 'rm -rf \"$work\"' EXIT; curl -fL --retry 3 --proto '=https' --proto-redir '=https' -o \"$work/SHA256SUMS\" %s; curl -fL --retry 3 --proto '=https' --proto-redir '=https' -o \"$work/install-updater.sh\" %s; (cd \"$work\" && grep '  install-updater.sh$' SHA256SUMS | sha256sum -c -); printf '%%s\\n' %s | sudo sh \"$work/install-updater.sh\" --controller %s --release-base-url %s --version %s --role %s --node-id %s --mode %s%s --token-stdin)", shellQuote(base+"/SHA256SUMS"), shellQuote(base+"/install-updater.sh"), shellQuote(token), shellQuote(s.cfg.PublicURL), shellQuote(strings.TrimSuffix(s.cfg.PublicURL, "/")+"/releases"), shellQuote(s.cfg.ReleaseVersion), shellQuote(string(role)), shellQuote(id), shellQuote(mode), args)
}

type updaterPairRequest struct {
	Token  string     `json:"token"`
	NodeID string     `json:"node_id"`
	Role   model.Role `json:"role"`
}

func (s *Server) updaterPair(w http.ResponseWriter, r *http.Request) {
	var req updaterPairRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	credential, err := identity.Token(32)
	if err != nil {
		http.Error(w, "credential generation failed", 500)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		up, ok := state.Updaters[req.NodeID]
		if !ok || up.Role != req.Role || !nodeExists(state, up.Role, up.NodeID) || up.PairTokenHash == "" || !s.now().Before(up.PairExpiresAt) || !auth.EqualSecretHash(up.PairTokenHash, req.Token) {
			return errors.New("invalid or expired pairing token")
		}
		// Keep the old credential and pairing token until the new helper
		// successfully authenticates after persisting its local config.
		up.PendingCredentialHash = auth.SecretHash(credential)
		state.Updaters[up.NodeID] = up
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 401)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"credential": credential})
}

func (s *Server) authenticateUpdater(r *http.Request) (model.Updater, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return model.Updater{}, false
	}
	credential := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if credential == "" {
		return model.Updater{}, false
	}
	state := s.store.Snapshot()
	for _, up := range state.Updaters {
		if !nodeExists(&state, up.Role, up.NodeID) {
			continue
		}
		if up.CredentialHash != "" && auth.EqualSecretHash(up.CredentialHash, credential) {
			return up, true
		}
		if pairingInProgress(up, s.now()) && up.PendingCredentialHash != "" && auth.EqualSecretHash(up.PendingCredentialHash, credential) {
			var promoted model.Updater
			err := s.store.Update(func(current *model.State) error {
				candidate := current.Updaters[up.NodeID]
				if !nodeExists(current, candidate.Role, candidate.NodeID) || !pairingInProgress(candidate, s.now()) || candidate.PendingCredentialHash == "" || !auth.EqualSecretHash(candidate.PendingCredentialHash, credential) {
					return errors.New("pairing changed")
				}
				candidate.CredentialHash = candidate.PendingCredentialHash
				candidate.PendingCredentialHash = ""
				candidate.PairTokenHash = ""
				candidate.PairExpiresAt = time.Time{}
				current.Updaters[candidate.NodeID] = candidate
				promoted = candidate
				return nil
			})
			return promoted, err == nil
		}
	}
	return model.Updater{}, false
}

type updaterHeartbeatRequest struct {
	Mode     string `json:"mode"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

func (s *Server) updaterHeartbeat(w http.ResponseWriter, r *http.Request) {
	up, ok := s.authenticateUpdater(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	var req updaterHeartbeatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if (req.Mode != "systemd" && req.Mode != "docker") || (req.Arch != "amd64" && req.Arch != "arm64") || req.Protocol != updaterProtocol {
		http.Error(w, "unsupported updater", 400)
		return
	}
	err := s.store.Update(func(state *model.State) error {
		current := state.Updaters[up.NodeID]
		current.Mode, current.Arch, current.Version, current.Protocol, current.LastSeen = req.Mode, req.Arch, req.Version, req.Protocol, s.now().UTC()
		state.Updaters[up.NodeID] = current
		return nil
	})
	if err != nil {
		http.Error(w, "failed to record heartbeat", 500)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) createUpgrade(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	version := strings.TrimSpace(r.FormValue("version"))
	if !releaseVersionPattern.MatchString(version) || !strings.HasPrefix(version, "v") || !s.releaseEnabled() || !isReleaseAsset(version, "install-updater.sh") {
		http.Error(w, "invalid release version or release source", 400)
		return
	}
	ids := r.Form["node_id"]
	if len(ids) == 0 || len(ids) > 100 {
		http.Error(w, "select 1 to 100 nodes", 400)
		return
	}
	batchID, err := identity.Token(12)
	if err != nil {
		http.Error(w, "task generation failed", 500)
		return
	}
	var tasks []model.UpgradeTask
	now := s.now().UTC()
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			http.Error(w, "duplicate node", 400)
			return
		}
		seen[id] = true
		taskID, err := identity.Token(12)
		if err != nil {
			http.Error(w, "task generation failed", 500)
			return
		}
		tasks = append(tasks, model.UpgradeTask{ID: taskID, BatchID: batchID, NodeID: id, Actor: s.cfg.AdminUsername, TargetVersion: version, Stage: "preparing", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(24 * time.Hour)})
	}
	err = s.store.Update(func(state *model.State) error {
		for i := range tasks {
			t := &tasks[i]
			up, ok := state.Updaters[t.NodeID]
			if !ok || up.CredentialHash == "" || up.Protocol != updaterProtocol || s.now().Sub(up.LastSeen) > 45*time.Second {
				return fmt.Errorf("updater for %s is unavailable", t.NodeID)
			}
			if pairingInProgress(up, s.now()) {
				return fmt.Errorf("updater for %s has an unfinished pairing", t.NodeID)
			}
			if !nodeExists(state, up.Role, t.NodeID) {
				return fmt.Errorf("node %s not found", t.NodeID)
			}
			if up.Role == model.RoleGateway && !state.Gateways[t.NodeID].Enabled || up.Role == model.RoleAgent && !state.Agents[t.NodeID].Enabled {
				return fmt.Errorf("node %s is disabled", t.NodeID)
			}
			for _, existing := range state.UpgradeTasks {
				if existing.NodeID == t.NodeID && activeUpgrade(existing.Stage) {
					return fmt.Errorf("node %s already has an upgrade", t.NodeID)
				}
			}
			status := state.NodeStatus[t.NodeID]
			if status.NodeID != t.NodeID || status.Role != up.Role || status.BinaryVersion == "" || status.LastSeen.IsZero() || s.now().Sub(status.LastSeen) > 45*time.Second {
				return fmt.Errorf("node %s has no fresh version status", t.NodeID)
			}
			if status.BinaryVersion == version {
				return fmt.Errorf("node %s already runs %s", t.NodeID, version)
			}
			t.Role, t.Mode, t.Arch, t.FromVersion = up.Role, up.Mode, up.Arch, status.BinaryVersion
			t.Asset = fmt.Sprintf("xmesh-%s-linux-%s.tar.gz", version, up.Arch)
			for _, attachment := range state.Attachments {
				if attachment.GatewayID != t.NodeID && attachment.AgentID != t.NodeID {
					continue
				}
				for _, link := range state.Links {
					if link.AttachmentID != attachment.ID || !link.Enabled {
						continue
					}
					peerID := attachment.GatewayID
					if peerID == t.NodeID {
						peerID = attachment.AgentID
					}
					local, peer := state.LinkStatus[t.NodeID+"/"+link.ID], state.LinkStatus[peerID+"/"+link.ID]
					if local.Ready && local.Online && peer.Ready && peer.Online && s.now().Sub(local.LastSeen) < 45*time.Second && s.now().Sub(peer.LastSeen) < 45*time.Second {
						t.BaselineLinks = append(t.BaselineLinks, link.ID)
					}
				}
			}
			sort.Strings(t.BaselineLinks)
		}
		if state.UpgradeTasks == nil {
			state.UpgradeTasks = map[string]model.UpgradeTask{}
		}
		if state.UpgradeBatches == nil {
			state.UpgradeBatches = map[string]model.UpgradeBatch{}
		}
		batch := model.UpgradeBatch{ID: batchID, Stage: "preparing", CreatedAt: now}
		for _, task := range tasks {
			state.UpgradeTasks[task.ID] = task
			batch.TaskIDs = append(batch.TaskIDs, task.ID)
		}
		state.UpgradeBatches[batchID] = batch
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	go s.prepareUpgradeBatch(batchID)
	http.Redirect(w, r, "/?notice="+url.QueryEscape("Upgrade batch queued: "+batchID), http.StatusSeeOther)
}

func (s *Server) prepareUpgradeBatch(id string) {
	state := s.store.Snapshot()
	batch, ok := state.UpgradeBatches[id]
	if !ok || batch.Stage != "preparing" {
		return
	}
	if len(batch.TaskIDs) == 0 {
		return
	}
	version := state.UpgradeTasks[batch.TaskIDs[0]].TargetVersion
	provider := s.releaseServer(version)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	var prepErr error
	if err := provider.ensureReleaseAsset(ctx, "SHA256SUMS"); err != nil {
		prepErr = err
	}
	hashes := map[string]string{}
	if prepErr == nil {
		for _, taskID := range batch.TaskIDs {
			asset := state.UpgradeTasks[taskID].Asset
			if hashes[asset] != "" {
				continue
			}
			if err := provider.ensureReleaseAsset(ctx, asset); err != nil {
				prepErr = err
				break
			}
			hashes[asset], prepErr = provider.releaseExpectedHash(asset)
			if prepErr != nil {
				break
			}
		}
	}
	_ = s.store.Update(func(next *model.State) error {
		batch := next.UpgradeBatches[id]
		if batch.Stage != "preparing" {
			return nil
		}
		if prepErr != nil {
			batch.Stage = "failed"
			batch.Error = prepErr.Error()
		} else {
			batch.Stage = "running"
		}
		for _, taskID := range batch.TaskIDs {
			task := next.UpgradeTasks[taskID]
			if task.Stage != "preparing" {
				continue
			}
			if prepErr != nil {
				task.Stage = "failed"
				task.Error = prepErr.Error()
			} else {
				task.Stage = "pending"
				task.SHA256 = hashes[task.Asset]
			}
			task.UpdatedAt = s.now().UTC()
			next.UpgradeTasks[taskID] = task
		}
		next.UpgradeBatches[id] = batch
		return nil
	})
}

func (s *Server) listUpgrades(w http.ResponseWriter, r *http.Request) {
	state := s.store.Snapshot()
	tasks := make([]model.UpgradeTask, 0, len(state.UpgradeTasks))
	for _, task := range state.UpgradeTasks {
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.After(tasks[j].CreatedAt) })
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, tasks)
}

func (s *Server) cancelUpgrade(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		task, ok := state.UpgradeTasks[r.PathValue("id")]
		if !ok || (task.Stage != "pending" && task.Stage != "preparing") {
			return errors.New("only unclaimed tasks can be cancelled")
		}
		if task.BatchID != "" {
			batch := state.UpgradeBatches[task.BatchID]
			batch.Stage = "paused"
			batch.Error = "cancelled by administrator"
			state.UpgradeBatches[batch.ID] = batch
			for _, id := range batch.TaskIDs {
				pending := state.UpgradeTasks[id]
				if pending.Stage == "pending" || pending.Stage == "preparing" {
					pending.Stage, pending.UpdatedAt = "cancelled", s.now().UTC()
					state.UpgradeTasks[id] = pending
				}
			}
		} else {
			task.Stage, task.UpdatedAt = "cancelled", s.now().UTC()
			state.UpgradeTasks[task.ID] = task
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	http.Redirect(w, r, "/?notice="+url.QueryEscape("Upgrade cancelled"), 303)
}

type manualUpgradeRequest struct {
	ID            string `json:"id"`
	FromVersion   string `json:"from_version"`
	TargetVersion string `json:"target_version"`
	SHA256        string `json:"sha256"`
	Mode          string `json:"mode"`
	Arch          string `json:"arch"`
}

func (s *Server) startManualUpgrade(w http.ResponseWriter, r *http.Request) {
	up, ok := s.authenticateUpdater(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req manualUpgradeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !validManualTaskID(req.ID) || !releaseVersionPattern.MatchString(req.TargetVersion) || !strings.HasPrefix(req.TargetVersion, "v") || req.FromVersion == "" || req.FromVersion == req.TargetVersion || (req.Arch != "amd64" && req.Arch != "arm64") || (req.Mode != "systemd" && req.Mode != "docker") {
		http.Error(w, "invalid manual upgrade", http.StatusBadRequest)
		return
	}
	hash, err := hex.DecodeString(req.SHA256)
	if err != nil || len(hash) != 32 {
		http.Error(w, "invalid archive hash", http.StatusBadRequest)
		return
	}
	var task model.UpgradeTask
	err = s.store.Update(func(state *model.State) error {
		if existing, exists := state.UpgradeTasks[req.ID]; exists {
			if !existing.Manual || existing.NodeID != up.NodeID || existing.Role != up.Role || existing.FromVersion != req.FromVersion || existing.TargetVersion != req.TargetVersion || existing.SHA256 != strings.ToLower(req.SHA256) || existing.Mode != req.Mode || existing.Arch != req.Arch {
				return errors.New("manual task ID already used for another upgrade")
			}
			task = existing
			return nil
		}
		current, ok := state.Updaters[up.NodeID]
		if !ok || current.CredentialHash != up.CredentialHash || !nodeExists(state, up.Role, up.NodeID) || pairingInProgress(current, s.now()) {
			return errors.New("updater unavailable or pairing in progress")
		}
		if current.Mode != "" && current.Mode != req.Mode || current.Arch != "" && current.Arch != req.Arch {
			return errors.New("installation mode or architecture changed")
		}
		for _, existing := range state.UpgradeTasks {
			if existing.NodeID == up.NodeID && activeUpgrade(existing.Stage) {
				return errors.New("node already has an active upgrade")
			}
		}
		status := state.NodeStatus[up.NodeID]
		if status.NodeID != up.NodeID || status.Role != up.Role || status.BinaryVersion != req.FromVersion || status.LastSeen.IsZero() || s.now().Sub(status.LastSeen) > 45*time.Second {
			return errors.New("node has no fresh matching version status")
		}
		now := s.now().UTC()
		task = model.UpgradeTask{ID: req.ID, Manual: true, NodeID: up.NodeID, Role: up.Role, Actor: "local installer", FromVersion: req.FromVersion, TargetVersion: req.TargetVersion, Asset: fmt.Sprintf("xmesh-%s-linux-%s.tar.gz", req.TargetVersion, req.Arch), SHA256: strings.ToLower(req.SHA256), Mode: req.Mode, Arch: req.Arch, Stage: "running", Attempt: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
		if state.UpgradeTasks == nil {
			state.UpgradeTasks = map[string]model.UpgradeTask{}
		}
		state.UpgradeTasks[req.ID] = task
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func validManualTaskID(id string) bool {
	if len(id) < 12 || len(id) > 64 {
		return false
	}
	for _, char := range id {
		if char < 'a' || char > 'z' {
			if char < 'A' || char > 'Z' {
				if char < '0' || char > '9' {
					if char != '_' && char != '-' {
						return false
					}
				}
			}
		}
	}
	return true
}

func (s *Server) updaterTask(w http.ResponseWriter, r *http.Request) {
	up, ok := s.authenticateUpdater(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	available := false
	for _, task := range s.store.Snapshot().UpgradeTasks {
		if task.NodeID == up.NodeID && !task.Manual && activeUpgrade(task.Stage) && task.Stage != "preparing" {
			available = true
			break
		}
	}
	if !available {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(204)
		return
	}
	var result *model.UpgradeTask
	err := s.store.Update(func(state *model.State) error {
		if s.now().Sub(state.Updaters[up.NodeID].LastSeen) > 45*time.Second {
			return errors.New("updater heartbeat expired")
		}
		for _, task := range state.UpgradeTasks {
			if task.NodeID != up.NodeID || task.Manual || (task.Stage != "pending" && task.Stage != "running" && task.Stage != "uncertain" && task.Stage != "downloading" && task.Stage != "prepared" && task.Stage != "switching" && task.Stage != "verifying" && task.Stage != "rolling_back") {
				continue
			}
			if task.Mode != up.Mode || task.Arch != up.Arch {
				return errors.New("installation changed after task creation")
			}
			if task.Stage == "pending" {
				batch := state.UpgradeBatches[task.BatchID]
				if batch.Stage != "running" {
					continue
				}
				for _, priorID := range batch.TaskIDs {
					if priorID == task.ID {
						break
					}
					if state.UpgradeTasks[priorID].Stage != "succeeded" {
						return nil
					}
				}
				if !s.now().Before(task.ExpiresAt) {
					task.Stage = "expired"
					state.UpgradeTasks[task.ID] = task
					batch.Stage = "paused"
					batch.Error = "task expired before claim"
					state.UpgradeBatches[batch.ID] = batch
					return nil
				}
				task.Stage, task.Attempt, task.UpdatedAt = "running", task.Attempt+1, s.now().UTC()
				state.UpgradeTasks[task.ID] = task
			}
			result = &task
			return nil
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if result == nil {
		w.WriteHeader(204)
		return
	}
	writeJSON(w, 200, result)
}

type updaterReportRequest struct {
	Stage   string `json:"stage"`
	Error   string `json:"error"`
	Attempt int    `json:"attempt"`
}

func (s *Server) updaterReport(w http.ResponseWriter, r *http.Request) {
	up, ok := s.authenticateUpdater(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	var req updaterReportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	allowed := map[string]bool{"downloading": true, "prepared": true, "switching": true, "verifying": true, "uncertain": true, "rolling_back": true, "succeeded": true, "rolled_back": true, "manual_intervention": true, "failed": true}
	if !allowed[req.Stage] || len(req.Error) > 1024 {
		http.Error(w, "invalid report", 400)
		return
	}
	err := s.store.Update(func(state *model.State) error {
		task, ok := state.UpgradeTasks[r.PathValue("id")]
		if !ok || task.NodeID != up.NodeID || task.Attempt != req.Attempt {
			return errors.New("task does not belong to updater")
		}
		if task.Stage == req.Stage {
			return nil
		}
		if task.Stage != "running" && task.Stage != "uncertain" && task.Stage != "downloading" && task.Stage != "prepared" && task.Stage != "switching" && task.Stage != "verifying" && task.Stage != "rolling_back" {
			return errors.New("task is already terminal")
		}
		task.Stage, task.Error, task.UpdatedAt = req.Stage, req.Error, s.now().UTC()
		state.UpgradeTasks[task.ID] = task
		if task.BatchID != "" && (req.Stage == "rolled_back" || req.Stage == "failed" || req.Stage == "manual_intervention") {
			batch := state.UpgradeBatches[task.BatchID]
			batch.Stage = "paused"
			batch.Error = req.Stage + ": " + req.Error
			state.UpgradeBatches[batch.ID] = batch
		}
		if task.BatchID != "" && req.Stage == "succeeded" {
			batch := state.UpgradeBatches[task.BatchID]
			for _, linkID := range task.BaselineLinks {
				attachment := state.Attachments[state.Links[linkID].AttachmentID]
				peerID := attachment.GatewayID
				if peerID == task.NodeID {
					peerID = attachment.AgentID
				}
				local, peer := state.LinkStatus[task.NodeID+"/"+linkID], state.LinkStatus[peerID+"/"+linkID]
				if !local.Ready || !local.Online || !peer.Ready || !peer.Online || s.now().Sub(local.LastSeen) > 45*time.Second || s.now().Sub(peer.LastSeen) > 45*time.Second {
					batch.Stage = "paused"
					batch.Error = "previously healthy Link " + linkID + " did not recover"
					break
				}
			}
			allDone := true
			for _, id := range batch.TaskIDs {
				if state.UpgradeTasks[id].Stage != "succeeded" {
					allDone = false
					break
				}
			}
			if allDone && batch.Stage != "paused" {
				batch.Stage = "succeeded"
			}
			state.UpgradeBatches[batch.ID] = batch
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	w.WriteHeader(204)
}
