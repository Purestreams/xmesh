package controller

import "xmesh/internal/model"

type GatewayConfig struct {
	Revision uint64              `json:"revision"`
	Gateway  model.Gateway       `json:"gateway"`
	Grants   []model.Grant       `json:"grants"`
	Links    []GatewayLinkConfig `json:"links"`
}

type GatewayLinkConfig struct {
	model.Link
	AgentID     string `json:"agent_id"`
	TunnelToken string `json:"tunnel_token"`
}

type AgentConfig struct {
	Revision uint64            `json:"revision"`
	Agent    model.Agent       `json:"agent"`
	Links    []AgentLinkConfig `json:"links"`
	GrantIDs []string          `json:"grant_ids"`
}

type AgentLinkConfig struct {
	model.Link
	GatewayID        string `json:"gateway_id"`
	TunnelToken      string `json:"tunnel_token"`
	RealityPublicKey string `json:"reality_public_key,omitempty"`
	RealityName      string `json:"reality_name,omitempty"`
}

type EnrollmentRequest struct {
	Token  string     `json:"token"`
	Role   model.Role `json:"role,omitempty"`
	NodeID string     `json:"node_id,omitempty"`
}

type EnrollmentResponse struct {
	Role       model.Role `json:"role"`
	NodeID     string     `json:"node_id"`
	Credential string     `json:"credential"`
	ConfigURL  string     `json:"config_url"`
}
