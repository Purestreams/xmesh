package agent

import (
	"context"
	"log/slog"
	"testing"

	"xmesh/internal/controller"
	"xmesh/internal/model"
	"xmesh/internal/runtimecfg"
)

func TestApplyConfigIgnoresCollectionOrderButRestartsForRealChange(t *testing.T) {
	r := New(runtimecfg.Config{NodeID: "agent"}, slog.Default())
	config := controller.AgentConfig{
		Revision: 1,
		Agent:    model.Agent{ID: "agent", AllowedCIDRs: []string{"0.0.0.0/0"}},
		Links: []controller.AgentLinkConfig{
			{Link: model.Link{ID: "link-b", Connections: 2}},
			{Link: model.Link{ID: "link-a", Connections: 2}},
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
	if r.configFingerprint == fingerprint || ctx.Err() == nil || len(r.workers) != 0 {
		t.Fatal("real configuration change did not stop the old worker")
	}
}
