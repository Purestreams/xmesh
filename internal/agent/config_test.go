package agent

import (
	"context"
	"log/slog"
	"testing"

	"xmesh/internal/controller"
	"xmesh/internal/model"
	"xmesh/internal/runtimecfg"
)

func TestApplyConfigPreservesWorkersAndRetiresOnlyAffectedSlots(t *testing.T) {
	r := New(runtimecfg.Config{NodeID: "agent"}, slog.Default())
	config := controller.AgentConfig{
		Revision: 1,
		Agent:    model.Agent{ID: "agent", Enabled: true, AllowedCIDRs: []string{"0.0.0.0/0"}},
		Links: []controller.AgentLinkConfig{
			{Link: model.Link{ID: "link-b", Enabled: true, Connections: 2, URL: "ws://gateway-b/tunnel"}},
			{Link: model.Link{ID: "link-a", Enabled: true, Connections: 2, URL: "ws://gateway-a/tunnel"}},
		},
		GrantIDs: []string{"grant-b", "grant-a"},
	}
	if err := r.ApplyConfig(config); err != nil {
		t.Fatal(err)
	}
	if config.Links[0].ID != "link-b" || config.GrantIDs[0] != "grant-b" {
		t.Fatal("applying configuration reordered the caller's slices")
	}
	fingerprint := r.configFingerprint
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.workers["link-a#0"] = cancel
	config.Links[0], config.Links[1] = config.Links[1], config.Links[0]
	config.GrantIDs[0], config.GrantIDs[1] = config.GrantIDs[1], config.GrantIDs[0]
	if err := r.ApplyConfig(config); err != nil {
		t.Fatal(err)
	}
	if r.configFingerprint != fingerprint || ctx.Err() != nil || len(r.workers) != 1 {
		t.Fatal("reordered configuration restarted a live worker")
	}
	config.Links[0].Connections = 3
	if r.config.Links[0].Connections != 2 {
		t.Fatal("changing the caller's config mutated the active runtime config")
	}
	if err := r.ApplyConfig(config); err != nil {
		t.Fatal(err)
	}
	if r.configFingerprint == fingerprint || ctx.Err() != nil || len(r.workers) != 1 {
		t.Fatal("adding a connection stopped an existing worker")
	}
	surplus, cancelSurplus := context.WithCancel(context.Background())
	defer cancelSurplus()
	other, cancelOther := context.WithCancel(context.Background())
	defer cancelOther()
	r.workers["link-a#2"] = cancelSurplus
	r.workers["link-b#0"] = cancelOther
	config.Revision++
	config.Agent.Name = "renamed"
	config.Links[0].Name = "renamed link"
	config.Links[0].Priority, config.Links[0].Weight, config.Links[0].MaxStreams = 20, 3, 5
	config.Links[0].GrantIDs = []string{"new-grant"}
	if err := r.ApplyConfig(config); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil || surplus.Err() != nil || other.Err() != nil {
		t.Fatal("hot configuration update stopped a worker")
	}
	config.Links[0].Connections = 1
	if err := r.ApplyConfig(config); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil || surplus.Err() == nil || other.Err() != nil || len(r.workers) != 2 {
		t.Fatal("reducing connections did not retire only the surplus slot")
	}
	config.Links[0].TunnelToken = "rotated"
	if err := r.ApplyConfig(config); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil || other.Err() != nil || len(r.workers) != 1 {
		t.Fatal("transport change did not retire only its own link")
	}
}
