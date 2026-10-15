package controller

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
)

type connectionPair struct {
	AgentID, GatewayID string
}

type connectionCounts struct {
	Created, Reenabled, Unchanged int
}

type pendingConnection struct {
	Value, AgentID, GatewayID, AgentName, GatewayName, PublicHost, Target, State string
	Available                                                                    bool
}

func pendingConnections(state model.State) []pendingConnection {
	routes := map[connectionPair]model.Attachment{}
	for _, route := range sortedAttachments(state) {
		if route.AgentID != "" && route.UpstreamID == "" {
			routes[connectionPair{route.AgentID, route.GatewayID}] = route
		}
	}
	activeLinks := map[string]bool{}
	linked := map[string]bool{}
	for _, link := range state.Links {
		linked[link.AttachmentID] = true
		if link.Enabled {
			activeLinks[link.AttachmentID] = true
		}
	}
	var result []pendingConnection
	gateways := sortedGateways(state)
	for _, agent := range sortedAgents(state) {
		for _, gateway := range gateways {
			pair := connectionPair{agent.ID, gateway.ID}
			route, exists := routes[pair]
			if exists && route.Enabled && activeLinks[route.ID] {
				continue
			}
			status := "未建立"
			if exists {
				status = "已停用"
				if !linked[route.ID] {
					status = "待补建 Link"
				}
			}
			target := gateway.RealityTarget
			if target == "" {
				target = defaultRealityTarget(gateway.Region)
			}
			value, _ := json.Marshal([2]string{agent.ID, gateway.ID})
			result = append(result, pendingConnection{
				Value: string(value), AgentID: agent.ID, GatewayID: gateway.ID,
				AgentName: agent.Name, GatewayName: gateway.Name, PublicHost: gateway.PublicHost,
				Target: target, State: status, Available: agent.Enabled && gateway.Enabled && gateway.PublicHost != "",
			})
		}
	}
	return result
}

func (s *Server) assignConnections(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	var pairs []connectionPair
	for _, value := range r.Form["connection"] {
		var ids []string
		if err := json.Unmarshal([]byte(value), &ids); err != nil || len(ids) != 2 || ids[0] == "" || ids[1] == "" {
			http.Error(w, "invalid Agent/Gateway connection", http.StatusBadRequest)
			return
		}
		pairs = append(pairs, connectionPair{ids[0], ids[1]})
	}
	if len(pairs) == 0 {
		http.Error(w, "select at least one connection", http.StatusBadRequest)
		return
	}
	var counts connectionCounts
	err := s.store.Update(func(state *model.State) error {
		var err error
		counts, err = s.enableConnections(state, pairs, strings.TrimSpace(r.FormValue("reality_target")))
		return err
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	message := fmt.Sprintf("连接已开启：新增 %d，重新启用 %d，已开启 %d。", counts.Created, counts.Reenabled, counts.Unchanged)
	http.Redirect(w, r, "/?notice="+url.QueryEscape(message), http.StatusSeeOther)
}

// All selected pairs share a single store transaction, including target provisioning.
func (s *Server) enableConnections(state *model.State, pairs []connectionPair, target string) (connectionCounts, error) {
	counts := connectionCounts{}
	seen := map[connectionPair]bool{}
	routes := map[connectionPair]model.Attachment{}
	for _, route := range sortedAttachments(*state) {
		pair := connectionPair{route.AgentID, route.GatewayID}
		if route.AgentID != "" && route.UpstreamID == "" && routes[pair].ID == "" {
			routes[pair] = route
		}
	}
	links := map[string]model.Link{}
	for _, link := range sortedLinks(*state) {
		previous := links[link.AttachmentID]
		if previous.ID == "" || (!previous.Enabled && link.Enabled) {
			links[link.AttachmentID] = link
		}
	}
	for _, pair := range pairs {
		if seen[pair] {
			continue
		}
		seen[pair] = true
		agent, ok := state.Agents[pair.AgentID]
		if !ok || !agent.Enabled {
			return counts, fmt.Errorf("Agent %s unavailable", pair.AgentID)
		}
		gateway, ok := state.Gateways[pair.GatewayID]
		if !ok || !gateway.Enabled || gateway.PublicHost == "" {
			return counts, fmt.Errorf("Gateway %s unavailable", pair.GatewayID)
		}
		attachment := routes[pair]
		var existingLink model.Link
		if attachment.ID != "" {
			existingLink = links[attachment.ID]
		}
		if attachment.ID != "" && attachment.Enabled && existingLink.ID != "" && existingLink.Enabled {
			counts.Unchanged++
			continue
		}
		if attachment.ID == "" {
			id, err := newID("node")
			if err != nil {
				return counts, err
			}
			attachment = model.Attachment{ID: id, AgentID: agent.ID, GatewayID: gateway.ID, CreatedAt: s.now().UTC()}
		}
		attachment.Enabled = true
		state.Attachments[attachment.ID] = attachment
		if existingLink.ID != "" {
			existingLink.Enabled = true
			state.Links[existingLink.ID] = existingLink
			counts.Reenabled++
		} else {
			linkID, err := newID("lnk")
			if err != nil {
				return counts, err
			}
			linkURL := "reality://" + net.JoinHostPort(gateway.PublicHost, "8443") + "/tunnel"
			linkTarget := target
			if gateway.RealityTarget != "" {
				linkTarget = gateway.RealityTarget
			}
			uuid, shortID, err := provisionReality(state, gateway.ID, linkTarget, linkURL)
			if err != nil {
				return counts, fmt.Errorf("Gateway %s: %w", gateway.Name, err)
			}
			state.Links[linkID] = model.Link{ID: linkID, AttachmentID: attachment.ID, Name: gateway.Name + " / " + agent.Name, URL: linkURL, TLSVerify: true, RealityUUID: uuid, RealityShortID: shortID, Priority: 10, Weight: 1, Connections: 2, MaxStreams: 256, Enabled: true, TunnelTokenHash: auth.SecretHash(auth.Derive(s.cfg.sessionKey(), "tunnel", linkID)), CreatedAt: s.now().UTC()}
			counts.Created++
		}
		bumpGateway(state, gateway.ID)
		bumpAgent(state, agent.ID)
	}
	return counts, nil
}
