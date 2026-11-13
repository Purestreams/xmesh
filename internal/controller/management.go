package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/Purestreams/xmesh/internal/model"
)

// Management forms follow configuration changes, without fetching HTML for
// ordinary heartbeats, counters or automatic upstream probe results.
func managementRevision(state model.State) string {
	gateways := make(map[string]model.Gateway, len(state.Gateways))
	for id, gateway := range state.Gateways {
		gateway.PublicIPCheckedAt, gateway.PublicIPv6CheckedAt = time.Time{}, time.Time{}
		gateway.ObservedPublicIP = ""
		gateways[id] = gateway
	}
	routes := make(map[string]model.Attachment, len(state.Attachments))
	for id, route := range state.Attachments {
		route.ProbeRequestID, route.ProbeRequestedAt, route.UpstreamProbe = "", time.Time{}, nil
		route.UpstreamProbeFailureSince, route.UpstreamProbeFailureVersion = time.Time{}, 0
		routes[id] = route
	}
	grants := make(map[string][2]string, len(state.Grants))
	for id, grant := range state.Grants {
		grants[id] = [2]string{grant.UserID, grant.AttachmentID}
	}
	encoded, _ := json.Marshal([]any{gateways, state.Agents, routes, state.Links, state.Upstreams, grants})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
