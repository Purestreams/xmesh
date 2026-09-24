package controller

import (
	"net/http"
	"strings"
	"time"

	"xmesh/internal/model"
)

const historyInterval = 5 * time.Minute
const historyRetention = 24 * time.Hour
const historyLimit = 289

func sampleLinkHistory(state *model.State, status model.LinkStatus, now time.Time) {
	if state.LinkHistory == nil {
		state.LinkHistory = map[string][]model.LinkSample{}
	}
	samples := state.LinkHistory[status.LinkID]
	if len(samples) > 0 && now.Sub(samples[len(samples)-1].At) < historyInterval {
		return
	}
	samples = append(samples, model.LinkSample{At: now, Generation: status.Generation, InstanceID: state.NodeStatus[status.ReporterNodeID].InstanceID, UploadBytes: status.UploadBytes, DownloadBytes: status.DownloadBytes, RTTMillis: status.RTTMillis, Ready: status.Online && status.Ready})
	if len(samples) > historyLimit {
		samples = samples[len(samples)-historyLimit:]
	}
	state.LinkHistory[status.LinkID] = samples
}

func pruneLinkHistory(state *model.State, now time.Time) {
	for id, samples := range state.LinkHistory {
		if _, exists := state.Links[id]; !exists {
			delete(state.LinkHistory, id)
			continue
		}
		first := 0
		for first < len(samples) && samples[first].At.Before(now.Add(-historyRetention)) {
			first++
		}
		state.LinkHistory[id] = samples[first:]
	}
}

type dashboardNode struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Role     string           `json:"role"`
	Host     string           `json:"host"`
	Region   string           `json:"region"`
	Target   string           `json:"target"`
	Enabled  bool             `json:"enabled"`
	Enrolled bool             `json:"enrolled"`
	Desired  uint64           `json:"desired"`
	Status   model.NodeStatus `json:"status"`
	Next     string           `json:"next"`
}
type dashboardRoute struct {
	ID         string `json:"id"`
	GatewayID  string `json:"gateway_id"`
	AgentID    string `json:"agent_id"`
	UpstreamID string `json:"upstream_id,omitempty"`
	Enabled    bool   `json:"enabled"`
	Selectable bool   `json:"selectable"`
	State      string `json:"state"`
}
type dashboardLink struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	RouteID string           `json:"route_id"`
	Enabled bool             `json:"enabled"`
	Status  model.LinkStatus `json:"status"`
}
type dashboardGrant struct {
	ID        string            `json:"id"`
	UserID    string            `json:"user_id"`
	RouteID   string            `json:"route_id"`
	Enabled   bool              `json:"enabled"`
	Published bool              `json:"published"`
	Status    model.GrantStatus `json:"status"`
}
type dashboardUser struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Publication string `json:"publication"`
}
type dashboardData struct {
	At             time.Time                     `json:"at"`
	Revision       uint64                        `json:"revision"`
	Nodes          []dashboardNode               `json:"nodes"`
	Routes         []dashboardRoute              `json:"routes"`
	Links          []dashboardLink               `json:"links"`
	Users          []dashboardUser               `json:"users"`
	Grants         []dashboardGrant              `json:"grants"`
	History        map[string][]model.LinkSample `json:"history"`
	Usage          []usageUserView               `json:"usage"`
	Operations     []model.Operation             `json:"operations"`
	Upgrade        controllerUpgradeView         `json:"upgrade"`
	UpgradeTasks   []model.UpgradeTask           `json:"upgrade_tasks"`
	Updaters       map[string]updaterView        `json:"updaters"`
	UpgradeBatches map[string]model.UpgradeBatch `json:"upgrade_batches"`
}

// panelState is shared by HTML and JSON, including heartbeat expiry semantics.
func (s *Server) panelState() model.State {
	state := s.store.Snapshot()
	for id, status := range state.NodeStatus {
		if s.now().Sub(status.LastSeen) > time.Duration(s.cfg.NodeOfflineAfterSeconds)*time.Second {
			status.Online, status.Ready = false, false
			state.NodeStatus[id] = status
		}
	}
	for id, status := range state.LinkStatus {
		if s.now().Sub(status.LastSeen) > time.Duration(s.cfg.NodeOfflineAfterSeconds)*time.Second {
			status.Online, status.Ready = false, false
			state.LinkStatus[id] = status
		}
	}
	return state
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	state := s.panelState()
	data := dashboardData{At: s.now().UTC(), Revision: state.Revision, Nodes: []dashboardNode{}, Routes: []dashboardRoute{}, Links: []dashboardLink{}, Users: []dashboardUser{}, Grants: []dashboardGrant{}, History: map[string][]model.LinkSample{}, Usage: usageViews(state, s.now()), Operations: state.Operations, Upgrade: s.controllerUpgradeStatus(), UpgradeTasks: sortedUpgradeTasks(state), Updaters: updaterViews(state, s.now()), UpgradeBatches: state.UpgradeBatches}
	for _, node := range sortedGateways(state) {
		data.Nodes = append(data.Nodes, dashboardNode{ID: node.ID, Name: node.Name, Role: "Gateway", Host: node.PublicHost, Region: node.Region, Target: node.RealityTarget, Enabled: node.Enabled, Enrolled: node.CredentialHash != "", Desired: node.DesiredVersion, Status: state.NodeStatus[node.ID], Next: nextDeploymentAction(state, node.ID, model.RoleGateway)})
	}
	for _, node := range sortedAgents(state) {
		data.Nodes = append(data.Nodes, dashboardNode{ID: node.ID, Name: node.Name, Role: "Agent", Enabled: node.Enabled, Enrolled: node.CredentialHash != "", Desired: node.DesiredVersion, Status: state.NodeStatus[node.ID], Next: nextDeploymentAction(state, node.ID, model.RoleAgent)})
	}
	for _, upstream := range sortedUpstreams(state) {
		data.Nodes = append(data.Nodes, dashboardNode{ID: upstream.ID, Name: upstream.Name, Role: "External exit", Host: upstream.Endpoint.Address, Enabled: upstream.Enabled, Status: model.NodeStatus{Online: upstream.Endpoint.UUID != ""}, Next: upstream.LastError})
	}
	for _, route := range sortedAttachments(state) {
		if route.UpstreamID != "" {
			upstream := state.Upstreams[route.UpstreamID]
			gateway := state.Gateways[route.GatewayID]
			status := state.NodeStatus[route.GatewayID]
			stage := "pending"
			switch {
			case !route.Enabled || !gateway.Enabled || !upstream.Enabled:
				stage = "disabled"
			case !status.Online:
				stage = "offline"
			case !status.ExternalUpstreams:
				stage = "pending"
			case upstream.Endpoint.UUID != "" && status.AppliedVersion >= gateway.DesiredVersion && status.Ready && status.XrayReady && status.XrayError == "":
				stage = "configured"
			}
			data.Routes = append(data.Routes, dashboardRoute{ID: route.ID, GatewayID: route.GatewayID, UpstreamID: route.UpstreamID, Enabled: route.Enabled && upstream.Enabled && gateway.Enabled, Selectable: routeAvailable(&state, route), State: stage})
			continue
		}
		enabled := route.Enabled && state.Gateways[route.GatewayID].Enabled && state.Agents[route.AgentID].Enabled
		selectable := enabled && attachmentHasEnabledLink(&state, route.ID)
		stage := "pending"
		if !enabled {
			stage = "disabled"
		} else {
			gateway, agent := state.NodeStatus[route.GatewayID], state.NodeStatus[route.AgentID]
			applied := gateway.AppliedVersion >= state.Gateways[route.GatewayID].DesiredVersion && agent.AppliedVersion >= state.Agents[route.AgentID].DesiredVersion && gateway.ApplyError == "" && agent.ApplyError == "" && gateway.XrayError == ""
			if !gateway.Online || !agent.Online {
				stage = "offline"
			} else if applied && gateway.Ready && gateway.XrayReady && agent.Ready {
				for _, link := range state.Links {
					if link.AttachmentID != route.ID || !link.Enabled {
						continue
					}
					gs, as := state.LinkStatus[route.GatewayID+"/"+link.ID], state.LinkStatus[route.AgentID+"/"+link.ID]
					if gs.Online && gs.Ready && as.Online && as.Ready {
						stage = "ready"
						break
					}
				}
			}
		}
		data.Routes = append(data.Routes, dashboardRoute{ID: route.ID, GatewayID: route.GatewayID, AgentID: route.AgentID, Enabled: enabled, Selectable: selectable, State: stage})
	}
	summaries := summarizeLinks(state)
	for _, link := range sortedLinks(state) {
		data.Links = append(data.Links, dashboardLink{ID: link.ID, Name: link.Name, RouteID: link.AttachmentID, Enabled: link.Enabled, Status: summaries[link.ID]})
		for _, sample := range state.LinkHistory[link.ID] {
			if !sample.At.Before(s.now().Add(-historyRetention)) {
				data.History[link.ID] = append(data.History[link.ID], sample)
			}
		}
	}
	counts := subscriptionCounts(state)
	for _, user := range sortedUsers(state) {
		data.Users = append(data.Users, dashboardUser{ID: user.ID, Name: user.Name, Enabled: user.Enabled, Publication: counts[user.ID]})
	}
	grantSummaries := summarizeGrants(state)
	for _, grant := range sortedGrants(state) {
		data.Grants = append(data.Grants, dashboardGrant{ID: grant.ID, UserID: grant.UserID, RouteID: grant.AttachmentID, Enabled: grant.Enabled, Published: grant.Published, Status: grantSummaries[grant.ID]})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, data)
}

type operationWriter struct {
	http.ResponseWriter
	status int
}

func (w *operationWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *operationWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (s *Server) recordOperation(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	recorder := &operationWriter{ResponseWriter: w}
	next(recorder, r)
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	// A registered route pattern cannot contain a token or untrusted request text.
	action := strings.TrimPrefix(r.Pattern, "POST /admin/")
	if action == "" {
		return
	}
	err := s.store.Update(func(state *model.State) error {
		state.Operations = append(state.Operations, model.Operation{At: s.now().UTC(), Actor: s.cfg.AdminUsername, Action: action, Status: recorder.status})
		if len(state.Operations) > 100 {
			state.Operations = state.Operations[len(state.Operations)-100:]
		}
		return nil
	})
	if err != nil {
		s.logger.Error("persist operation history", "error", err)
	}
}
