package controller

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"xmesh/internal/model"
)

func (s *Server) editLink(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	linkURL := strings.TrimSpace(r.FormValue("url"))
	if name == "" || validateURL(linkURL) != nil {
		http.Error(w, "valid name and Link URL are required", http.StatusBadRequest)
		return
	}
	err := s.store.Update(func(state *model.State) error {
		link, ok := state.Links[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("link not found")
		}
		oldReality := strings.HasPrefix(link.URL, "reality://")
		newReality := strings.HasPrefix(linkURL, "reality://")
		if oldReality != newReality {
			return fmt.Errorf("changing between REALITY and legacy WS requires a new Link")
		}
		attachment := state.Attachments[link.AttachmentID]
		if newReality {
			u, err := url.Parse(linkURL)
			if err != nil || u.Hostname() == "" || u.Port() == "" || u.Path == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return fmt.Errorf("REALITY URL must be reality://host:port/path")
			}
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("invalid REALITY port")
			}
			target := strings.TrimSpace(r.FormValue("reality_target"))
			gateway := state.Gateways[attachment.GatewayID]
			if target == "" {
				target = gateway.RealityTarget
			}
			if target != gateway.RealityTarget {
				name, err := realityTargetName(target)
				if err != nil {
					return err
				}
				gateway.RealityTarget, gateway.RealityName = target, name
				gateway.DesiredVersion++
				state.Gateways[gateway.ID] = gateway
				for _, route := range state.Attachments {
					if route.GatewayID == gateway.ID {
						bumpAgent(state, route.AgentID)
					}
				}
			}
		}
		link.Name, link.URL = name, linkURL
		link.HTTPHost = strings.TrimSpace(r.FormValue("http_host"))
		link.TLSServerName = strings.TrimSpace(r.FormValue("tls_server_name"))
		link.TLSVerify = r.FormValue("tls_verify") == "on"
		state.Links[link.ID] = link
		bumpGateway(state, attachment.GatewayID)
		bumpAgent(state, attachment.AgentID)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func realityTargetName(target string) (string, error) {
	name, port, err := net.SplitHostPort(target)
	if err != nil || name == "" || port != "443" || !strings.Contains(name, ".") || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.ContainsAny(name, "/\\?#@ \t\r\n") || net.ParseIP(name) != nil {
		return "", fmt.Errorf("REALITY target must be a DNS name on port 443")
	}
	return name, nil
}

func (s *Server) deleteLink(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		link, ok := state.Links[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("link not found")
		}
		if r.FormValue("confirm_name") != link.Name {
			return fmt.Errorf("confirmation name does not match")
		}
		attachment := state.Attachments[link.AttachmentID]
		removeLink(state, link.ID)
		unpublishGrantsWithoutLink(state, attachment.ID)
		bumpGateway(state, attachment.GatewayID)
		bumpAgent(state, attachment.AgentID)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		id := r.PathValue("id")
		if _, ok := state.Attachments[id]; !ok {
			return fmt.Errorf("assignment not found")
		}
		if r.FormValue("confirm_id") != id {
			return fmt.Errorf("confirmation ID does not match")
		}
		removeAttachment(state, id)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) deleteGrant(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		id := r.PathValue("id")
		grant, ok := state.Grants[id]
		if !ok {
			return fmt.Errorf("grant not found")
		}
		if r.FormValue("confirm_id") != id {
			return fmt.Errorf("confirmation ID does not match")
		}
		removeGrant(state, id)
		bumpGatewayForAttachment(state, grant.AttachmentID)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func unpublishGrantsWithoutLink(state *model.State, attachmentID string) {
	for _, link := range state.Links {
		if link.AttachmentID == attachmentID && link.Enabled {
			return
		}
	}
	for id, grant := range state.Grants {
		if grant.AttachmentID == attachmentID {
			grant.Published = false
			state.Grants[id] = grant
		}
	}
}
