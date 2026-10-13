package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"time"

	"xmesh/internal/model"
)

func currentUpstreamProbe(state *model.State, attachment model.Attachment) *model.UpstreamProbeStatus {
	probe := attachment.UpstreamProbe
	if probe == nil || probe.Revision != state.Gateways[attachment.GatewayID].DesiredVersion ||
		probe.RequestID != attachment.ProbeRequestID {
		return nil
	}
	return probe
}

func upstreamProbeFreshness(status model.NodeStatus) time.Duration {
	interval := time.Duration(status.UpstreamProbeIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return interval*2 + 35*time.Second
}

func (s *Server) requestUpstreamProbe(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	requestID, err := newID("probe")
	if err != nil {
		http.Error(w, "could not create test request", 500)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		attachment, exists := state.Attachments[id]
		if !exists || attachment.UpstreamID == "" {
			return errors.New("external route not found")
		}
		if !attachment.Enabled || !state.Gateways[attachment.GatewayID].Enabled || !upstreamAvailable(state, attachment) {
			return errors.New("external route is disabled or has no selected node")
		}
		status := state.NodeStatus[attachment.GatewayID]
		if !status.UpstreamProbes {
			return errors.New("upgrade this Gateway to support upstream tests")
		}
		if !status.Online || status.LastSeen.IsZero() || s.now().Sub(status.LastSeen) > time.Duration(s.cfg.NodeOfflineAfterSeconds)*time.Second {
			return errors.New("Gateway is offline")
		}
		if probe := currentUpstreamProbe(state, attachment); probe != nil && probe.CompletedAt.IsZero() {
			return nil
		}
		if attachment.ProbeRequestID != "" && currentUpstreamProbe(state, attachment) == nil &&
			s.now().Sub(attachment.ProbeRequestedAt) < time.Minute {
			return nil
		}
		attachment.ProbeRequestID, attachment.ProbeRequestedAt = requestID, s.now().UTC()
		state.Attachments[id] = attachment
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/#upstreams", http.StatusSeeOther)
}

func (s *Server) acceptUpstreamProbes(state *model.State, role model.Role, nodeID string, report StatusReport, now time.Time) error {
	if len(report.UpstreamProbes) == 0 {
		return nil
	}
	if role != model.RoleGateway || !report.Status.UpstreamProbes {
		return errors.New("only capable Gateways may report upstream tests")
	}
	seen := map[string]bool{}
	for _, result := range report.UpstreamProbes {
		attachment, exists := state.Attachments[result.AttachmentID]
		if !exists {
			continue
		}
		if attachment.GatewayID != nodeID || attachment.UpstreamID == "" {
			return fmt.Errorf("upstream test %s does not belong to Gateway", result.AttachmentID)
		}
		if seen[result.AttachmentID] {
			return errors.New("duplicate upstream test result")
		}
		seen[result.AttachmentID] = true
		// Ignore in-flight results for a replaced endpoint or a newer request.
		if result.Revision != state.Gateways[nodeID].DesiredVersion || result.RequestID != attachment.ProbeRequestID ||
			report.Status.AppliedVersion != result.Revision || !report.Status.XrayReady ||
			!attachment.Enabled || !state.Gateways[nodeID].Enabled || !upstreamAvailable(state, attachment) {
			continue
		}
		if err := validateUpstreamProbe(result); err != nil {
			return err
		}
		if result.StartedAt.After(now.Add(time.Minute)) {
			return errors.New("upstream test time is in the future")
		}
		if previous := currentUpstreamProbe(state, attachment); previous != nil {
			if result.StartedAt.Equal(previous.StartedAt) && result.CompletedAt.Equal(previous.CompletedAt) {
				continue
			}
			if result.StartedAt.Before(previous.StartedAt) ||
				(result.StartedAt.Equal(previous.StartedAt) && !previous.CompletedAt.IsZero() && result.CompletedAt.IsZero()) {
				continue
			}
		}
		attachment.UpstreamProbe = &result
		state.Attachments[attachment.ID] = attachment
		if !result.CompletedAt.IsZero() {
			sampleLinkHistory(state, model.LinkStatus{LinkID: "external:" + attachment.ID, ReporterNodeID: nodeID, Generation: result.Revision, Online: true, Ready: result.Successful(), RTTMillis: float64(result.Targets[0].DurationMillis)}, now)
		}
	}
	return nil
}

func validateUpstreamProbe(result model.UpstreamProbeStatus) error {
	if result.StartedAt.IsZero() {
		return errors.New("upstream test start time is missing")
	}
	if result.CompletedAt.IsZero() {
		if len(result.Targets) != 0 {
			return errors.New("unfinished upstream test has results")
		}
		return nil
	}
	if result.CompletedAt.Before(result.StartedAt) || len(result.Targets) != 2 {
		return errors.New("invalid upstream test completion")
	}
	expected := []string{model.UpstreamIPProbeURL, model.UpstreamMicrosoftProbeURL}
	for i, target := range result.Targets {
		if target.URL != expected[i] || len(target.Error) > 512 || target.DurationMillis < 0 || target.DurationMillis > 35000 ||
			target.HTTPStatus < 0 || target.HTTPStatus > 599 {
			return errors.New("invalid upstream test target")
		}
		if target.Success && (target.HTTPStatus < 200 || target.HTTPStatus >= 400 || target.Error != "") {
			return errors.New("inconsistent upstream test success")
		}
		if i == 0 && target.Success {
			ip, err := netip.ParseAddr(target.ExitIP)
			if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
				return errors.New("invalid upstream exit IP")
			}
		} else if target.ExitIP != "" {
			return errors.New("unexpected upstream exit IP")
		}
	}
	return nil
}
