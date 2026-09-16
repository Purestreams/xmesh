package controller

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"

	"xmesh/internal/model"
)

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
		nodeID string
		link   string
	}
	items := make([]item, 0)
	for _, grant := range state.Grants {
		if grant.UserID != userID || !grant.Enabled || !grant.Published {
			continue
		}
		attachment, ok := state.Attachments[grant.AttachmentID]
		if !ok || !attachment.Enabled {
			continue
		}
		gateway, gatewayOK := state.Gateways[attachment.GatewayID]
		agent, agentOK := state.Agents[attachment.AgentID]
		if !gatewayOK || !agentOK || !gateway.Enabled || !agent.Enabled {
			continue
		}
		share := vmessShare{
			Version: "2", Name: gateway.Name + " / " + agent.Name,
			Address: gateway.PublicHost, Port: fmt.Sprintf("%d", gateway.VMessPort),
			ID: grant.VMessUUID, AlterID: "0", Net: "ws", Type: "none",
			Host: gateway.VMessHost, Path: gateway.VMessPath, TLS: "", Cipher: "auto",
		}
		payload, err := json.Marshal(share)
		if err != nil {
			return "", err
		}
		items = append(items, item{
			nodeID: attachment.ID,
			link:   "vmess://" + base64.RawStdEncoding.EncodeToString(payload),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].nodeID < items[j].nodeID })
	plain := ""
	for i, item := range items {
		if i != 0 {
			plain += "\n"
		}
		plain += item.link
	}
	return base64.StdEncoding.EncodeToString([]byte(plain)), nil
}
