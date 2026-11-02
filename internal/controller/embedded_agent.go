package controller

import (
	"fmt"
	"net/http"

	"github.com/Purestreams/xmesh/internal/model"
)

// agentEnabled includes the physical Gateway's switches for embedded exits,
// without overwriting the Agent's configured preference when its owner stops.
func agentEnabled(state *model.State, agent model.Agent) bool {
	if !agent.Enabled {
		return false
	}
	if agent.GatewayID == "" {
		return true
	}
	gateway := state.Gateways[agent.GatewayID]
	return gateway.Enabled && gateway.AgentEnabled
}

func standaloneAgents(state model.State) []model.Agent {
	var result []model.Agent
	for _, agent := range sortedAgents(state) {
		if agent.GatewayID == "" {
			result = append(result, agent)
		}
	}
	return result
}

func attachmentUsesNode(state *model.State, attachment model.Attachment, nodeID string) bool {
	return attachment.GatewayID == nodeID || attachment.AgentID == nodeID || state.Agents[attachment.AgentID].GatewayID == nodeID
}

func embeddedAgentChanged(state *model.State, agent model.Agent) {
	state.Agents[agent.ID] = agent
	for _, attachment := range state.Attachments {
		if attachment.AgentID != agent.ID {
			continue
		}
		bumpGateway(state, attachment.GatewayID)
		for id, grant := range state.Grants {
			if grant.AttachmentID == attachment.ID {
				grant.Published = false
				state.Grants[id] = grant
			}
		}
	}
}

func (s *Server) toggleEmbeddedAgent(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		gateway, ok := state.Gateways[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("gateway not found")
		}
		id := model.GatewayAgentID(gateway.ID)
		agent, exists := state.Agents[id]
		if exists && agent.GatewayID != gateway.ID {
			return fmt.Errorf("embedded Agent identity conflict")
		}
		if !exists {
			agent = model.Agent{ID: id, GatewayID: gateway.ID, Name: gateway.Name + " (Gateway)", AllowedCIDRs: []string{"0.0.0.0/0", "::/0"}, CreatedAt: s.now().UTC()}
		}
		gateway.AgentEnabled = !gateway.AgentEnabled
		gateway.DesiredVersion++
		agent.Enabled = gateway.AgentEnabled
		agent.DesiredVersion++
		state.Gateways[gateway.ID] = gateway
		embeddedAgentChanged(state, agent)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/#gateways", http.StatusSeeOther)
}
