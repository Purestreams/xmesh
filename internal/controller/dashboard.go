package controller

import (
	"net/http"
	"strings"
	"time"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
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
		_, exists := state.Links[id]
		if strings.HasPrefix(id, "external:") {
			attachment, ok := state.Attachments[strings.TrimPrefix(id, "external:")]
			exists = ok && attachment.UpstreamID != ""
		}
		if !exists {
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
	PublicIPSupported       bool             `json:"public_ip_supported"`
	IPv6IngressSupported    bool             `json:"ipv6_ingress_supported"`
	IPv6IngressEnabled      bool             `json:"ipv6_ingress_enabled,omitempty"`
	DetectedPublicIPv6      string           `json:"detected_public_ipv6,omitempty"`
	PublicIPv6Sources       string           `json:"public_ipv6_sources,omitempty"`
	VMessPort               int              `json:"vmess_port,omitempty"`
	VMessPath               string           `json:"vmess_path,omitempty"`
	PublicHostAuto          bool             `json:"public_host_auto,omitempty"`
	DetectedPublicIP        string           `json:"detected_public_ip,omitempty"`
	PublicIPSources         string           `json:"public_ip_sources,omitempty"`
	ObservedPublicIP        string           `json:"observed_public_ip,omitempty"`
	PublicIPCheckedAt       time.Time        `json:"public_ip_checked_at,omitempty"`
	HY2Port                 int              `json:"hy2_port,omitempty"`
	RealityPort             int              `json:"reality_port,omitempty"`
	PreviousRealityLinkPort int              `json:"previous_reality_link_port,omitempty"`
	VMessDisabled           bool             `json:"vmess_disabled,omitempty"`
	HY2Disabled             bool             `json:"hy2_disabled,omitempty"`
	ID                      string           `json:"id"`
	GatewayID               string           `json:"gateway_id,omitempty"`
	Impact                  deletionImpact   `json:"impact"`
	Name                    string           `json:"name"`
	Role                    string           `json:"role"`
	Host                    string           `json:"host"`
	Region                  string           `json:"region"`
	Target                  string           `json:"target"`
	Enabled                 bool             `json:"enabled"`
	ConfiguredEnabled       bool             `json:"configured_enabled"`
	AgentEnabled            bool             `json:"agent_enabled"`
	Enrolled                bool             `json:"enrolled"`
	Desired                 uint64           `json:"desired"`
	Status                  model.NodeStatus `json:"status"`
	Next                    string           `json:"next"`
}
type dashboardRoute struct {
	DisplayName          string                     `json:"display_name"`
	Name                 string                     `json:"name,omitempty"`
	Issue                accessView                 `json:"issue"`
	Probe                *model.UpstreamProbeStatus `json:"probe,omitempty"`
	ProbeSupported       bool                       `json:"probe_supported,omitempty"`
	CanProbe             bool                       `json:"can_probe,omitempty"`
	ProbeIntervalSeconds int64                      `json:"probe_interval_seconds,omitempty"`
	ID                   string                     `json:"id"`
	GatewayID            string                     `json:"gateway_id"`
	AgentID              string                     `json:"agent_id"`
	UpstreamID           string                     `json:"upstream_id,omitempty"`
	Enabled              bool                       `json:"enabled"`
	AssignmentEnabled    bool                       `json:"assignment_enabled"`
	Selectable           bool                       `json:"selectable"`
	State                string                     `json:"state"`
}
type dashboardLink struct {
	URL     string           `json:"url"`
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	RouteID string           `json:"route_id"`
	Enabled bool             `json:"enabled"`
	Status  model.LinkStatus `json:"status"`
}
type dashboardGrant struct {
	Importable   bool              `json:"importable"`
	Access       accessView        `json:"access"`
	HY2Available bool              `json:"hy2_available"`
	ID           string            `json:"id"`
	UserID       string            `json:"user_id"`
	RouteID      string            `json:"route_id"`
	Enabled      bool              `json:"enabled"`
	Published    bool              `json:"published"`
	Status       model.GrantStatus `json:"status"`
}
type dashboardUser struct {
	AuditEnabled        bool   `json:"audit_enabled"`
	AuditLimitMB        int    `json:"audit_limit_mb"`
	Notes               string `json:"notes,omitempty"`
	Archived            bool   `json:"archived"`
	SubscriptionVersion string `json:"subscription_version"`
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Enabled             bool   `json:"enabled"`
	Publication         string `json:"publication"`
}
type dashboardData struct {
	ManagementRevision string                        `json:"management_revision"`
	LinkReports        []model.LinkStatus            `json:"link_reports"`
	Enrollments        []enrollmentStatus            `json:"enrollments"`
	At                 time.Time                     `json:"at"`
	Revision           uint64                        `json:"revision"`
	Nodes              []dashboardNode               `json:"nodes"`
	Routes             []dashboardRoute              `json:"routes"`
	Links              []dashboardLink               `json:"links"`
	Users              []dashboardUser               `json:"users"`
	Grants             []dashboardGrant              `json:"grants"`
	History            map[string][]model.LinkSample `json:"history"`
	Usage              []usageUserView               `json:"usage"`
	Operations         []model.Operation             `json:"operations"`
	Upgrade            controllerUpgradeView         `json:"upgrade"`
	UpgradeTasks       []model.UpgradeTask           `json:"upgrade_tasks"`
	Updaters           map[string]updaterView        `json:"updaters"`
	UpgradeBatches     map[string]model.UpgradeBatch `json:"upgrade_batches"`
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
	impacts := nodeImpacts(state)
	data := dashboardData{Enrollments: enrollmentStatuses(state, s.now()), At: s.now().UTC(), Revision: state.Revision, Nodes: []dashboardNode{}, Routes: []dashboardRoute{}, Links: []dashboardLink{}, Users: []dashboardUser{}, Grants: []dashboardGrant{}, History: map[string][]model.LinkSample{}, Usage: usageViews(state, s.now()), Operations: state.Operations, Upgrade: s.controllerUpgradeStatus(), UpgradeTasks: sortedUpgradeTasks(state), Updaters: updaterViews(state, s.now()), UpgradeBatches: state.UpgradeBatches}
	data.ManagementRevision = managementRevision(state)
	data.LinkReports = sortedLinkReports(state)
	for _, node := range sortedGateways(state) {
		data.Nodes = append(data.Nodes, dashboardNode{PublicIPSupported: node.CredentialHash == "" || gatewayAutoIPSupported(state.NodeStatus[node.ID].BinaryVersion), IPv6IngressSupported: node.CredentialHash == "" || gatewayIPv6Supported(state.NodeStatus[node.ID].BinaryVersion), ID: node.ID, Impact: impacts[node.ID], Name: node.Name, Role: "Gateway", VMessPort: node.VMessPort, VMessPath: node.VMessPath, HY2Port: node.HY2Port, RealityPort: node.RealityPort, PreviousRealityLinkPort: node.PreviousRealityLinkPort, VMessDisabled: node.VMessDisabled, HY2Disabled: node.HY2Disabled, Host: node.PublicHost, PublicHostAuto: node.PublicHostAuto, IPv6IngressEnabled: node.IPv6IngressEnabled, DetectedPublicIPv6: node.DetectedPublicIPv6, PublicIPv6Sources: node.PublicIPv6Sources, DetectedPublicIP: node.DetectedPublicIP, PublicIPSources: node.PublicIPSources, PublicIPCheckedAt: node.PublicIPCheckedAt, ObservedPublicIP: node.ObservedPublicIP, Region: node.Region, Target: node.RealityTarget, Enabled: node.Enabled, ConfiguredEnabled: node.Enabled, AgentEnabled: node.AgentEnabled, Enrolled: node.CredentialHash != "", Desired: node.DesiredVersion, Status: state.NodeStatus[node.ID], Next: nextDeploymentAction(state, node.ID, model.RoleGateway)})
	}
	for _, node := range sortedAgents(state) {
		enabled, enrolled := agentEnabled(&state, node), node.CredentialHash != ""
		if node.GatewayID != "" {
			gateway := state.Gateways[node.GatewayID]
			enrolled = gateway.CredentialHash != ""
		}
		data.Nodes = append(data.Nodes, dashboardNode{ID: node.ID, GatewayID: node.GatewayID, Impact: impacts[node.ID], Name: node.Name, Role: "Agent", Enabled: enabled, ConfiguredEnabled: node.Enabled, Enrolled: enrolled, Desired: node.DesiredVersion, Status: state.NodeStatus[node.ID], Next: nextDeploymentAction(state, node.ID, model.RoleAgent)})
	}
	for _, upstream := range sortedUpstreams(state) {
		data.Nodes = append(data.Nodes, dashboardNode{ID: upstream.ID, Name: upstream.Name, Role: "External exit", Host: upstream.Endpoint.Address, Enabled: upstream.Enabled, ConfiguredEnabled: upstream.Enabled, Status: model.NodeStatus{Online: upstream.Endpoint.UUID != ""}, Next: upstream.LastError})
	}
	for _, route := range sortedAttachments(state) {
		issue := routeIssue(state, route, s.now())
		if route.UpstreamID != "" {
			upstream := state.Upstreams[route.UpstreamID]
			gateway := state.Gateways[route.GatewayID]
			status := state.NodeStatus[route.GatewayID]
			data.Routes = append(data.Routes, dashboardRoute{DisplayName: routeDisplayName(state, route), Name: route.Name, Issue: issue, ID: route.ID, GatewayID: route.GatewayID, UpstreamID: route.UpstreamID, Enabled: route.Enabled && upstream.Enabled && gateway.Enabled, AssignmentEnabled: route.Enabled, Selectable: routeAvailable(&state, route), State: issue.State, Probe: currentUpstreamProbe(&state, route), ProbeSupported: status.UpstreamProbes, ProbeIntervalSeconds: status.UpstreamProbeIntervalSeconds, CanProbe: status.UpstreamProbes && status.Online && route.Enabled && gateway.Enabled && upstreamAvailable(&state, route)})
			continue
		}
		enabled := route.Enabled && state.Gateways[route.GatewayID].Enabled && agentEnabled(&state, state.Agents[route.AgentID])
		data.Routes = append(data.Routes, dashboardRoute{DisplayName: routeDisplayName(state, route), Name: route.Name, Issue: issue, ID: route.ID, GatewayID: route.GatewayID, AgentID: route.AgentID, Enabled: enabled, AssignmentEnabled: route.Enabled, Selectable: routeAvailable(&state, route), State: issue.State})
	}
	summaries := summarizeLinks(state)
	for _, link := range sortedLinks(state) {
		data.Links = append(data.Links, dashboardLink{ID: link.ID, URL: link.URL, Name: link.Name, RouteID: link.AttachmentID, Enabled: link.Enabled, Status: summaries[link.ID]})
		for _, sample := range state.LinkHistory[link.ID] {
			if !sample.At.Before(s.now().Add(-historyRetention)) {
				data.History[link.ID] = append(data.History[link.ID], sample)
			}
		}
	}
	counts := subscriptionCounts(state, s.now())
	for _, user := range sortedUsers(state) {
		data.Users = append(data.Users, dashboardUser{AuditEnabled: user.AuditEnabled, AuditLimitMB: user.AuditLimitMB, Notes: user.Notes, Archived: user.Archived, ID: user.ID, Name: user.Name, Enabled: user.Enabled, Publication: counts[user.ID], SubscriptionVersion: auth.Derive(s.cfg.sessionKey(), "subscription-version", user.ID+"/"+user.SubscriptionTokenHash)})
	}
	grantSummaries := summarizeGrants(state)
	for _, grant := range sortedGrants(state) {
		data.Grants = append(data.Grants, dashboardGrant{Importable: grantImportable(state, grant, s.now()), Access: grantAccess(state, grant, s.now()), ID: grant.ID, UserID: grant.UserID, RouteID: grant.AttachmentID, Enabled: grant.Enabled, Published: grant.Published, HY2Available: grantHY2Available(state, grant, s.now()), Status: grantSummaries[grant.ID]})
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
