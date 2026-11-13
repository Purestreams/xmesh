package controller

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Purestreams/xmesh/internal/model"
)

func routeLinkNames(state model.State, routeID string) []string {
	names := []string{}
	seen := map[string]bool{}
	for _, link := range state.Links {
		if link.AttachmentID == routeID && link.Name != "" && !seen[link.Name] {
			names = append(names, link.Name)
			seen[link.Name] = true
		}
	}
	sort.Strings(names)
	return names
}

// Explicit desired state makes retries safe and changes only selected grants.
func (s *Server) setGrantState(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	value := r.PostForm.Get("enabled")
	if value != "true" && value != "false" {
		http.Error(w, "choose enable or disable", http.StatusBadRequest)
		return
	}
	ids := r.PostForm["grant_id"]
	if id := r.PathValue("id"); id != "" {
		ids = []string{id}
	}
	if len(ids) == 0 {
		http.Error(w, "select at least one grant", http.StatusBadRequest)
		return
	}
	enabled, changed := value == "true", 0
	err := s.store.Update(func(state *model.State) error {
		selected := map[string]bool{}
		for _, id := range ids {
			if _, ok := state.Grants[id]; !ok {
				return fmt.Errorf("grant not found")
			}
			selected[id] = true
		}
		gateways := map[string]bool{}
		changedIDs := map[string]bool{}
		for id := range selected {
			grant := state.Grants[id]
			if grant.Enabled == enabled {
				continue
			}
			grant.Enabled, grant.Published = enabled, false
			state.Grants[id] = grant
			gateways[state.Attachments[grant.AttachmentID].GatewayID] = true
			changed++
			changedIDs[id] = true
		}
		for id := range gateways {
			bumpGateway(state, id)
		}
		for id := range changedIDs {
			grant := state.Grants[id]
			if gateways[state.Attachments[grant.AttachmentID].GatewayID] {
				grant.AccessVersion = state.Gateways[state.Attachments[grant.AttachmentID].GatewayID].DesiredVersion
				state.Grants[id] = grant
			}
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	action := "disabled"
	if enabled {
		action = "enabled"
	}
	message := fmt.Sprintf("User route access %s: %d changed; waiting for Gateway configuration", action, changed)
	if changed == 0 {
		message = "Selected route access is already " + action + ". Check each route for Gateway confirmation."
	}
	http.Redirect(w, r, "/?notice="+url.QueryEscape(message)+"#grants", http.StatusSeeOther)
}

func (s *Server) editLink(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	linkURL := strings.TrimSpace(r.FormValue("url"))
	if name == "" {
		http.Error(w, "valid name and Link URL are required", http.StatusBadRequest)
		return
	}
	err := s.store.Update(func(state *model.State) error {
		link, ok := state.Links[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("link not found")
		}
		pending := link.AutoURL && link.URL == ""
		if !(pending && linkURL == "") && validateURL(linkURL) != nil {
			return fmt.Errorf("valid Link URL is required")
		}
		oldReality := strings.HasPrefix(link.URL, "reality://") || (pending && link.RealityUUID != "")
		newReality := strings.HasPrefix(linkURL, "reality://") || (pending && linkURL == "" && oldReality)
		if oldReality != newReality {
			return fmt.Errorf("changing between REALITY and legacy WS requires a new Link")
		}
		attachment := state.Attachments[link.AttachmentID]
		if attachment.UpstreamID != "" {
			return fmt.Errorf("external VMess routes do not use Links")
		}
		if newReality {
			if linkURL != "" {
				u, err := url.Parse(linkURL)
				if err != nil || u.Hostname() == "" || u.Port() == "" || u.Path == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
					return fmt.Errorf("REALITY URL must be reality://host:port/path")
				}
				port, err := strconv.Atoi(u.Port())
				if err != nil || port < 1 || port > 65535 {
					return fmt.Errorf("invalid REALITY port")
				}
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
		previous := link
		if link.URL != linkURL {
			link.AutoURL = false
			link.URLOverride = true
		}
		link.Name, link.URL = name, linkURL
		link.HTTPHost = strings.TrimSpace(r.FormValue("http_host"))
		link.TLSServerName = strings.TrimSpace(r.FormValue("tls_server_name"))
		link.TLSVerify = r.FormValue("tls_verify") == "on"
		state.Links[link.ID] = link
		if link.URL != previous.URL || link.HTTPHost != previous.HTTPHost || link.TLSServerName != previous.TLSServerName || link.TLSVerify != previous.TLSVerify {
			bumpGateway(state, attachment.GatewayID)
			bumpAgent(state, attachment.AgentID)
		}
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
		confirmation := state.Users[grant.UserID].Name + " / " + routeDisplayName(*state, state.Attachments[grant.AttachmentID])
		if r.FormValue("confirm_name") != confirmation && r.FormValue("confirm_id") != id {
			return fmt.Errorf("confirmation name does not match")
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
