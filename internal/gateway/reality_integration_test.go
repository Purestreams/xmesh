package gateway

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	agentpkg "xmesh/internal/agent"
	"xmesh/internal/controller"
	"xmesh/internal/model"
	"xmesh/internal/runtimecfg"
)

func TestRealityTunnelTCPAndUDP(t *testing.T) {
	xray := os.Getenv("XMESH_TEST_XRAY")
	if xray == "" {
		t.Skip("XMESH_TEST_XRAY not set")
	}
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	target.Config.ErrorLog = log.New(io.Discard, "", 0)
	target.StartTLS()
	defer target.Close()
	tunnelAddress, socksAddress, realityAddress := freeTCPAddress(t), freeTCPAddress(t), freeTCPAddress(t)
	link := model.Link{ID: "link-reality", AttachmentID: "node-reality", URL: "reality://" + realityAddress + "/tunnel", RealityUUID: "00000000-0000-4000-8000-000000000003", RealityShortID: "0123456789abcdef", Priority: 10, Weight: 1, Connections: 1, MaxStreams: 16, Enabled: true}
	grant := model.Grant{ID: "grant-reality", AttachmentID: "node-reality", VMessUUID: "00000000-0000-4000-8000-000000000004", SOCKSUsername: "grant-reality", SOCKSPassword: "secret-password", Enabled: true}
	gatewayConfig := controller.GatewayConfig{Gateway: model.Gateway{ID: "gateway-reality", VMessPort: testPort(t, freeTCPAddress(t)), VMessPath: "/proxy", RealityTarget: target.Listener.Addr().String(), RealityName: "example.com", RealityPrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes())}, Links: []controller.GatewayLinkConfig{{Link: link, AgentID: "agent-reality", TunnelToken: "tunnel-secret"}}, Grants: []model.Grant{grant}}
	gatewayRuntime := New(runtimecfg.Config{NodeID: "gateway-reality", Gateway: runtimecfg.Gateway{SOCKSListen: socksAddress, TunnelListen: tunnelAddress, TunnelPath: "/tunnel", RealityListen: realityAddress, MaxUDPAssociations: 8}}, slog.Default())
	gatewayRuntime.config = gatewayConfig
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gatewayRuntime.runTunnelServer(ctx)
	go gatewayRuntime.runSOCKS(ctx)
	serverJSON, err := buildXrayConfigWithReality(gatewayConfig, socksAddress, realityAddress, tunnelAddress)
	if err != nil {
		t.Fatal(err)
	}
	serverProcess := startTestXray(t, xray, serverJSON)
	defer stopTestProcess(serverProcess)
	waitTCP(t, realityAddress, 5*time.Second)
	agentRuntime := agentpkg.New(runtimecfg.Config{NodeID: "agent-reality"}, slog.Default())
	if err := agentRuntime.ApplyConfig(controller.AgentConfig{Agent: model.Agent{ID: "agent-reality", AllowedCIDRs: []string{"127.0.0.0/8"}}, GrantIDs: []string{grant.ID}}); err != nil {
		t.Fatal(err)
	}
	agentLink := controller.AgentLinkConfig{Link: link, GatewayID: "gateway-reality", TunnelToken: "tunnel-secret", RealityPublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), RealityName: "example.com"}
	go func() {
		for ctx.Err() == nil {
			if err := agentRuntime.RunLink(ctx, agentLink); err != nil {
				t.Logf("agent REALITY link retry: %v", err)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	waitFor(t, 20*time.Second, func() bool {
		lease, err := gatewayRuntime.pool.Acquire("agent-reality")
		if err == nil {
			lease.Release()
			return true
		}
		return false
	})
	tcpTarget := startTCPEcho(t)
	defer tcpTarget.Close()
	conn := dialSOCKS(t, socksAddress, grant.SOCKSUsername, grant.SOCKSPassword, 1, tcpTarget.Addr().String())
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	message := []byte("tcp-over-reality")
	if _, err := conn.Write(message); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(message))
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reply, message) {
		t.Fatalf("TCP reply %q", reply)
	}
	udpTarget := startUDPEcho(t)
	defer udpTarget.Close()
	control := dialSOCKS(t, socksAddress, grant.SOCKSUsername, grant.SOCKSPassword, 3, "0.0.0.0:0").(*socksTestConn)
	defer control.Close()
	udpClient, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer udpClient.Close()
	assertUDPEcho(t, udpClient, control.bound, udpTarget.LocalAddr().(*net.UDPAddr), []byte("udp-over-reality"), 8*time.Second)

	// A client with a different short ID must not reach tunnel registration.
	badRuntime := agentpkg.New(runtimecfg.Config{NodeID: "agent-reality"}, slog.Default())
	badLink := agentLink
	badLink.RealityShortID = "fedcba9876543210"
	badCtx, badCancel := context.WithTimeout(ctx, 8*time.Second)
	defer badCancel()
	if err := badRuntime.RunLink(badCtx, badLink); err == nil {
		t.Fatal("REALITY accepted a client with an invalid short ID")
	}
	if sessions := gatewayRuntime.pool.Snapshot(); len(sessions) != 1 {
		t.Fatalf("invalid REALITY client changed tunnel sessions: %d", len(sessions))
	}
}
