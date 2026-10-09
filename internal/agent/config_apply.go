package agent

import (
	"strconv"
	"strings"

	"xmesh/internal/controller"
	"xmesh/internal/model"
)

// Only fields used to authenticate/dial a tunnel require rebuilding it.
func sameTunnel(left, right controller.AgentLinkConfig) bool {
	if !(left.ID == right.ID && left.AttachmentID == right.AttachmentID &&
		left.GatewayID == right.GatewayID && left.URL == right.URL &&
		left.TunnelToken == right.TunnelToken && left.HTTPHost == right.HTTPHost) {
		return false
	}
	if strings.HasPrefix(left.URL, "reality://") {
		return left.RealityUUID == right.RealityUUID && left.RealityShortID == right.RealityShortID && left.RealityPublicKey == right.RealityPublicKey && left.RealityName == right.RealityName
	}
	return left.TLSServerName == right.TLSServerName && left.TLSVerify == right.TLSVerify
}

func linkConfigs(links []controller.AgentLinkConfig) map[string]controller.AgentLinkConfig {
	result := make(map[string]controller.AgentLinkConfig, len(links))
	for _, link := range links {
		result[link.ID] = link
	}
	return result
}

func (r *Runtime) retireWorkersLocked(config controller.AgentConfig) {
	oldLinks, nextLinks := linkConfigs(r.config.Links), linkConfigs(config.Links)
	for key, cancel := range r.workers {
		separator := strings.LastIndexByte(key, '#')
		if separator < 0 {
			if !config.Agent.Enabled {
				cancel()
				delete(r.workers, key)
			}
			continue
		}
		id := key[:separator]
		index, err := strconv.Atoi(key[separator+1:])
		old, existed := oldLinks[id]
		next, exists := nextLinks[id]
		if !config.Agent.Enabled || !exists || !next.Enabled || !existed || err != nil || index >= next.Connections || !sameTunnel(old, next) {
			cancel()
			delete(r.workers, key)
		}
	}
	for id, old := range oldLinks {
		next, exists := nextLinks[id]
		if !config.Agent.Enabled || !exists || !next.Enabled || !sameTunnel(old, next) {
			r.closeReality(id)
		}
	}
}

func (r *Runtime) retireStreamsLocked(policyChanged bool) {
	policy, err := compilePolicy(r.config.Agent)
	for _, stream := range r.activeStreams {
		if !r.grantAllowedLocked(stream.linkID, stream.grantID) || (policyChanged && (err != nil || stream.target == nil || !policy.permitsTCP(stream.target))) {
			stream.cancel()
		}
	}
}

func sameAccessPolicy(left, right model.Agent) bool {
	a, errA := compilePolicy(left)
	b, errB := compilePolicy(right)
	if errA != nil || errB != nil {
		return false
	}
	for i := range a.allow {
		a.allow[i] = a.allow[i].Masked()
	}
	for i := range a.deny {
		a.deny[i] = a.deny[i].Masked()
	}
	for i := range b.allow {
		b.allow[i] = b.allow[i].Masked()
	}
	for i := range b.deny {
		b.deny[i] = b.deny[i].Masked()
	}
	return sameSet(a.allow, b.allow) && sameSet(a.deny, b.deny) && sameSet(left.AllowedPorts, right.AllowedPorts)
}

func sameSet[T comparable](left, right []T) bool {
	a, b := make(map[T]bool, len(left)), make(map[T]bool, len(right))
	for _, value := range left {
		a[value] = true
	}
	for _, value := range right {
		b[value] = true
	}
	if len(a) != len(b) {
		return false
	}
	for value := range a {
		if !b[value] {
			return false
		}
	}
	return true
}
