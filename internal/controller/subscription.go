package controller

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"xmesh/internal/model"
)

func routeDisplayName(state model.State, route model.Attachment) string {
	return state.Gateways[route.GatewayID].Name + " / " + state.Agents[route.AgentID].Name
}

// Compare the visible route names, not randomly generated identifiers. IDs only
// break ties, so repeated subscriptions and panel refreshes remain deterministic.
func routeNameLess(leftName, leftID, rightName, rightID string) bool {
	left, right := strings.ToLower(leftName), strings.ToLower(rightName)
	if left != right {
		return left < right
	}
	if leftName != rightName {
		return leftName < rightName
	}
	return leftID < rightID
}

type vmessShare struct {
	Version string `json:"v"`
	Name    string `json:"ps"`
	Address string `json:"add"`
	Port    string `json:"port"`
	ID      string `json:"id"`
	AlterID string `json:"aid"`
	Net     string `json:"net"`
	Type    string `json:"type"`
	Host    string `json:"host"`
	Path    string `json:"path"`
	TLS     string `json:"tls"`
	Cipher  string `json:"scy"`
}

func BuildSubscription(state model.State, userID string) (string, error) {
	user, ok := state.Users[userID]
	if !ok || !user.Enabled {
		return "", fmt.Errorf("user unavailable")
	}
	type item struct {
		name    string
		nodeID  string
		grantID string
		link    string
	}
	items := make([]item, 0)
	for _, grant := range state.Grants {
		if grant.UserID != userID || !grant.Enabled || !grant.Published {
			continue
		}
		attachment, ok := state.Attachments[grant.AttachmentID]
		if !ok || !attachment.Enabled || !attachmentHasEnabledLink(&state, attachment.ID) {
			continue
		}
		gateway, gatewayOK := state.Gateways[attachment.GatewayID]
		agent, agentOK := state.Agents[attachment.AgentID]
		if !gatewayOK || !agentOK || !gateway.Enabled || !agent.Enabled {
			continue
		}
		share := vmessShare{
			Version: "2", Name: routeDisplayName(state, attachment),
			Address: gateway.PublicHost, Port: fmt.Sprintf("%d", gateway.VMessPort),
			ID: grant.VMessUUID, AlterID: "0", Net: "ws", Type: "none",
			Host: gateway.VMessHost, Path: gateway.VMessPath, TLS: "", Cipher: "auto",
		}
		payload, err := json.Marshal(share)
		if err != nil {
			return "", err
		}
		items = append(items, item{
			name:    share.Name,
			nodeID:  attachment.ID,
			grantID: grant.ID,
			link:    "vmess://" + base64.RawStdEncoding.EncodeToString(payload),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.name == right.name && left.nodeID == right.nodeID {
			return left.grantID < right.grantID
		}
		return routeNameLess(left.name, left.nodeID, right.name, right.nodeID)
	})
	plain := ""
	for i, item := range items {
		if i != 0 {
			plain += "\n"
		}
		plain += item.link
	}
	return base64.StdEncoding.EncodeToString([]byte(plain)), nil
}

func attachmentHasEnabledLink(state *model.State, attachmentID string) bool {
	for _, link := range state.Links {
		if link.AttachmentID == attachmentID && link.Enabled {
			return true
		}
	}
	return false
}
