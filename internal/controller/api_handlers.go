package controller

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/identity"
	"xmesh/internal/model"
)

var errEnrollmentUpgradeActive = errors.New("node has an active upgrade; retry credential rotation after it finishes")

type StatusReport struct {
	Status model.NodeStatus    `json:"status"`
	Links  []model.LinkStatus  `json:"links"`
	Grants []model.GrantStatus `json:"grants,omitempty"`
}

func (s *Server) selfStatus(w http.ResponseWriter, r *http.Request) {
	_, nodeID, ok := s.authenticateNode(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var status model.NodeStatus
	s.store.View(func(state *model.State) { status = state.NodeStatus[nodeID] })
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var request EnrollmentRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Token == "" {
		http.Error(w, "token is required", 400)
		return
	}
	credential, err := identity.Token(32)
	if err != nil {
		http.Error(w, "credential generation failed", 500)
		return
	}
	updaterCredential := ""
	if request.WantUpdater {
		updaterCredential, err = identity.Token(32)
		if err != nil {
			http.Error(w, "credential generation failed", 500)
			return
		}
	}
	var response EnrollmentResponse
	err = s.store.Update(func(state *model.State) error {
		var enrollment model.Enrollment
		found := false
		for _, candidate := range state.Enrollments {
			if auth.EqualSecretHash(candidate.TokenHash, request.Token) {
				enrollment = candidate
				found = true
				break
			}
		}
		if !found || !enrollment.UsedAt.IsZero() || !s.now().Before(enrollment.ExpiresAt) {
			return fmt.Errorf("invalid or expired enrollment token")
		}
		if request.Role != "" && request.Role != enrollment.Role || request.NodeID != "" && request.NodeID != enrollment.NodeID {
			return fmt.Errorf("enrollment token is for a different node")
		}
		if nodeHasActiveUpgrade(state, enrollment.NodeID) {
			return errEnrollmentUpgradeActive
		}
		switch enrollment.Role {
		case model.RoleGateway:
			node, ok := state.Gateways[enrollment.NodeID]
			if !ok {
				return fmt.Errorf("gateway no longer exists")
			}
			if node.CredentialHash != "" {
				node.PreviousCredentialHash = node.CredentialHash
				node.PreviousCredentialExpiresAt = s.now().Add(15 * time.Minute).UTC()
			}
			node.CredentialHash = auth.SecretHash(credential)
			state.Gateways[node.ID] = node
		case model.RoleAgent:
			node, ok := state.Agents[enrollment.NodeID]
			if !ok {
				return fmt.Errorf("agent no longer exists")
			}
			if node.CredentialHash != "" {
				node.PreviousCredentialHash = node.CredentialHash
				node.PreviousCredentialExpiresAt = s.now().Add(15 * time.Minute).UTC()
			}
			node.CredentialHash = auth.SecretHash(credential)
			state.Agents[node.ID] = node
		default:
			return fmt.Errorf("invalid enrollment role")
		}
		enrollment.UsedAt = s.now().UTC()
		state.Enrollments[enrollment.ID] = enrollment
		if request.WantUpdater {
			if state.Updaters == nil {
				state.Updaters = map[string]model.Updater{}
			}
			state.Updaters[enrollment.NodeID] = model.Updater{NodeID: enrollment.NodeID, Role: enrollment.Role, CredentialHash: auth.SecretHash(updaterCredential)}
		}
		response = EnrollmentResponse{Role: enrollment.Role, NodeID: enrollment.NodeID, Credential: credential, ConfigURL: strings.TrimSuffix(s.cfg.PublicURL, "/") + "/api/v1/config", UpdaterCredential: updaterCredential}
		return nil
	})
	if err != nil {
		if errors.Is(err, errEnrollmentUpgradeActive) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) nodeConfig(w http.ResponseWriter, r *http.Request) {
	role, nodeID, ok := s.authenticateNode(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", 401)
		return
	}
	state := s.store.Snapshot()
	w.Header().Set("Cache-Control", "no-store")
	switch role {
	case model.RoleGateway:
		gateway := state.Gateways[nodeID]
		gateway.CredentialHash = ""
		gateway.PreviousCredentialHash = ""
		gateway.PreviousCredentialExpiresAt = time.Time{}
		response := GatewayConfig{Revision: gateway.DesiredVersion, Gateway: gateway}
		for _, attachment := range state.Attachments {
			if attachment.GatewayID != nodeID || !attachment.Enabled {
				continue
			}
			if attachment.UpstreamID != "" {
				if upstreamAvailable(&state, attachment) {
					upstream := state.Upstreams[attachment.UpstreamID]
					response.Upstreams = append(response.Upstreams, GatewayUpstreamConfig{AttachmentID: attachment.ID, UpstreamID: upstream.ID, Endpoint: upstream.Endpoint})
				}
			} else {
				for _, link := range state.Links {
					if link.AttachmentID != attachment.ID || !link.Enabled {
						continue
					}
					link.TunnelTokenHash = ""
					response.Links = append(response.Links, GatewayLinkConfig{Link: link, AgentID: attachment.AgentID, TunnelToken: auth.Derive(s.cfg.sessionKey(), "tunnel", link.ID)})
				}
			}
			for _, grant := range state.Grants {
				user := state.Users[grant.UserID]
				if grant.AttachmentID == attachment.ID && grant.Enabled && user.Enabled && routeAvailable(&state, attachment) {
					response.Grants = append(response.Grants, grant)
				}
			}
		}
		sort.Slice(response.Links, func(i, j int) bool { return response.Links[i].ID < response.Links[j].ID })
		sort.Slice(response.Grants, func(i, j int) bool { return response.Grants[i].ID < response.Grants[j].ID })
		sort.Slice(response.Upstreams, func(i, j int) bool { return response.Upstreams[i].AttachmentID < response.Upstreams[j].AttachmentID })
		writeJSON(w, 200, response)
	case model.RoleAgent:
		agent := state.Agents[nodeID]
		agent.CredentialHash = ""
		agent.PreviousCredentialHash = ""
		agent.PreviousCredentialExpiresAt = time.Time{}
		response := AgentConfig{Revision: agent.DesiredVersion, Agent: agent}
		for _, attachment := range state.Attachments {
			if attachment.AgentID != nodeID || !attachment.Enabled {
				continue
			}
			for _, link := range state.Links {
				if link.AttachmentID != attachment.ID || !link.Enabled {
					continue
				}
				link.TunnelTokenHash = ""
				gateway := state.Gateways[attachment.GatewayID]
				response.Links = append(response.Links, AgentLinkConfig{Link: link, GatewayID: attachment.GatewayID, TunnelToken: auth.Derive(s.cfg.sessionKey(), "tunnel", link.ID), RealityPublicKey: gateway.RealityPublicKey, RealityName: gateway.RealityName})
			}
			for _, grant := range state.Grants {
				user := state.Users[grant.UserID]
				if grant.AttachmentID == attachment.ID && grant.Enabled && user.Enabled {
					response.GrantIDs = append(response.GrantIDs, grant.ID)
				}
			}
		}
		sort.Slice(response.Links, func(i, j int) bool { return response.Links[i].ID < response.Links[j].ID })
		sort.Strings(response.GrantIDs)
		writeJSON(w, 200, response)
	}
}

func (s *Server) nodeStatus(w http.ResponseWriter, r *http.Request) {
	role, nodeID, ok := s.authenticateNode(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	var report StatusReport
	if !decodeJSON(w, r, &report) {
		return
	}
	if report.Status.NodeID != "" && report.Status.NodeID != nodeID {
		http.Error(w, "status node mismatch", 403)
		return
	}
	if report.Status.Role != "" && report.Status.Role != role {
		http.Error(w, "status role mismatch", 403)
		return
	}
	err := s.store.Update(func(state *model.State) error {
		now := s.now().UTC()
		report.Status.NodeID, report.Status.Role, report.Status.LastSeen = nodeID, role, now
		state.NodeStatus[nodeID] = report.Status
		if role == model.RoleGateway && !report.Status.ExternalUpstreams {
			for id, grant := range state.Grants {
				attachment := state.Attachments[grant.AttachmentID]
				if attachment.GatewayID == nodeID && attachment.UpstreamID != "" {
					grant.Published = false
					state.Grants[id] = grant
				}
			}
		}
		credential := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if role == model.RoleGateway {
			gateway := state.Gateways[nodeID]
			if auth.EqualSecretHash(gateway.CredentialHash, credential) {
				gateway.PreviousCredentialHash = ""
				gateway.PreviousCredentialExpiresAt = time.Time{}
				state.Gateways[nodeID] = gateway
			}
		} else {
			agent := state.Agents[nodeID]
			if auth.EqualSecretHash(agent.CredentialHash, credential) {
				agent.PreviousCredentialHash = ""
				agent.PreviousCredentialExpiresAt = time.Time{}
				state.Agents[nodeID] = agent
			}
		}
		allowedLinks := map[string]bool{}
		for _, attachment := range state.Attachments {
			if (role == model.RoleGateway && attachment.GatewayID == nodeID) || (role == model.RoleAgent && attachment.AgentID == nodeID) {
				if role == model.RoleGateway && attachment.UpstreamID != "" {
					allowedLinks[attachment.ID] = true
				}
				for _, link := range state.Links {
					if link.AttachmentID == attachment.ID {
						allowedLinks[link.ID] = true
					}
				}
			}
		}
		for id, link := range state.RetiredLinks {
			if now.Before(link.ExpiresAt) && (role == model.RoleGateway && link.GatewayID == nodeID || role == model.RoleAgent && link.AgentID == nodeID) {
				allowedLinks[id] = true
			}
		}
		for _, status := range report.Links {
			if !allowedLinks[status.LinkID] {
				return fmt.Errorf("link %s does not belong to node", status.LinkID)
			}
			if _, live := state.Links[status.LinkID]; !live {
				attachment, live := state.Attachments[status.LinkID]
				if !live || attachment.UpstreamID == "" {
					continue
				}
			}
			status.LastSeen = now
			status.ReporterNodeID = nodeID
			state.LinkStatus[nodeID+"/"+status.LinkID] = status
			if role == model.RoleGateway && state.Links[status.LinkID].ID != "" {
				sampleLinkHistory(state, status, now)
			}
		}
		for _, status := range report.Grants {
			grant, ok := state.Grants[status.GrantID]
			if !ok {
				retired, known := state.RetiredGrants[status.GrantID]
				if role != model.RoleGateway || !known || retired.GatewayID != nodeID || !now.Before(retired.ExpiresAt) {
					return fmt.Errorf("grant %s does not exist", status.GrantID)
				}
				if err := sampleRetiredGrantUsage(state, report.Status.InstanceID, status.GrantID, retired, status.Links, now); err != nil {
					return err
				}
				continue
			}
			attachment := state.Attachments[grant.AttachmentID]
			if role != model.RoleGateway || attachment.GatewayID != nodeID {
				return fmt.Errorf("grant %s does not belong to gateway", status.GrantID)
			}
			status.ReporterNodeID, status.LastSeen = nodeID, now
			if err := sampleGrantUsage(state, report.Status.InstanceID, status.GrantID, status.Links, now); err != nil {
				return err
			}
			state.GrantStatus[nodeID+"/"+status.GrantID] = status
		}
		pruneUsageHistory(state, now)
		pruneLinkHistory(state, now)
		if role == model.RoleGateway && report.Status.Ready && report.Status.XrayReady && report.Status.ApplyError == "" && report.Status.XrayError == "" {
			gateway := state.Gateways[nodeID]
			if report.Status.AppliedVersion >= gateway.DesiredVersion {
				for id, grant := range state.Grants {
					attachment := state.Attachments[grant.AttachmentID]
					user := state.Users[grant.UserID]
					if attachment.GatewayID == nodeID && grant.Enabled && user.Enabled && routeAvailable(state, attachment) {
						grant.Published = true
						state.Grants[id] = grant
					}
				}
			}
		}
		pruneRetiredUsage(state, now)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) authenticateNode(r *http.Request) (model.Role, string, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return "", "", false
	}
	credential := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if credential == "" || len(credential) > 256 {
		return "", "", false
	}
	var role model.Role
	var nodeID string
	now := s.now()
	candidateHash := auth.SecretHash(credential)
	matches := func(stored string) bool {
		return stored != "" && subtle.ConstantTimeCompare([]byte(stored), []byte(candidateHash)) == 1
	}
	s.store.View(func(state *model.State) {
		for id, gateway := range state.Gateways {
			if gateway.Enabled && (matches(gateway.CredentialHash) || now.Before(gateway.PreviousCredentialExpiresAt) && matches(gateway.PreviousCredentialHash)) {
				role, nodeID = model.RoleGateway, id
				return
			}
		}
		for id, agent := range state.Agents {
			if agent.Enabled && (matches(agent.CredentialHash) || now.Before(agent.PreviousCredentialExpiresAt) && matches(agent.PreviousCredentialHash)) {
				role, nodeID = model.RoleAgent, id
				return
			}
		}
	})
	return role, nodeID, nodeID != ""
}

var _ = time.Second
