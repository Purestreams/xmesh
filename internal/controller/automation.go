package controller

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"xmesh/internal/auth"
	"xmesh/internal/identity"
	"xmesh/internal/model"
)

// assignGateways creates missing Agent/Gateway routes in one state transaction.
// Existing routes are never silently re-enabled or modified.
func (s *Server) assignGateways(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	agentID := r.PathValue("id")
	if agentID == "" {
		agentID = r.FormValue("agent_id")
	}
	selected := r.Form["gateway_id"]
	if len(selected) == 0 {
		http.Error(w, "select at least one Gateway", http.StatusBadRequest)
		return
	}
	target := strings.TrimSpace(r.FormValue("reality_target"))
	seen := map[string]bool{}
	err := s.store.Update(func(state *model.State) error {
		agent, ok := state.Agents[agentID]
		if !ok || !agent.Enabled {
			return fmt.Errorf("Agent unavailable")
		}
		for _, gatewayID := range selected {
			if seen[gatewayID] {
				continue
			}
			seen[gatewayID] = true
			gateway, ok := state.Gateways[gatewayID]
			if !ok || !gateway.Enabled || gateway.PublicHost == "" {
				return fmt.Errorf("Gateway %s unavailable", gatewayID)
			}
			attachmentID := ""
			for _, attachment := range state.Attachments {
				if attachment.AgentID == agentID && attachment.GatewayID == gatewayID {
					if !attachment.Enabled {
						return fmt.Errorf("Gateway %s assignment is disabled; enable it explicitly", gateway.Name)
					}
					attachmentID = attachment.ID
					break
				}
			}
			if attachmentID != "" {
				hasExistingLink := false
				for _, link := range state.Links {
					if link.AttachmentID == attachmentID {
						hasExistingLink = true
						break
					}
				}
				if hasExistingLink {
					continue
				}
			} else {
				var err error
				attachmentID, err = newID("node")
				if err != nil {
					return err
				}
				state.Attachments[attachmentID] = model.Attachment{ID: attachmentID, AgentID: agentID, GatewayID: gatewayID, Enabled: true, CreatedAt: s.now().UTC()}
			}
			linkID, err := newID("lnk")
			if err != nil {
				return err
			}
			linkURL := "reality://" + net.JoinHostPort(gateway.PublicHost, "8443") + "/tunnel"
			linkTarget := target
			if gateway.RealityTarget != "" {
				linkTarget = gateway.RealityTarget
			}
			uuid, shortID, err := provisionReality(state, gatewayID, linkTarget, linkURL)
			if err != nil {
				return fmt.Errorf("Gateway %s: %w", gateway.Name, err)
			}
			state.Links[linkID] = model.Link{ID: linkID, AttachmentID: attachmentID, Name: gateway.Name + " / " + agent.Name, URL: linkURL, TLSVerify: true, RealityUUID: uuid, RealityShortID: shortID, Priority: 10, Weight: 1, Connections: 2, MaxStreams: 256, Enabled: true, TunnelTokenHash: auth.SecretHash(auth.Derive(s.cfg.sessionKey(), "tunnel", linkID)), CreatedAt: s.now().UTC()}
			bumpGateway(state, gatewayID)
			bumpAgent(state, agentID)
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// openSubscription grants only the routes selected by the administrator.
func (s *Server) openSubscription(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	userID := r.PathValue("id")
	if userID == "" {
		userID = r.FormValue("user_id")
	}
	selected := r.Form["attachment_id"]
	if len(selected) == 0 {
		http.Error(w, "select at least one route", http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	err := s.store.Update(func(state *model.State) error {
		user, ok := state.Users[userID]
		if !ok || !user.Enabled {
			return fmt.Errorf("user unavailable")
		}
		for _, attachmentID := range selected {
			if seen[attachmentID] {
				continue
			}
			seen[attachmentID] = true
			attachment, ok := state.Attachments[attachmentID]
			if !ok || !attachment.Enabled || !state.Gateways[attachment.GatewayID].Enabled || !state.Agents[attachment.AgentID].Enabled {
				return fmt.Errorf("route %s unavailable", attachmentID)
			}
			hasLink := false
			for _, link := range state.Links {
				if link.AttachmentID == attachmentID && link.Enabled {
					hasLink = true
					break
				}
			}
			if !hasLink {
				return fmt.Errorf("route %s has no enabled Link", attachmentID)
			}
			hasExistingGrant := false
			for _, grant := range state.Grants {
				if grant.UserID == userID && grant.AttachmentID == attachmentID {
					hasExistingGrant = true
					break
				}
			}
			if hasExistingGrant {
				continue
			}
			id, err := newID("grt")
			if err != nil {
				return err
			}
			uuid, err := identity.UUID()
			if err != nil {
				return err
			}
			password, err := identity.Token(24)
			if err != nil {
				return err
			}
			state.Grants[id] = model.Grant{ID: id, UserID: userID, AttachmentID: attachmentID, VMessUUID: uuid, SOCKSUsername: id, SOCKSPassword: password, Enabled: true, CreatedAt: s.now().UTC()}
			bumpGateway(state, attachment.GatewayID)
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) upgradeOptions(w http.ResponseWriter, r *http.Request) {
	role := model.Role(r.PathValue("role"))
	id := r.PathValue("id")
	state := s.store.Snapshot()
	if role == model.RoleGateway {
		if _, ok := state.Gateways[id]; !ok {
			http.NotFound(w, r)
			return
		}
	} else if role == model.RoleAgent {
		if _, ok := state.Agents[id]; !ok {
			http.NotFound(w, r)
			return
		}
	} else {
		http.NotFound(w, r)
		return
	}
	if !s.releaseEnabled() {
		http.Error(w, "release source is not configured", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, "Upgrade %s %s to %s on its existing host. The installer preserves node identity and checks the reported version; a failed upgrade restores the previous image or binaries.\n", role, id, s.cfg.ReleaseVersion)
	s.writeInstallOptions(w, "Controller on-demand cache", strings.TrimSuffix(s.cfg.PublicURL, "/")+"/releases", role)
	s.writeInstallOptions(w, "GitHub release", strings.TrimSuffix(s.cfg.ReleaseBaseURL, "/"), role)
}
