package agent

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/controller"
	"github.com/Purestreams/xmesh/internal/model"
	"github.com/Purestreams/xmesh/internal/runtimecfg"
)

func TestReconnectDelayRecoversAfterStableSession(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name  string
		ready time.Time
		want  time.Duration
	}{
		{"dial failure", time.Time{}, 30 * time.Second},
		{"unstable session", now.Add(-29 * time.Second), 30 * time.Second},
		{"stable session", now.Add(-30 * time.Second), time.Second},
		{"long lived session", now.Add(-time.Hour), time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconnectDelay(32*time.Second, tc.ready, now); got != tc.want {
				t.Fatalf("delay=%s, want %s", got, tc.want)
			}
		})
	}
}

func TestApplyConfigPrunesDeletedLinkStatus(t *testing.T) {
	r := New(runtimecfg.Config{NodeID: "a"}, slog.Default())
	r.statuses["deleted"] = &linkRuntimeStatus{LinkStatus: model.LinkStatus{LinkID: "deleted"}}
	r.statuses["kept"] = &linkRuntimeStatus{LinkStatus: model.LinkStatus{LinkID: "kept"}}
	config := controller.AgentConfig{Agent: model.Agent{ID: "a", Enabled: true, AllowedCIDRs: []string{"127.0.0.0/8"}}, Links: []controller.AgentLinkConfig{{Link: model.Link{ID: "kept", Enabled: true}}}}
	if err := r.ApplyConfig(config); err != nil {
		t.Fatal(err)
	}
	if len(r.statuses) != 1 || r.statuses["kept"] == nil {
		t.Fatal("stale status survived config removal")
	}
	// A cancelled old worker must not recreate its deleted status.
	r.updateStatus("deleted", func(s *model.LinkStatus) { s.LastError = context.Canceled.Error() })
	if len(r.statuses) != 1 {
		t.Fatal("retired worker recreated a status")
	}
}
