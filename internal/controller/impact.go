package controller

import "xmesh/internal/model"

type deletionImpact struct {
	Assignments int
	Links       int
	Grants      int
	Users       int
}

func routeImpacts(state model.State) map[string]deletionImpact {
	result := make(map[string]deletionImpact, len(state.Attachments))
	for id := range state.Attachments {
		impact := deletionImpact{Assignments: 1}
		users := map[string]bool{}
		for _, link := range state.Links {
			if link.AttachmentID == id {
				impact.Links++
			}
		}
		for _, grant := range state.Grants {
			if grant.AttachmentID == id {
				impact.Grants++
				users[grant.UserID] = true
			}
		}
		impact.Users = len(users)
		result[id] = impact
	}
	return result
}

func nodeImpacts(state model.State) map[string]deletionImpact {
	routes := routeImpacts(state)
	result := make(map[string]deletionImpact, len(state.Gateways)+len(state.Agents))
	users := map[string]map[string]bool{}
	for id, attachment := range state.Attachments {
		for _, nodeID := range []string{attachment.GatewayID, attachment.AgentID} {
			impact := result[nodeID]
			route := routes[id]
			impact.Assignments += route.Assignments
			impact.Links += route.Links
			impact.Grants += route.Grants
			result[nodeID] = impact
			if users[nodeID] == nil {
				users[nodeID] = map[string]bool{}
			}
			for _, grant := range state.Grants {
				if grant.AttachmentID == id {
					users[nodeID][grant.UserID] = true
				}
			}
		}
	}
	for nodeID, ids := range users {
		impact := result[nodeID]
		impact.Users = len(ids)
		result[nodeID] = impact
	}
	return result
}
