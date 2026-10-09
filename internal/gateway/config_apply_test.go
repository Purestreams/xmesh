package gateway

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentpkg "xmesh/internal/agent"
	"xmesh/internal/controller"
	"xmesh/internal/model"
	"xmesh/internal/runtimecfg"
)

func TestGatewayMetadataDoesNotAcknowledgePendingXrayPayload(t *testing.T) {
	r := New(runtimecfg.Config{NodeID: "g", Gateway: runtimecfg.Gateway{SOCKSListen: "127.0.0.1:18080"}}, slog.Default())
	r.config = controller.GatewayConfig{Revision: 2, Gateway: model.Gateway{ID: "g", Enabled: true, VMessPort: 8081, VMessPath: "/proxy"}}
	running := r.config
	running.Revision, running.Gateway.VMessPort = 1, 8080
	payload, err := r.xrayPayload(running)
	if err != nil {
		t.Fatal(err)
	}
	r.xrayRunningPayload = string(payload)
	r.xrayStatsConfig = running
	r.xrayReady.Store(true)
	r.xrayAppliedRevision.Store(1)
	next := r.config
	next.Revision, next.Gateway.Name = 3, "renamed"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) { _ = json.NewEncoder(w).Encode(next) }))
	defer server.Close()
	r.client.ControllerURL = server.URL
	if err := r.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.xrayAppliedRevision.Load() != 1 || r.xrayStatsConfig.Revision != 1 {
		t.Fatal("metadata update acknowledged a payload the running process has not applied")
	}
	select {
	case <-r.xrayApply:
		t.Fatal("metadata update requested another Xray restart")
	default:
	}
}

func TestGatewayAppliesChangedPayloadAtSameRevision(t *testing.T) {
	binary := os.Getenv("XMESH_TEST_XRAY")
	if binary == "" {
		t.Skip("XMESH_TEST_XRAY not set")
	}
	r := New(runtimecfg.Config{NodeID: "g", Gateway: runtimecfg.Gateway{SOCKSListen: "127.0.0.1:18080", XrayBinary: binary, XrayConfigPath: filepath.Join(t.TempDir(), "gateway.json")}}, slog.Default())
	r.config = controller.GatewayConfig{Revision: 1, Gateway: model.Gateway{ID: "g", Enabled: true, VMessPort: 8080, VMessPath: "/proxy"}}
	next := r.config
	next.Grants = []model.Grant{{ID: "new", AttachmentID: "route", Enabled: true, VMessUUID: "00000000-0000-4000-8000-000000000001", SOCKSUsername: "new", SOCKSPassword: "password"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) { _ = json.NewEncoder(w).Encode(next) }))
	defer server.Close()
	r.client.ControllerURL = server.URL
	if err := r.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.xrayApply:
	default:
		t.Fatal("same-revision payload change was not applied to Xray")
	}
}

func TestHotConfigKeepsManagedTunnelsAndEstablishedVMessTraffic(t *testing.T) {
	t.Run("websocket", func(t *testing.T) { testHotConfigTraffic(t, false) })
	t.Run("reality", func(t *testing.T) { testHotConfigTraffic(t, true) })
}

func testHotConfigTraffic(t *testing.T, useReality bool) {
	binary := os.Getenv("XMESH_TEST_XRAY")
	if binary == "" {
		t.Skip("XMESH_TEST_XRAY not set")
	}
	tunnel, socks, vmess, clientSOCKS := freeTCPAddress(t), freeTCPAddress(t), freeTCPAddress(t), freeTCPUDPAddress(t)
	link := model.Link{ID: "link", AttachmentID: "route", URL: "ws://" + tunnel + "/tunnel", Enabled: true, Connections: 1, Priority: 10, Weight: 1, MaxStreams: 16}
	grant := model.Grant{ID: "grant", AttachmentID: "route", Enabled: true, VMessUUID: "00000000-0000-4000-8000-000000000001", SOCKSUsername: "user", SOCKSPassword: "password"}
	gateway := model.Gateway{ID: "g", Enabled: true, VMessPort: testPort(t, vmess), VMessPath: "/proxy"}
	var realityAddress, publicKey string
	if useReality {
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		target.Config.ErrorLog = log.New(io.Discard, "", 0)
		target.StartTLS()
		defer target.Close()
		realityAddress = freeTCPAddress(t)
		link.URL, link.RealityUUID, link.RealityShortID = "reality://"+realityAddress+"/tunnel", "00000000-0000-4000-8000-000000000003", "0123456789abcdef"
		gateway.RealityTarget, gateway.RealityName = target.Listener.Addr().String(), "example.com"
		gateway.RealityPrivateKey = base64.RawURLEncoding.EncodeToString(key.Bytes())
		publicKey = base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	}
	var configMu sync.Mutex
	agentConfig := controller.AgentConfig{Revision: 1, Agent: model.Agent{ID: "a", Enabled: true, AllowedCIDRs: []string{"127.0.0.0/8"}}, Links: []controller.AgentLinkConfig{{Link: link, GatewayID: "g", TunnelToken: "token", GrantIDs: []string{grant.ID}, RealityPublicKey: publicKey, RealityName: gateway.RealityName}}}
	gatewayConfig := controller.GatewayConfig{Revision: 1, Gateway: gateway, Links: []controller.GatewayLinkConfig{{Link: link, AgentID: "a", TunnelToken: "token"}}, Grants: []model.Grant{grant}}
	var agentApplied atomic.Uint64
	var agentReport atomic.Pointer[controller.StatusReport]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			var report controller.StatusReport
			if err := json.NewDecoder(request.Body).Decode(&report); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if report.Status.NodeID == "a" {
				agentApplied.Store(report.Status.AppliedVersion)
				agentReport.Store(&report)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		configMu.Lock()
		defer configMu.Unlock()
		if request.Header.Get("Authorization") == "Bearer agent" {
			_ = json.NewEncoder(w).Encode(agentConfig)
		} else {
			_ = json.NewEncoder(w).Encode(gatewayConfig)
		}
	}))
	defer server.Close()
	r := New(runtimecfg.Config{NodeID: "g", ControllerURL: server.URL, Credential: "gateway", Gateway: runtimecfg.Gateway{SOCKSListen: socks, TunnelListen: tunnel, TunnelPath: "/tunnel", RealityListen: realityAddress, XrayBinary: binary, XrayConfigPath: filepath.Join(t.TempDir(), "gateway.json"), MaxUDPAssociations: 8}}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	go r.runTunnelServer(ctx)
	go r.runSOCKS(ctx)
	xrayDone := make(chan error, 1)
	go func() { xrayDone <- r.xrayLoop(ctx) }()
	// Exercise hot updates on established tunnels, without racing the Agent's
	// first REALITY dial against the Gateway process/listener startup.
	waitFor(t, 5*time.Second, func() bool { return r.xrayReady.Load() })
	waitTCP(t, tunnel, 5*time.Second)
	a := agentpkg.New(runtimecfg.Config{NodeID: "a", ControllerURL: server.URL, Credential: "agent", PollInterval: runtimecfg.Duration(20 * time.Millisecond), StatusInterval: runtimecfg.Duration(20 * time.Millisecond)}, slog.Default())
	agentDone := make(chan error, 1)
	go func() { agentDone <- a.Run(ctx) }()
	defer func() {
		if t.Failed() {
			t.Logf("Agent status before shutdown: %+v", agentReport.Load())
		}
		cancel()
		for _, done := range []chan error{agentDone, xrayDone} {
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("runtime did not stop")
			}
		}
	}()
	waitFor(t, 5*time.Second, func() bool { return r.xrayReady.Load() && len(r.pool.Snapshot()) == 1 && agentApplied.Load() == 1 })
	old := r.pool.Snapshot()[0]
	clientConfig := map[string]any{"log": map[string]string{"loglevel": "warning"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": testPort(t, clientSOCKS), "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true, "ip": "127.0.0.1"}}}, "outbounds": []any{map[string]any{"protocol": "vmess", "settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": testPort(t, vmess), "users": []any{map[string]any{"id": grant.VMessUUID, "alterId": 0, "security": "auto"}}}}}, "streamSettings": map[string]any{"network": "ws", "security": "none", "wsSettings": map[string]any{"path": "/proxy"}}}}}
	clientJSON, _ := json.Marshal(clientConfig)
	process := startTestXray(t, binary, clientJSON)
	defer stopTestProcess(process)
	waitTCP(t, clientSOCKS, 5*time.Second)
	target := startTCPEcho(t)
	defer target.Close()
	conn := dialSOCKSNoAuth(t, clientSOCKS, 1, target.Addr().String())
	defer conn.Close()
	echo := func() {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write([]byte("continuous")); err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, 10)
		if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != "continuous" {
			t.Fatalf("existing VMess connection interrupted: %q %v", reply, err)
		}
	}
	echo()
	udpTarget := startUDPEcho(t)
	control := dialSOCKSNoAuth(t, clientSOCKS, 3, "0.0.0.0:0").(*socksTestConn)
	defer control.Close()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	assertUDP := func() {
		assertUDPEcho(t, udp, control.bound, udpTarget.LocalAddr().(*net.UDPAddr), []byte("still-live"), 3*time.Second)
	}
	assertUDP()
	udpCount := r.udpAssociations.Load()
	if udpCount != 1 {
		t.Fatalf("expected live UDP association, got %d", udpCount)
	}
	configMu.Lock()
	agentConfig.Revision, gatewayConfig.Revision = 2, 2
	agentConfig.Agent.Name, gatewayConfig.Gateway.Name = "renamed agent", "renamed gateway"
	agentConfig.Links[0].Priority, gatewayConfig.Links[0].Priority = 30, 30
	agentConfig.Links[0].Weight, gatewayConfig.Links[0].Weight = 7, 7
	agentConfig.Links[0].MaxStreams, gatewayConfig.Links[0].MaxStreams = 4, 4
	configMu.Unlock()
	if err := r.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return agentApplied.Load() == 2 && r.xrayAppliedRevision.Load() == 2 })
	echo()
	assertUDP()
	checkOld := func(want int) {
		t.Helper()
		sessions := r.pool.Snapshot()
		if len(sessions) != want {
			t.Fatalf("got %d sessions, want %d", len(sessions), want)
		}
		for _, session := range sessions {
			if session.ID == old.ID {
				if session.SMux != old.SMux || session.Generation != old.Generation || session.SMux.IsClosed() || session.Priority != 30 || session.Weight != 7 || session.MaxStreams != 4 {
					t.Fatal("original session was replaced or did not receive scheduling policy")
				}
				return
			}
		}
		t.Fatal("original managed session disappeared")
	}
	checkOld(1)
	// Duplicate notifications can remain after a newer snapshot was used at
	// startup. They must not restart the process or interrupt live traffic.
	r.xrayApply <- struct{}{}
	waitFor(t, time.Second, func() bool { return len(r.xrayApply) == 0 })
	time.Sleep(100 * time.Millisecond) // Let the consumed notification finish processing.
	echo()
	assertUDP()
	configMu.Lock()
	agentConfig.Revision = 3
	agentConfig.Links[0].Connections = 2
	configMu.Unlock()
	waitFor(t, 3*time.Second, func() bool { return agentApplied.Load() == 3 && len(r.pool.Snapshot()) == 2 })
	checkOld(2)
	echo()
	assertUDP()
	configMu.Lock()
	agentConfig.Revision = 4
	agentConfig.Links[0].Connections = 1
	configMu.Unlock()
	waitFor(t, 3*time.Second, func() bool { return agentApplied.Load() == 4 && len(r.pool.Snapshot()) == 1 })
	checkOld(1)
	echo()
	assertUDP()
	if r.udpAssociations.Load() != udpCount {
		t.Fatal("hot update replaced the UDP association")
	}
	// A Controller capability/credential change may alter the snapshot without
	// a new revision. The running Xray must also pick up the actual payload.
	configMu.Lock()
	gatewayConfig.Grants[0].SOCKSPassword = "rotated-password"
	candidate := gatewayConfig
	configMu.Unlock()
	nextPayload, err := r.xrayPayload(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		r.mu.RLock()
		applied := r.xrayReady.Load() && r.xrayRunningPayload == string(nextPayload)
		r.mu.RUnlock()
		return applied && len(r.pool.Snapshot()) == 1
	})
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("rotated credential retained its established connection")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("rotated connection did not close promptly")
	}
	conn = dialSOCKSNoAuth(t, clientSOCKS, 1, target.Addr().String())
	defer conn.Close()
	echo()
}
