package controller

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Purestreams/xmesh/internal/model"
)

const upstreamSubscriptionFailureGrace = 5 * time.Minute

// Match the conditions that actually emit at least one URI in BuildSubscription.
func grantImportable(state model.State, grant model.Grant, now time.Time) bool {
	user, exists := state.Users[grant.UserID]
	route, routeExists := state.Attachments[grant.AttachmentID]
	gateway, gatewayExists := state.Gateways[route.GatewayID]
	if !exists || !user.Enabled || user.Archived || !grant.Enabled || !grant.Published || !routeExists || !gatewayExists || !subscriptionRouteAvailable(&state, route, now) {
		return false
	}
	return gateway.VMessEnabled() || (gateway.HY2Enabled() && state.NodeStatus[gateway.ID].HY2Ready && grant.HY2Auth != "" && gateway.HY2PinSHA256 != "")
}

func subscriptionRouteAvailable(state *model.State, attachment model.Attachment, now time.Time) bool {
	if state.Gateways[attachment.GatewayID].PublicHost == "" || !routeAvailable(state, attachment) {
		return false
	}
	if attachment.UpstreamID == "" {
		return true
	}
	gateway := state.Gateways[attachment.GatewayID]
	return attachment.UpstreamProbeFailureVersion != gateway.DesiredVersion ||
		attachment.UpstreamProbeFailureSince.IsZero() ||
		now.Sub(attachment.UpstreamProbeFailureSince) <= upstreamSubscriptionFailureGrace
}

func routeDisplayName(state model.State, route model.Attachment) string {
	if route.Name != "" {
		return route.Name
	}
	if names := routeLinkNames(state, route.ID); len(names) > 0 {
		return strings.Join(names, " / ")
	}
	if route.UpstreamID != "" {
		return state.Gateways[route.GatewayID].Name + " / " + state.Upstreams[route.UpstreamID].Name
	}
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
	return buildSubscriptionAt(state, userID, time.Now())
}

func buildSubscriptionAt(state model.State, userID string, now time.Time) (string, error) {
	user, ok := state.Users[userID]
	if !ok || !user.Enabled || user.Archived {
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
		if !ok || !subscriptionRouteAvailable(&state, attachment, now) {
			continue
		}
		gateway, gatewayOK := state.Gateways[attachment.GatewayID]
		if !gatewayOK || !gateway.Enabled {
			continue
		}
		addresses := []string{gateway.PublicHost}
		if extra := gateway.IPv6IngressHost(); extra != "" && extra != gateway.PublicHost && state.NodeStatus[gateway.ID].IPv6IngressReady && gatewayIPv6Supported(state.NodeStatus[gateway.ID].BinaryVersion) {
			addresses = append(addresses, extra)
		}
		for index, host := range addresses {
			name, suffix := routeDisplayName(state, attachment), ""
			if index != 0 {
				name += " / IPv6"
				suffix = "/ipv6"
			}
			share := vmessShare{
				Version: "2", Name: name,
				Address: host, Port: fmt.Sprintf("%d", gateway.VMessPort),
				ID: grant.VMessUUID, AlterID: "0", Net: "ws", Type: "none",
				Host: gateway.VMessHost, Path: gateway.VMessPath, TLS: "", Cipher: "auto",
			}
			payload, err := json.Marshal(share)
			if err != nil {
				return "", err
			}
			if gateway.VMessEnabled() {
				items = append(items, item{
					name:    share.Name,
					nodeID:  attachment.ID,
					grantID: grant.ID + suffix,
					link:    "vmess://" + base64.RawStdEncoding.EncodeToString(payload),
				})
			}
			if gateway.HY2Enabled() && state.NodeStatus[gateway.ID].HY2Ready && grant.HY2Auth != "" && gateway.HY2PinSHA256 != "" {
				endpoint := gateway
				endpoint.PublicHost = host
				items = append(items, item{name: share.Name, nodeID: attachment.ID, grantID: grant.ID + suffix + "/hy2", link: hy2Share(endpoint, grant, share.Name)})
			}
		}
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
