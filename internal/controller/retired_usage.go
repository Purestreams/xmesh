package controller

import (
	"strings"
	"time"

	"xmesh/internal/model"
)

const retiredUsageLifetime = 7 * 24 * time.Hour

func retireGrant(state *model.State, grant model.Grant) {
	attachment, ok := state.Attachments[grant.AttachmentID]
	if !ok {
		return
	}
	gateway, ok := state.Gateways[attachment.GatewayID]
	if !ok {
		return
	}
	if state.RetiredGrants == nil {
		state.RetiredGrants = map[string]model.RetiredGrant{}
	}
	names := map[string]string{}
	if attachment.UpstreamID != "" {
		names[attachment.ID] = state.Upstreams[attachment.UpstreamID].Name
	}
	for id, link := range state.Links {
		if link.AttachmentID == attachment.ID {
			names[id] = link.Name
		}
	}
	for id, link := range state.RetiredLinks {
		if link.AttachmentID == attachment.ID {
			names[id] = link.Name
		}
	}
	state.RetiredGrants[grant.ID] = model.RetiredGrant{UserID: grant.UserID, AttachmentID: attachment.ID, GatewayID: attachment.GatewayID, LinkNames: names, RetireVersion: gateway.DesiredVersion + 1, ExpiresAt: time.Now().UTC().Add(retiredUsageLifetime)}
}

func retireLink(state *model.State, id string, attachment model.Attachment, name string) {
	if attachment.ID == "" {
		return
	}
	gateway, ok := state.Gateways[attachment.GatewayID]
	if !ok {
		return
	}
	if state.RetiredLinks == nil {
		state.RetiredLinks = map[string]model.RetiredLink{}
	}
	agentVersion := uint64(0)
	if agent, ok := state.Agents[attachment.AgentID]; ok {
		agentVersion = agent.DesiredVersion + 1
	}
	state.RetiredLinks[id] = model.RetiredLink{AttachmentID: attachment.ID, GatewayID: attachment.GatewayID, AgentID: attachment.AgentID, Name: name, GatewayVersion: gateway.DesiredVersion + 1, AgentVersion: agentVersion, ExpiresAt: time.Now().UTC().Add(retiredUsageLifetime)}
}

func pruneRetiredUsage(state *model.State, now time.Time) {
	for id, grant := range state.RetiredGrants {
		status := state.NodeStatus[grant.GatewayID]
		_, gatewayExists := state.Gateways[grant.GatewayID]
		if gatewayExists && now.Before(grant.ExpiresAt) && !(status.XrayReady && status.AppliedVersion >= grant.RetireVersion) {
			continue
		}
		delete(state.RetiredGrants, id)
		for key := range state.UsageCounters {
			if strings.HasPrefix(key, id+"/") {
				delete(state.UsageCounters, key)
			}
		}
	}
	for id, link := range state.RetiredLinks {
		gatewayStatus := state.NodeStatus[link.GatewayID]
		_, gatewayExists := state.Gateways[link.GatewayID]
		gatewayDone := !gatewayExists || gatewayStatus.XrayReady && gatewayStatus.AppliedVersion >= link.GatewayVersion
		agentStatus := state.NodeStatus[link.AgentID]
		_, agentExists := state.Agents[link.AgentID]
		agentDone := !agentExists || agentStatus.AppliedVersion >= link.AgentVersion
		if now.Before(link.ExpiresAt) && !(gatewayDone && agentDone) {
			continue
		}
		delete(state.RetiredLinks, id)
		for key := range state.UsageCounters {
			if strings.HasSuffix(key, "/"+id) {
				delete(state.UsageCounters, key)
			}
		}
	}
}
