package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"xmesh/internal/model"
)

const upgradeLinkRecoveryTimeout = 2 * time.Minute
const upgradeLinkRecoveryError = "previously healthy Link "

// A successful process switch does not imply that both Link reports have caught
// up. Keep the next task unclaimed until fresh reports confirm recovery.
func upgradeRecoveryProblem(state *model.State, task model.UpgradeTask, now time.Time) string {
	for _, linkID := range task.BaselineLinks {
		link, ok := state.Links[linkID]
		attachment, attached := state.Attachments[link.AttachmentID]
		if !ok || !link.Enabled || !attached || !attachment.Enabled || (attachment.GatewayID != task.NodeID && attachment.AgentID != task.NodeID) {
			return fmt.Sprintf("%s%s did not recover: Link configuration is unavailable", upgradeLinkRecoveryError, linkID)
		}
		for _, endpoint := range []struct {
			role model.Role
			id   string
		}{{model.RoleGateway, attachment.GatewayID}, {model.RoleAgent, attachment.AgentID}} {
			status, reported := state.LinkStatus[endpoint.id+"/"+linkID]
			problem := ""
			switch {
			case !reported:
				problem = "has not reported"
			case status.LastSeen.IsZero() || now.Sub(status.LastSeen) > 45*time.Second:
				problem = "report is older than 45 seconds"
			case status.LastSeen.Before(task.UpdatedAt):
				problem = "has not reported since upgrade completion"
			case !status.Online:
				problem = "is offline"
			case !status.Ready:
				problem = "is not ready"
			}
			if problem != "" {
				if status.LastError != "" {
					problem += " (" + status.LastError + ")"
				}
				return fmt.Sprintf("%s%s did not recover: %s %s %s", upgradeLinkRecoveryError, linkID, endpoint.role, endpoint.id, problem)
			}
		}
	}
	return ""
}

func finishUpgradeRecovery(state *model.State, batch model.UpgradeBatch) {
	batch.Stage, batch.Error = "running", ""
	batch.RecoveryTaskID, batch.RecoveryDeadline = "", time.Time{}
	allDone := len(batch.TaskIDs) > 0
	for _, id := range batch.TaskIDs {
		if state.UpgradeTasks[id].Stage != "succeeded" {
			allDone = false
			break
		}
	}
	if allDone {
		batch.Stage = "succeeded"
	}
	state.UpgradeBatches[batch.ID] = batch
}

// Called by node status updates, helper heartbeats and task polls, so the deadline
// survive Controller restarts without a detached timer or a new updater protocol.
func reconcileUpgradeRecovery(state *model.State, now time.Time) {
	for _, batch := range state.UpgradeBatches {
		if batch.Stage != "recovering" {
			continue
		}
		task, ok := state.UpgradeTasks[batch.RecoveryTaskID]
		if !ok || task.BatchID != batch.ID || task.Stage != "succeeded" {
			batch.Stage, batch.Error = "paused", "upgrade recovery task is unavailable"
			state.UpgradeBatches[batch.ID] = batch
			continue
		}
		problem := upgradeRecoveryProblem(state, task, now)
		if problem == "" {
			finishUpgradeRecovery(state, batch)
			continue
		}
		batch.Error = problem
		if !now.Before(batch.RecoveryDeadline) {
			batch.Stage = "paused"
		} else {
			batch.Error = "waiting for Link recovery: " + problem
		}
		state.UpgradeBatches[batch.ID] = batch
	}
}

// The prefix also recognises paused batches persisted by older Controllers.
// Failure, rollback, expiry and administrator cancellation cannot be resumed.
func upgradeResumeTaskID(state *model.State, batch model.UpgradeBatch) string {
	if batch.Stage != "paused" || !strings.HasPrefix(batch.Error, upgradeLinkRecoveryError) {
		return ""
	}
	lastCompleted := ""
	pending := false
	for _, id := range batch.TaskIDs {
		task, ok := state.UpgradeTasks[id]
		if !ok || task.BatchID != batch.ID {
			return ""
		}
		switch task.Stage {
		case "succeeded":
			if pending {
				return ""
			}
			lastCompleted = id
		case "pending":
			pending = true
		default:
			return ""
		}
	}
	return lastCompleted
}

func canResumeUpgrade(task model.UpgradeTask, state model.State) bool {
	return task.ID != "" && task.ID == upgradeResumeTaskID(&state, state.UpgradeBatches[task.BatchID])
}

func (s *Server) resumeUpgrade(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		task, ok := state.UpgradeTasks[r.PathValue("id")]
		batch := state.UpgradeBatches[task.BatchID]
		if !ok || upgradeResumeTaskID(state, batch) == "" {
			return errors.New("only batches paused for Link recovery can be resumed")
		}
		now := s.now().UTC()
		for _, id := range batch.TaskIDs {
			completed := state.UpgradeTasks[id]
			if completed.Stage == "pending" {
				if !now.Before(completed.ExpiresAt) {
					return errors.New("pending task expired; create a new upgrade batch")
				}
				continue
			}
			if problem := upgradeRecoveryProblem(state, completed, now); problem != "" {
				return errors.New(problem)
			}
		}
		finishUpgradeRecovery(state, batch)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/?notice="+url.QueryEscape("Link recovery confirmed; upgrade batch resumed"), http.StatusSeeOther)
}
