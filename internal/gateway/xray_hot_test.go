package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"

	handlercommand "github.com/xtls/xray-core/app/proxyman/command"
	"google.golang.org/grpc"

	"github.com/Purestreams/xmesh/internal/controller"
	"github.com/Purestreams/xmesh/internal/model"
	"github.com/Purestreams/xmesh/internal/runtimecfg"
)

func TestXrayHotAdditionRejectsChangesToExistingTraffic(t *testing.T) {
	base := controller.GatewayConfig{Gateway: model.Gateway{ID: "g", Enabled: true, VMessPort: 8080, VMessPath: "/proxy"}, Grants: []model.Grant{{ID: "old", Enabled: true, AttachmentID: "route", VMessUUID: "00000000-0000-4000-8000-000000000001", SOCKSUsername: "old", SOCKSPassword: "password"}}}
	render := func(config controller.GatewayConfig) []byte {
		t.Helper()
		payload, err := buildXrayConfigWithStats(config, "127.0.0.1:18080", "", "", "127.0.0.1:18082")
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	previous := render(base)
	for _, tc := range []struct {
		name   string
		change func(*controller.GatewayConfig)
	}{
		{"revoke", func(c *controller.GatewayConfig) { c.Grants[0].Enabled = false }},
		{"rotate-vmess", func(c *controller.GatewayConfig) { c.Grants[0].VMessUUID = "00000000-0000-4000-8000-000000000002" }},
		{"rotate-socks", func(c *controller.GatewayConfig) { c.Grants[0].SOCKSPassword = "rotated" }},
		{"listener", func(c *controller.GatewayConfig) { c.Gateway.VMessPort++ }},
		{"transport", func(c *controller.GatewayConfig) { c.Gateway.VMessPath = "/new" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			candidate.Grants = append([]model.Grant(nil), base.Grants...)
			tc.change(&candidate)
			if plan, err := planXrayAddition(previous, render(candidate)); err != nil || plan != nil {
				t.Fatalf("unsafe hot update accepted: plan=%v err=%v", plan, err)
			}
		})
	}
	candidate := base
	candidate.Grants = append(append([]model.Grant(nil), base.Grants...), model.Grant{ID: "added", Enabled: true, AttachmentID: "route", VMessUUID: "00000000-0000-4000-8000-000000000002", SOCKSUsername: "added", SOCKSPassword: "password"})
	plan, err := planXrayAddition(previous, render(candidate))
	if err != nil || plan == nil || len(plan.users) != 1 || len(plan.outbounds) != 1 {
		t.Fatalf("valid addition rejected: plan=%+v err=%v", plan, err)
	}
	// Old Xray configs without the local control API cannot use hot updates.
	var old xrayConfig
	if err := json.Unmarshal(previous, &old); err != nil {
		t.Fatal(err)
	}
	old.API.Services = []string{"StatsService"}
	legacy, _ := json.Marshal(old)
	if plan, err := planXrayAddition(legacy, render(candidate)); err != nil || plan != nil {
		t.Fatalf("hot update accepted without HandlerService: %v %v", plan, err)
	}
}

type partialXrayAPI struct {
	handlercommand.UnimplementedHandlerServiceServer
	added atomic.Int32
}

func (api *partialXrayAPI) AddOutbound(context.Context, *handlercommand.AddOutboundRequest) (*handlercommand.AddOutboundResponse, error) {
	api.added.Add(1)
	return &handlercommand.AddOutboundResponse{}, nil
}

func TestXrayHotAdditionDoesNotAcknowledgePartialRPCFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	api := &partialXrayAPI{}
	handlercommand.RegisterHandlerServiceServer(server, api)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	r := New(runtimecfg.Config{Gateway: runtimecfg.Gateway{StatsListen: listener.Addr().String(), XrayConfigPath: filepath.Join(t.TempDir(), "xray.json")}}, slog.Default())
	config := controller.GatewayConfig{Gateway: model.Gateway{ID: "g", Enabled: true, VMessPort: 8080, VMessPath: "/proxy"}}
	previous, err := buildXrayConfigWithStats(config, "127.0.0.1:18080", "", "", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	config.Grants = []model.Grant{{ID: "new", Enabled: true, VMessUUID: "00000000-0000-4000-8000-000000000001", SOCKSUsername: "user", SOCKSPassword: "password"}}
	next, err := buildXrayConfigWithStats(config, "127.0.0.1:18080", "", "", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	// AddOutbound succeeds; RoutingService is unavailable. The caller must
	// reconcile by restarting, rather than acknowledge this partial update.
	if applied, err := r.hotAddXrayConfig(context.Background(), previous, next); applied || err == nil || api.added.Load() != 1 {
		t.Fatalf("partial update acknowledged or not exercised: applied=%v err=%v added=%d", applied, err, api.added.Load())
	}
}
