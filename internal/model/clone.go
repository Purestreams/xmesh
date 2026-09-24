package model

import (
	"maps"
	"slices"
)

// Clone returns an independent state for transactional edits and read snapshots.
// Keep nested maps and slices here in sync with State and its value types.
func (s State) Clone() State {
	next := s
	next.Users = maps.Clone(s.Users)
	next.Gateways = maps.Clone(s.Gateways)
	next.Agents = maps.Clone(s.Agents)
	for id, agent := range next.Agents {
		agent.AllowedCIDRs = slices.Clone(agent.AllowedCIDRs)
		agent.DeniedCIDRs = slices.Clone(agent.DeniedCIDRs)
		agent.AllowedPorts = slices.Clone(agent.AllowedPorts)
		next.Agents[id] = agent
	}
	next.Upstreams = maps.Clone(s.Upstreams)
	for id, upstream := range next.Upstreams {
		upstream.Candidates = slices.Clone(upstream.Candidates)
		next.Upstreams[id] = upstream
	}
	next.Attachments = maps.Clone(s.Attachments)
	next.Links = maps.Clone(s.Links)
	next.Grants = maps.Clone(s.Grants)
	next.RetiredGrants = maps.Clone(s.RetiredGrants)
	for id, grant := range next.RetiredGrants {
		grant.LinkNames = maps.Clone(grant.LinkNames)
		next.RetiredGrants[id] = grant
	}
	next.RetiredLinks = maps.Clone(s.RetiredLinks)
	next.Enrollments = maps.Clone(s.Enrollments)
	next.NodeStatus = maps.Clone(s.NodeStatus)
	for id, status := range next.NodeStatus {
		status.FailureCounters = maps.Clone(status.FailureCounters)
		next.NodeStatus[id] = status
	}
	next.LinkStatus = maps.Clone(s.LinkStatus)
	next.GrantStatus = maps.Clone(s.GrantStatus)
	for id, status := range next.GrantStatus {
		status.Links = slices.Clone(status.Links)
		next.GrantStatus[id] = status
	}
	next.UsageHistory = maps.Clone(s.UsageHistory)
	for id, buckets := range next.UsageHistory {
		next.UsageHistory[id] = slices.Clone(buckets)
	}
	next.UsageLabels = maps.Clone(s.UsageLabels)
	next.UsageCounters = maps.Clone(s.UsageCounters)
	next.LinkHistory = maps.Clone(s.LinkHistory)
	for id, samples := range next.LinkHistory {
		next.LinkHistory[id] = slices.Clone(samples)
	}
	next.Operations = slices.Clone(s.Operations)
	next.Updaters = maps.Clone(s.Updaters)
	next.UpgradeTasks = maps.Clone(s.UpgradeTasks)
	for id, task := range next.UpgradeTasks {
		task.BaselineLinks = slices.Clone(task.BaselineLinks)
		next.UpgradeTasks[id] = task
	}
	next.UpgradeBatches = maps.Clone(s.UpgradeBatches)
	for id, batch := range next.UpgradeBatches {
		batch.TaskIDs = slices.Clone(batch.TaskIDs)
		next.UpgradeBatches[id] = batch
	}
	return next
}
