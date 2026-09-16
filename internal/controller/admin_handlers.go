package controller

import (
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/identity"
	"xmesh/internal/model"
)

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	id, err := newID("usr")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	token, err := identity.Token(32)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		state.Users[id] = model.User{ID: id, Name: name, Enabled: true, SubscriptionToken: token, CreatedAt: s.now().UTC()}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) toggleUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.store.Update(func(state *model.State) error {
		user, ok := state.Users[id]
		if !ok {
			return fmt.Errorf("user not found")
		}
		user.Enabled = !user.Enabled
		state.Users[id] = user
		for grantID, grant := range state.Grants {
			if grant.UserID == id {
				grant.Published = false
				state.Grants[grantID] = grant
				bumpGatewayForAttachment(state, grant.AttachmentID)
			}
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) resetSubscription(w http.ResponseWriter, r *http.Request) {
	token, err := identity.Token(32)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		user, ok := state.Users[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("user not found")
		}
		user.SubscriptionToken = token
		state.Users[user.ID] = user
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) createGateway(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	host := strings.TrimSpace(r.FormValue("public_host"))
	path := strings.TrimSpace(r.FormValue("vmess_path"))
	port, err := parsePositive(r.FormValue("vmess_port"), 8080)
	if err != nil || port > 65535 || name == "" || host == "" {
		http.Error(w, "valid name, public host, and port are required", 400)
		return
	}
	if path == "" {
		path = "/proxy"
	}
	if !strings.HasPrefix(path, "/") {
		http.Error(w, "VMess path must start with /", 400)
		return
	}
	id, err := newID("gw")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		state.Gateways[id] = model.Gateway{ID: id, Name: name, PublicHost: host, VMessPort: port, VMessPath: path, VMessHost: strings.TrimSpace(r.FormValue("vmess_host")), Enabled: true, DesiredVersion: 1, CreatedAt: s.now().UTC()}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", 303)
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name is required", 400)
		return
	}
	id, err := newID("agt")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		allowedCIDRs := splitCSV(r.FormValue("allowed_cidrs"))
		if len(allowedCIDRs) == 0 {
			allowedCIDRs = []string{"0.0.0.0/0", "::/0"}
		}
		state.Agents[id] = model.Agent{ID: id, Name: name, Enabled: true, AllowedCIDRs: allowedCIDRs, DeniedCIDRs: splitCSV(r.FormValue("denied_cidrs")), DesiredVersion: 1, CreatedAt: s.now().UTC()}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", 303)
}

// quickSetup creates a minimal Gateway-Agent route as one desired-state transaction.
func (s *Server) quickSetup(w http.ResponseWriter, r *http.Request) {
	gwName := strings.TrimSpace(r.FormValue("gateway_name"))
	agtName := strings.TrimSpace(r.FormValue("agent_name"))
	publicHost := strings.TrimSpace(r.FormValue("public_host"))
	linkURL := strings.TrimSpace(r.FormValue("link_url"))
	vmessPath := strings.TrimSpace(r.FormValue("vmess_path"))
	vmessPort, portErr := parsePositive(r.FormValue("vmess_port"), 8080)
	allowedCIDRs := splitCSV(r.FormValue("allowed_cidrs"))
	deniedCIDRs := splitCSV(r.FormValue("denied_cidrs"))
	if gwName == "" || agtName == "" || publicHost == "" || strings.ContainsAny(publicHost, "/?#@ \t\r\n") ||
		vmessPort > 65535 || portErr != nil || !strings.HasPrefix(vmessPath, "/") || strings.ContainsAny(vmessPath, " \t\r\n") ||
		validateURL(linkURL) != nil || !strings.HasPrefix(linkURL, "wss://") || len(allowedCIDRs) == 0 {
		http.Error(w, "valid node names, public host, VMess port/path, allowed CIDRs and wss:// Link URL are required", 400)
		return
	}
	for _, cidr := range append(append([]string{}, allowedCIDRs...), deniedCIDRs...) {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			http.Error(w, "invalid CIDR: "+cidr, 400)
			return
		}
	}
	gwID, err := newID("gw")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	agtID, err := newID("agt")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	attachmentID, err := newID("node")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	linkID, err := newID("lnk")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	now := s.now().UTC()
	err = s.store.Update(func(state *model.State) error {
		state.Gateways[gwID] = model.Gateway{ID: gwID, Name: gwName, PublicHost: publicHost, VMessPort: vmessPort, VMessPath: vmessPath, VMessHost: strings.TrimSpace(r.FormValue("vmess_host")), Enabled: true, DesiredVersion: 1, CreatedAt: now}
		state.Agents[agtID] = model.Agent{ID: agtID, Name: agtName, Enabled: true, AllowedCIDRs: allowedCIDRs, DeniedCIDRs: deniedCIDRs, DesiredVersion: 1, CreatedAt: now}
		state.Attachments[attachmentID] = model.Attachment{ID: attachmentID, GatewayID: gwID, AgentID: agtID, Enabled: true, CreatedAt: now}
		state.Links[linkID] = model.Link{ID: linkID, AttachmentID: attachmentID, Name: gwName + " / " + agtName, URL: linkURL, HTTPHost: strings.TrimSpace(r.FormValue("link_http_host")), TLSServerName: strings.TrimSpace(r.FormValue("tls_server_name")), TLSVerify: true, Priority: 10, Weight: 1, Connections: 2, MaxStreams: 256, Enabled: true, TunnelTokenHash: auth.SecretHash(auth.Derive(s.cfg.sessionKey(), "tunnel", linkID)), CreatedAt: now}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) toggleGateway(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		gateway, ok := state.Gateways[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("gateway not found")
		}
		gateway.Enabled = !gateway.Enabled
		gateway.DesiredVersion++
		state.Gateways[gateway.ID] = gateway
		if !gateway.Enabled {
			for id, grant := range state.Grants {
				attachment := state.Attachments[grant.AttachmentID]
				if attachment.GatewayID == gateway.ID {
					grant.Published = false
					state.Grants[id] = grant
				}
			}
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	http.Redirect(w, r, "/", 303)
}

func (s *Server) toggleAgent(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		agent, ok := state.Agents[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("agent not found")
		}
		agent.Enabled = !agent.Enabled
		agent.DesiredVersion++
		state.Agents[agent.ID] = agent
		for _, attachment := range state.Attachments {
			if attachment.AgentID != agent.ID {
				continue
			}
			bumpGateway(state, attachment.GatewayID)
			if !agent.Enabled {
				for id, grant := range state.Grants {
					if grant.AttachmentID == attachment.ID {
						grant.Published = false
						state.Grants[id] = grant
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	http.Redirect(w, r, "/", 303)
}

func (s *Server) toggleAttachment(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		attachment, ok := state.Attachments[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("attachment not found")
		}
		attachment.Enabled = !attachment.Enabled
		state.Attachments[attachment.ID] = attachment
		bumpGateway(state, attachment.GatewayID)
		bumpAgent(state, attachment.AgentID)
		if !attachment.Enabled {
			for id, grant := range state.Grants {
				if grant.AttachmentID == attachment.ID {
					grant.Published = false
					state.Grants[id] = grant
				}
			}
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	http.Redirect(w, r, "/", 303)
}

func (s *Server) toggleLink(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		link, ok := state.Links[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("link not found")
		}
		link.Enabled = !link.Enabled
		state.Links[link.ID] = link
		attachment := state.Attachments[link.AttachmentID]
		bumpGateway(state, attachment.GatewayID)
		bumpAgent(state, attachment.AgentID)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	http.Redirect(w, r, "/", 303)
}

func (s *Server) updateLinkPolicy(w http.ResponseWriter, r *http.Request) {
	priority, err := strconv.Atoi(r.FormValue("priority"))
	if err != nil || priority < 0 {
		http.Error(w, "invalid priority", 400)
		return
	}
	weight, err := parsePositive(r.FormValue("weight"), 1)
	if err != nil {
		http.Error(w, "invalid weight", 400)
		return
	}
	connections, err := parsePositive(r.FormValue("connections"), 2)
	if err != nil {
		http.Error(w, "invalid connections", 400)
		return
	}
	maxStreams, err := parsePositive(r.FormValue("max_streams"), 256)
	if err != nil {
		http.Error(w, "invalid max streams", 400)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		link, ok := state.Links[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("link not found")
		}
		link.Priority, link.Weight, link.Connections, link.MaxStreams = priority, weight, connections, maxStreams
		state.Links[link.ID] = link
		attachment := state.Attachments[link.AttachmentID]
		bumpGateway(state, attachment.GatewayID)
		bumpAgent(state, attachment.AgentID)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	http.Redirect(w, r, "/", 303)
}

func (s *Server) createAttachment(w http.ResponseWriter, r *http.Request) {
	gatewayID, agentID := r.FormValue("gateway_id"), r.FormValue("agent_id")
	id, err := newID("node")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		if _, ok := state.Gateways[gatewayID]; !ok {
			return fmt.Errorf("gateway not found")
		}
		if _, ok := state.Agents[agentID]; !ok {
			return fmt.Errorf("agent not found")
		}
		for _, attachment := range state.Attachments {
			if attachment.GatewayID == gatewayID && attachment.AgentID == agentID {
				return fmt.Errorf("attachment already exists")
			}
		}
		state.Attachments[id] = model.Attachment{ID: id, GatewayID: gatewayID, AgentID: agentID, Enabled: true, CreatedAt: s.now().UTC()}
		bumpGateway(state, gatewayID)
		bumpAgent(state, agentID)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/", 303)
}

func (s *Server) createLink(w http.ResponseWriter, r *http.Request) {
	attachmentID := r.FormValue("attachment_id")
	name, linkURL := strings.TrimSpace(r.FormValue("name")), strings.TrimSpace(r.FormValue("url"))
	priority, err := strconv.Atoi(r.FormValue("priority"))
	if err != nil || priority < 0 {
		http.Error(w, "priority must be zero or greater", 400)
		return
	}
	weight, err := parsePositive(r.FormValue("weight"), 1)
	if err != nil {
		http.Error(w, "invalid weight", 400)
		return
	}
	connections, err := parsePositive(r.FormValue("connections"), 2)
	if err != nil {
		http.Error(w, "invalid connections", 400)
		return
	}
	maxStreams, err := parsePositive(r.FormValue("max_streams"), 256)
	if err != nil {
		http.Error(w, "invalid max streams", 400)
		return
	}
	if name == "" {
		http.Error(w, "name is required", 400)
		return
	}
	if err := validateURL(linkURL); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	id, err := newID("lnk")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	tunnelToken := auth.Derive(s.cfg.sessionKey(), "tunnel", id)
	err = s.store.Update(func(state *model.State) error {
		attachment, ok := state.Attachments[attachmentID]
		if !ok {
			return fmt.Errorf("attachment not found")
		}
		state.Links[id] = model.Link{ID: id, AttachmentID: attachmentID, Name: name, URL: linkURL, HTTPHost: strings.TrimSpace(r.FormValue("http_host")), TLSServerName: strings.TrimSpace(r.FormValue("tls_server_name")), TLSVerify: r.FormValue("tls_verify") == "on", Priority: priority, Weight: weight, Connections: connections, MaxStreams: maxStreams, Enabled: true, TunnelTokenHash: auth.SecretHash(tunnelToken), CreatedAt: s.now().UTC()}
		bumpGateway(state, attachment.GatewayID)
		bumpAgent(state, attachment.AgentID)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/", 303)
}

func (s *Server) createGrant(w http.ResponseWriter, r *http.Request) {
	userID, attachmentID := r.FormValue("user_id"), r.FormValue("attachment_id")
	id, err := newID("grt")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	uuid, err := identity.UUID()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	password, err := identity.Token(24)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		if _, ok := state.Users[userID]; !ok {
			return fmt.Errorf("user not found")
		}
		if _, ok := state.Attachments[attachmentID]; !ok {
			return fmt.Errorf("attachment not found")
		}
		for _, grant := range state.Grants {
			if grant.UserID == userID && grant.AttachmentID == attachmentID {
				return fmt.Errorf("grant already exists")
			}
		}
		state.Grants[id] = model.Grant{ID: id, UserID: userID, AttachmentID: attachmentID, VMessUUID: uuid, SOCKSUsername: id, SOCKSPassword: password, Enabled: true, Published: false, CreatedAt: s.now().UTC()}
		bumpGatewayForAttachment(state, attachmentID)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/", 303)
}

func (s *Server) toggleGrant(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		grant, ok := state.Grants[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("grant not found")
		}
		grant.Enabled = !grant.Enabled
		grant.Published = false
		state.Grants[grant.ID] = grant
		bumpGatewayForAttachment(state, grant.AttachmentID)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	http.Redirect(w, r, "/", 303)
}

func (s *Server) createEnrollment(w http.ResponseWriter, r *http.Request) {
	role := model.Role(r.FormValue("role"))
	nodeID := r.FormValue("node_id")
	if role != model.RoleGateway && role != model.RoleAgent {
		http.Error(w, "invalid role", 400)
		return
	}
	state := s.store.Snapshot()
	if role == model.RoleGateway {
		if _, ok := state.Gateways[nodeID]; !ok {
			http.Error(w, "gateway not found", 404)
			return
		}
	}
	if role == model.RoleAgent {
		if _, ok := state.Agents[nodeID]; !ok {
			http.Error(w, "agent not found", 404)
			return
		}
	}
	if !s.releaseEnabled() {
		http.Error(w, "configure a release version and a GitHub or Controller release source", 409)
		return
	}
	token, err := identity.Token(32)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	id, err := newID("enr")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		for existingID, enrollment := range state.Enrollments {
			if enrollment.NodeID == nodeID && enrollment.Role == role && enrollment.UsedAt.IsZero() {
				enrollment.ExpiresAt = s.now().UTC()
				state.Enrollments[existingID] = enrollment
			}
		}
		state.Enrollments[id] = model.Enrollment{ID: id, NodeID: nodeID, Role: role, TokenHash: auth.SecretHash(token), ExpiresAt: s.now().Add(30 * time.Minute).UTC(), CreatedAt: s.now().UTC()}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, "One-time enrollment token (expires in 30 minutes):\n%s\n\nPaste one command on the %s host. The installer prompts for the token; it is not embedded in the command.\n", token, role)
	s.writeInstallOptions(w, "Controller on-demand cache", strings.TrimSuffix(s.cfg.PublicURL, "/")+"/releases", role)
	s.writeInstallOptions(w, "GitHub release", strings.TrimSuffix(s.cfg.ReleaseBaseURL, "/"), role)
}

func (s *Server) revokeEnrollment(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		enrollment, ok := state.Enrollments[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("enrollment not found")
		}
		enrollment.ExpiresAt = s.now().UTC()
		state.Enrollments[enrollment.ID] = enrollment
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) writeInstallOptions(w http.ResponseWriter, label, base string, role model.Role) {
	for _, method := range []struct{ label, script string }{{"systemd", "install.sh"}, {"Docker Compose", "install-docker.sh"}} {
		_, _ = fmt.Fprintf(w, "\n%s / %s:\n%s\n", label, method.label, s.installCommand(base, method.script, role))
	}
}

func (s *Server) installCommand(base, script string, role model.Role) string {
	versionBase := strings.TrimSuffix(base, "/") + "/" + s.cfg.ReleaseVersion
	return fmt.Sprintf("(set -eu; work=$(mktemp -d); trap 'rm -rf \"$work\"' EXIT; curl -fL --retry 3 --proto '=https' --proto-redir '=https' -o \"$work/SHA256SUMS\" %s; curl -fL --retry 3 --proto '=https' --proto-redir '=https' -o \"$work/%s\" %s; (cd \"$work\" && grep '  %s$' SHA256SUMS | sha256sum -c -); sudo sh \"$work/%s\" --controller %s --role %s --version %s --release-base-url %s)",
		shellQuote(versionBase+"/SHA256SUMS"), script, shellQuote(versionBase+"/"+script), script, script,
		shellQuote(s.cfg.PublicURL), shellQuote(string(role)), shellQuote(s.cfg.ReleaseVersion), shellQuote(base))
}

func bumpGatewayForAttachment(state *model.State, attachmentID string) {
	if attachment, ok := state.Attachments[attachmentID]; ok {
		bumpGateway(state, attachment.GatewayID)
	}
}
func bumpGateway(state *model.State, id string) {
	gateway := state.Gateways[id]
	gateway.DesiredVersion++
	state.Gateways[id] = gateway
}
func bumpAgent(state *model.State, id string) {
	agent := state.Agents[id]
	agent.DesiredVersion++
	state.Agents[id] = agent
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func splitCSV(value string) []string {
	var result []string
	for _, part := range strings.Split(value, ",") {
		if item := strings.TrimSpace(part); item != "" {
			result = append(result, item)
		}
	}
	return result
}
