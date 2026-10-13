package controller

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"xmesh/internal/identity"
	"xmesh/internal/model"
)

// assignGateways creates or re-enables only the selected Agent/Gateway routes.
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
	pairs := make([]connectionPair, 0, len(selected))
	for _, gatewayID := range selected {
		pairs = append(pairs, connectionPair{AgentID: agentID, GatewayID: gatewayID})
	}
	var counts connectionCounts
	err := s.store.Update(func(state *model.State) error {
		var err error
		counts, err = s.enableConnections(state, pairs, target)
		return err
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/?notice="+url.QueryEscape(fmt.Sprintf("Gateway assignment: %d created, %d re-enabled, %d already active", counts.Created, counts.Reenabled, counts.Unchanged)), http.StatusSeeOther)
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
	created, reenabled, unchanged := 0, 0, 0
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
			grantReenabled := false
			attachment, ok := state.Attachments[attachmentID]
			if !ok || !routeAvailable(state, attachment) {
				return fmt.Errorf("route %s unavailable", attachmentID)
			}
			hasExistingGrant := false
			for id, grant := range state.Grants {
				if grant.UserID == userID && grant.AttachmentID == attachmentID {
					hasExistingGrant = true
					if !grant.Enabled {
						grant.Enabled = true
						grant.Published = false
						state.Grants[id] = grant
						bumpGateway(state, attachment.GatewayID)
						grantReenabled = true
					}
					break
				}
			}
			if hasExistingGrant {
				if grantReenabled {
					reenabled++
				} else {
					unchanged++
				}
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
			gateway := state.Gateways[attachment.GatewayID]
			if err := configureHY2(state, &gateway, gateway.HY2Port, s.now()); err != nil {
				return err
			}
			state.Gateways[gateway.ID] = gateway
			created++
			bumpGateway(state, attachment.GatewayID)
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/?notice="+url.QueryEscape(fmt.Sprintf("Subscription routes: %d created, %d re-enabled, %d already active", created, reenabled, unchanged)), http.StatusSeeOther)
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
	portArgs := nodeInstallPortArgs(state, role, id)
	s.writeInstallOptions(w, "Controller on-demand cache", strings.TrimSuffix(s.cfg.PublicURL, "/")+"/releases", role, portArgs...)
	s.writeInstallOptions(w, "GitHub release", strings.TrimSuffix(s.cfg.ReleaseBaseURL, "/"), role, portArgs...)
}
