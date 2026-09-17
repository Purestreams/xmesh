package controller

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"xmesh/internal/model"
)

func (s *Server) editGateway(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	host := strings.TrimSpace(r.FormValue("public_host"))
	path := strings.TrimSpace(r.FormValue("vmess_path"))
	port, err := parsePositive(r.FormValue("vmess_port"), 0)
	if name == "" || host == "" || port < 1 || port > 65535 || err != nil || !strings.HasPrefix(path, "/") {
		http.Error(w, "valid name, public host, port, and WS path are required", http.StatusBadRequest)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		gateway, ok := state.Gateways[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("gateway not found")
		}
		vmessHost := strings.TrimSpace(r.FormValue("vmess_host"))
		connectionChanged := gateway.PublicHost != host || gateway.VMessPort != port || gateway.VMessPath != path || gateway.VMessHost != vmessHost
		if gateway.PublicHost != host {
			oldURL := "reality://" + net.JoinHostPort(gateway.PublicHost, "8443") + "/tunnel"
			newURL := "reality://" + net.JoinHostPort(host, "8443") + "/tunnel"
			for id, link := range state.Links {
				attachment := state.Attachments[link.AttachmentID]
				if attachment.GatewayID == gateway.ID && link.URL == oldURL {
					link.URL = newURL
					state.Links[id] = link
					bumpAgent(state, attachment.AgentID)
				}
			}
		}
		gateway.Name, gateway.PublicHost, gateway.VMessPort, gateway.VMessPath, gateway.VMessHost = name, host, port, path, vmessHost
		gateway.DesiredVersion++
		state.Gateways[gateway.ID] = gateway
		if connectionChanged {
			for id, grant := range state.Grants {
				if state.Attachments[grant.AttachmentID].GatewayID == gateway.ID {
					grant.Published = false
					state.Grants[id] = grant
				}
			}
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) editAgent(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	allowed := splitCSV(r.FormValue("allowed_cidrs"))
	denied := splitCSV(r.FormValue("denied_cidrs"))
	if len(allowed) == 0 {
		http.Error(w, "at least one allowed CIDR is required", http.StatusBadRequest)
		return
	}
	for _, cidr := range append(append([]string{}, allowed...), denied...) {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			http.Error(w, "invalid CIDR: "+cidr, http.StatusBadRequest)
			return
		}
	}
	err := s.store.Update(func(state *model.State) error {
		agent, ok := state.Agents[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("agent not found")
		}
		agent.Name = name
		agent.AllowedCIDRs, agent.DeniedCIDRs = allowed, denied
		agent.DesiredVersion++
		state.Agents[agent.ID] = agent
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) deleteGateway(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		gateway, ok := state.Gateways[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("gateway not found")
		}
		if r.FormValue("confirm_name") != gateway.Name {
			return fmt.Errorf("confirmation name does not match")
		}
		removed := map[string]bool{gateway.ID: true}
		delete(state.Gateways, gateway.ID)
		removeNodeReferences(state, removed)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		agent, ok := state.Agents[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("agent not found")
		}
		if r.FormValue("confirm_name") != agent.Name {
			return fmt.Errorf("confirmation name does not match")
		}
		delete(state.Agents, agent.ID)
		removeNodeReferences(state, map[string]bool{agent.ID: true})
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func removeNodeReferences(state *model.State, removed map[string]bool) {
	for id, attachment := range state.Attachments {
		if !removed[attachment.GatewayID] && !removed[attachment.AgentID] {
			continue
		}
		removeAttachment(state, id)
	}
	for id, enrollment := range state.Enrollments {
		if removed[enrollment.NodeID] {
			delete(state.Enrollments, id)
		}
	}
	for id, status := range state.NodeStatus {
		if removed[status.NodeID] {
			delete(state.NodeStatus, id)
		}
	}
}

func removeAttachment(state *model.State, id string) {
	attachment, ok := state.Attachments[id]
	if !ok {
		return
	}
	if gateway, ok := state.Gateways[attachment.GatewayID]; ok {
		gateway.DesiredVersion++
		state.Gateways[gateway.ID] = gateway
	}
	if agent, ok := state.Agents[attachment.AgentID]; ok {
		agent.DesiredVersion++
		state.Agents[agent.ID] = agent
	}
	for linkID, link := range state.Links {
		if link.AttachmentID == id {
			removeLink(state, linkID)
		}
	}
	for grantID, grant := range state.Grants {
		if grant.AttachmentID == id {
			removeGrant(state, grantID)
		}
	}
	delete(state.Attachments, id)
}

func removeLink(state *model.State, id string) {
	delete(state.Links, id)
	for statusID, status := range state.LinkStatus {
		if status.LinkID == id {
			delete(state.LinkStatus, statusID)
		}
	}
}

func removeGrant(state *model.State, id string) {
	delete(state.Grants, id)
	for statusID, status := range state.GrantStatus {
		if status.GrantID == id {
			delete(state.GrantStatus, statusID)
		}
	}
}
