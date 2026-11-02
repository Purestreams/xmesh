package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/controller"
	"github.com/Purestreams/xmesh/internal/model"
	"github.com/Purestreams/xmesh/internal/runtimecfg"
)

func TestEmbeddedAgentServesOwnAndRemoteGateway(t *testing.T) {
	id := model.GatewayAgentID("owner")
	var disabled atomic.Bool
	var reports atomic.Int64
	links := []controller.AgentLinkConfig{}
	for _, gateway := range []string{"owner", "remote"} {
		links = append(links, controller.AgentLinkConfig{Link: model.Link{ID: gateway, AttachmentID: gateway, URL: "ws://" + freeTCPAddress(t) + "/tunnel", Connections: 1, Weight: 1, MaxStreams: 8, Enabled: true}, GatewayID: gateway, TunnelToken: "tunnel-secret", GrantIDs: []string{gateway}})
	}
	// The self Link deliberately names an unreachable public endpoint. The
	// embedded runtime must connect through its owner's local tunnel listener.
	links[0].URL = "reality://unreachable.invalid:8443/tunnel"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer owner-secret" || r.Header.Get("X-XMesh-Embedded-Agent") != "1" {
			http.Error(w, "unexpected identity", 403)
			return
		}
		if r.Method == "POST" {
			var report controller.StatusReport
			_ = json.NewDecoder(r.Body).Decode(&report)
			if report.Status.NodeID == id && report.Status.Role == model.RoleAgent {
				reports.Add(1)
			}
			w.WriteHeader(204)
			return
		}
		cfg := controller.AgentConfig{Agent: model.Agent{ID: id, GatewayID: "owner", Enabled: !disabled.Load(), AllowedCIDRs: []string{"127.0.0.0/8"}}, Links: links, GrantIDs: []string{"owner", "remote"}}
		_ = json.NewEncoder(w).Encode(cfg)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner := New(runtimecfg.Config{NodeID: "owner", Credential: "owner-secret", ControllerURL: server.URL, PollInterval: runtimecfg.Duration(20 * time.Millisecond), StatusInterval: runtimecfg.Duration(20 * time.Millisecond), Gateway: runtimecfg.Gateway{TunnelListen: freeTCPAddress(t), TunnelPath: "/tunnel", SOCKSListen: freeTCPAddress(t), MaxUDPAssociations: 8}}, slog.Default())
	remote := New(runtimecfg.Config{NodeID: "remote", Gateway: runtimecfg.Gateway{TunnelListen: links[1].URL[len("ws://") : len(links[1].URL)-len("/tunnel")], TunnelPath: "/tunnel", SOCKSListen: freeTCPAddress(t), MaxUDPAssociations: 8}}, slog.Default())
	for i, runtime := range []*Runtime{owner, remote} {
		gatewayID := []string{"owner", "remote"}[i]
		runtime.config = controller.GatewayConfig{Gateway: model.Gateway{ID: gatewayID, Enabled: true, AgentEnabled: i == 0}, Links: []controller.GatewayLinkConfig{{Link: links[i].Link, AgentID: id, TunnelToken: "tunnel-secret"}}, Grants: []model.Grant{{ID: gatewayID, AttachmentID: gatewayID, Enabled: true, SOCKSUsername: "user", SOCKSPassword: "password"}}}
	}
	done := make(chan error, 5)
	for _, runtime := range []*Runtime{owner, remote} {
		go func() { done <- runtime.runTunnelServer(ctx) }()
		go func() { done <- runtime.runSOCKS(ctx) }()
	}
	go func() { owner.embeddedAgentLoop(ctx); done <- nil }()
	t.Cleanup(func() {
		cancel()
		for i := 0; i < 5; i++ {
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("embedded runtime did not stop")
			}
		}
	})
	waitFor(t, 5*time.Second, func() bool {
		return len(owner.pool.Snapshot()) == 1 && len(remote.pool.Snapshot()) == 1 && reports.Load() > 0
	})
	target := startTCPEcho(t)
	defer target.Close()
	udpTarget := startUDPEcho(t)
	defer udpTarget.Close()
	for _, runtime := range []*Runtime{owner, remote} {
		conn := dialSOCKS(t, runtime.local.Gateway.SOCKSListen, "user", "password", 1, target.Addr().String())
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, err := conn.Write([]byte("embedded"))
		if err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, 8)
		if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != "embedded" {
			t.Fatalf("embedded TCP: %s %v", reply, err)
		}
		conn.Close()
		control := dialSOCKS(t, runtime.local.Gateway.SOCKSListen, "user", "password", 3, "0.0.0.0:0")
		udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			t.Fatal(err)
		}
		assertUDPEcho(t, udp, control.(*socksTestConn).bound, udpTarget.LocalAddr().(*net.UDPAddr), []byte("embedded-udp"), 3*time.Second)
		udp.Close()
		control.Close()
	}
	disabled.Store(true)
	waitFor(t, 5*time.Second, func() bool { return len(owner.pool.Snapshot()) == 0 && len(remote.pool.Snapshot()) == 0 })
	disabled.Store(false)
	waitFor(t, 5*time.Second, func() bool { return len(owner.pool.Snapshot()) == 1 && len(remote.pool.Snapshot()) == 1 })
}
