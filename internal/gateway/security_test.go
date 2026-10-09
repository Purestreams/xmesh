package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	agentpkg "xmesh/internal/agent"
	"xmesh/internal/controller"
	"xmesh/internal/model"
	"xmesh/internal/protocol"
	"xmesh/internal/runtimecfg"
)

func securityGateway(t *testing.T) (*Runtime, string) {
	t.Helper()
	tunnelAddress, socksAddress := freeTCPAddress(t), freeTCPAddress(t)
	link := model.Link{ID: "link", AttachmentID: "route", URL: "ws://" + tunnelAddress + "/tunnel", Priority: 10, Weight: 1, Connections: 1, MaxStreams: 8, Enabled: true}
	grant := model.Grant{ID: "grant", AttachmentID: "route", VMessUUID: "00000000-0000-4000-8000-000000000001", SOCKSUsername: "user", SOCKSPassword: "pass", Enabled: true}
	r := New(runtimecfg.Config{NodeID: "g", Gateway: runtimecfg.Gateway{TunnelListen: tunnelAddress, TunnelPath: "/tunnel", SOCKSListen: socksAddress, MaxUDPAssociations: 4, MaxUDPQueueBytes: 256 << 10}}, slog.Default())
	r.config = controller.GatewayConfig{Gateway: model.Gateway{ID: "g", Enabled: true}, Links: []controller.GatewayLinkConfig{{Link: link, AgentID: "a", TunnelToken: "token"}}, Grants: []model.Grant{grant}}
	ctx, cancel := context.WithCancel(context.Background())
	go r.runTunnelServer(ctx)
	go r.runSOCKS(ctx)
	a := agentpkg.New(runtimecfg.Config{NodeID: "a"}, slog.Default())
	if err := a.ApplyConfig(controller.AgentConfig{Agent: model.Agent{ID: "a", Enabled: true, AllowedCIDRs: []string{"127.0.0.0/8"}}, Links: []controller.AgentLinkConfig{{Link: link, GrantIDs: []string{"grant"}}}}); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			_ = a.RunLink(ctx, controller.AgentLinkConfig{Link: link, GatewayID: "g", TunnelToken: "token"})
			time.Sleep(10 * time.Millisecond)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("agent worker did not stop")
		}
	})
	waitFor(t, 5*time.Second, func() bool {
		lease, err := r.pool.Acquire("a")
		if err == nil {
			lease.Release()
			return true
		}
		return false
	})
	return r, socksAddress
}

func TestGatewayRevocationClosesEstablishedTrafficAndRejectsNewAccess(t *testing.T) {
	r, address := securityGateway(t)
	target := startTCPEcho(t)
	defer target.Close()
	conn := dialSOCKS(t, address, "user", "pass", 1, target.Addr().String())
	defer conn.Close()
	if _, err := conn.Write([]byte("before")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 6)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) { http.Error(w, "unauthorized", 401) }))
	defer server.Close()
	r.client.ControllerURL = server.URL
	if err := r.refresh(context.Background()); err == nil {
		t.Fatal("401 not returned")
	}
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("revoked TCP connection remains usable")
	}
	if _, _, ok := r.grant("user", "pass"); ok {
		t.Fatal("revoked SOCKS grant accepted")
	}
	if _, ok := r.authorizeTunnel(protocol.Message{LinkID: "link", AgentID: "a", Token: "token"}); ok {
		t.Fatal("revoked tunnel accepted")
	}
}

func TestGatewayDoesNotRestoreConfigFetchedBeforeRevocation(t *testing.T) {
	binary := os.Getenv("XMESH_TEST_XRAY")
	if binary == "" {
		t.Skip("XMESH_TEST_XRAY not set")
	}
	r, _ := securityGateway(t)
	r.local.Gateway.XrayBinary = binary
	r.local.Gateway.XrayConfigPath = filepath.Join(t.TempDir(), "xray.json")
	cached := r.config
	cached.Gateway.VMessPort = testPort(t, freeTCPAddress(t))
	cached.Gateway.VMessPath = "/proxy"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		r.invalidateAuthorization()
		_ = json.NewEncoder(w).Encode(cached)
	}))
	defer server.Close()
	r.client.ControllerURL = server.URL
	if err := r.refresh(context.Background()); err == nil {
		t.Fatal("configuration fetched before revocation was accepted")
	}
	if _, _, ok := r.grant("user", "pass"); ok {
		t.Fatal("revocation was undone by an in-flight configuration request")
	}
}

func TestGatewayStatusRejectionDuringRefreshDoesNotRestoreConfig(t *testing.T) {
	binary := os.Getenv("XMESH_TEST_XRAY")
	if binary == "" {
		t.Skip("XMESH_TEST_XRAY not set")
	}
	r, _ := securityGateway(t)
	r.local.Gateway.XrayBinary = binary
	r.local.Gateway.XrayConfigPath = filepath.Join(t.TempDir(), "xray.json")
	candidate := r.config
	candidate.Revision++
	candidate.Gateway.VMessPort = testPort(t, freeTCPAddress(t))
	candidate.Gateway.VMessPath = "/proxy"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(candidate)
	}))
	defer server.Close()
	r.client.ControllerURL = server.URL
	r.xrayReady.Store(true)
	if err := r.refresh(context.Background()); err == nil {
		t.Fatal("status rejection was ignored while installing configuration")
	}
	if _, _, ok := r.grant("user", "pass"); ok {
		t.Fatal("status rejection was undone by configuration refresh")
	}
}

func TestGatewayDisabledConfigBypassesXrayValidationAndClosesSessions(t *testing.T) {
	r, _ := securityGateway(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(w).Encode(controller.GatewayConfig{Revision: 2, Gateway: model.Gateway{ID: "g"}})
	}))
	defer server.Close()
	r.client.ControllerURL = server.URL
	if err := r.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.config.Gateway.Enabled || len(r.config.Grants) != 0 {
		t.Fatal("disabled configuration retained grants")
	}
	waitFor(t, time.Second, func() bool {
		for _, session := range r.pool.Snapshot() {
			if !session.SMux.IsClosed() {
				return false
			}
		}
		return true
	})
}

func TestGatewayCredentialRotationReleasesEstablishedStream(t *testing.T) {
	r, address := securityGateway(t)
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := target.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	conn := dialSOCKS(t, address, "user", "pass", 1, target.Addr().String())
	defer conn.Close()
	var passive net.Conn
	select {
	case passive = <-accepted:
		defer passive.Close()
	case <-time.After(time.Second):
		t.Fatal("target was not connected")
	}
	candidate := r.config
	candidate.Grants = append([]model.Grant(nil), candidate.Grants...)
	candidate.Grants[0].SOCKSPassword = "rotated-password"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(w).Encode(candidate)
	}))
	defer server.Close()
	r.client.ControllerURL = server.URL
	if err := r.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("rotated connection remains open")
	}
	waitFor(t, time.Second, func() bool {
		if r.tcpConnections.Load() != 0 {
			return false
		}
		for _, session := range r.pool.Snapshot() {
			if session.ActiveStreams != 0 {
				return false
			}
		}
		return true
	})
}

func TestGatewayExpiredAuthorizationRejectsBothProtocols(t *testing.T) {
	r, address := securityGateway(t)
	target := startTCPEcho(t)
	defer target.Close()
	conn := dialSOCKS(t, address, "user", "pass", 1, target.Addr().String())
	defer conn.Close()
	r.mu.Lock()
	r.authorizationUntil = time.Now().Add(-time.Second)
	r.mu.Unlock()
	if _, _, ok := r.grant("user", "pass"); ok {
		t.Fatal("expired SOCKS grant accepted")
	}
	if _, ok := r.authorizeTunnel(protocol.Message{LinkID: "link", AgentID: "a", Token: "token"}); ok {
		t.Fatal("expired tunnel accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.watchAuthorization(ctx)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("expired established connection remains usable")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("authorization watcher did not close established traffic")
	}
}

func TestSOCKSUDPRejectsWrongSourceBeforePinningAssociation(t *testing.T) {
	_, address := securityGateway(t)
	legitimate, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer legitimate.Close()
	control := dialSOCKS(t, address, "user", "pass", 3, legitimate.LocalAddr().String())
	defer control.Close()
	relayAddress := control.(*socksTestConn).bound
	attacker, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer attacker.Close()
	target := startUDPEcho(t)
	defer target.Close()
	packet, err := encodeSOCKSUDPForTest(target.LocalAddr().(*net.UDPAddr), []byte("injection"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attacker.WriteToUDP(packet, relayAddress); err != nil {
		t.Fatal(err)
	}
	_ = attacker.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := attacker.ReadFromUDP(make([]byte, 1024)); err == nil {
		t.Fatal("unauthenticated UDP injection succeeded")
	}
	assertUDPEcho(t, legitimate, relayAddress, target.LocalAddr().(*net.UDPAddr), []byte("legitimate"), 3*time.Second)
}

func TestUDPUnspecifiedPortUsesAuthenticatedSocketOwner(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	owner, ok := udpControlOwner(server)
	if !ok {
		t.Fatal("cannot determine local authenticated socket owner")
	}
	source, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if !udpSourceOwned(source.LocalAddr().(*net.UDPAddr), owner) {
		t.Fatal("same-owner UDP socket rejected")
	}
	// On Linux owner+1 can be a thread sharing this process's descriptors.
	if udpSourceOwned(source.LocalAddr().(*net.UDPAddr), uint32(os.Getppid())) {
		t.Fatal("another socket owner accepted")
	}
}

func TestUDPUnspecifiedPortRejectsAnotherProcess(t *testing.T) {
	_, address := securityGateway(t)
	control := dialSOCKS(t, address, "user", "pass", 3, "0.0.0.0:0")
	defer control.Close()
	target := startUDPEcho(t)
	defer target.Close()
	packet, err := encodeSOCKSUDPForTest(target.LocalAddr().(*net.UDPAddr), []byte("other-process"))
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestUDPCrossProcessHelper$")
	cmd.Env = append(os.Environ(), "XMESH_UDP_SECURITY_HELPER=1", "XMESH_UDP_SECURITY_RELAY="+control.(*socksTestConn).bound.String(), "XMESH_UDP_SECURITY_PACKET="+base64.StdEncoding.EncodeToString(packet))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("another process accessed the UDP relay: %v\n%s", err, output)
	}
	legitimate, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer legitimate.Close()
	assertUDPEcho(t, legitimate, control.(*socksTestConn).bound, target.LocalAddr().(*net.UDPAddr), []byte("authenticated-process"), 3*time.Second)
	// Pin a legitimate source, then let a different process reuse its port.
	// Keeping only a first-packet ownership check would accept this traffic.
	sourceAddress := legitimate.LocalAddr().String()
	_ = legitimate.Close()
	cmd = exec.Command(executable, "-test.run=^TestUDPCrossProcessHelper$")
	cmd.Env = append(os.Environ(), "XMESH_UDP_SECURITY_HELPER=1", "XMESH_UDP_SECURITY_RELAY="+control.(*socksTestConn).bound.String(), "XMESH_UDP_SECURITY_PACKET="+base64.StdEncoding.EncodeToString(packet), "XMESH_UDP_SECURITY_SOURCE="+sourceAddress)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("another process reused the authorized UDP port: %v\n%s", err, output)
	}
}

func TestUDPCrossProcessHelper(t *testing.T) {
	if os.Getenv("XMESH_UDP_SECURITY_HELPER") != "1" {
		t.Skip("subprocess fixture")
	}
	relay, err := net.ResolveUDPAddr("udp", os.Getenv("XMESH_UDP_SECURITY_RELAY"))
	if err != nil {
		t.Fatal(err)
	}
	packet, err := base64.StdEncoding.DecodeString(os.Getenv("XMESH_UDP_SECURITY_PACKET"))
	if err != nil {
		t.Fatal(err)
	}
	sourceAddress := &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}
	if value := os.Getenv("XMESH_UDP_SECURITY_SOURCE"); value != "" {
		sourceAddress, err = net.ResolveUDPAddr("udp", value)
		if err != nil {
			t.Fatal(err)
		}
	}
	source, err := net.ListenUDP("udp", sourceAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.WriteToUDP(packet, relay); err != nil {
		t.Fatal(err)
	}
	_ = source.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, _, err := source.ReadFromUDP(make([]byte, 1024)); err == nil {
		t.Fatal("unauthenticated relay traffic succeeded")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatal(err)
	}
}

func TestGatewayDisabledStopsRunningXray(t *testing.T) {
	binary := os.Getenv("XMESH_TEST_XRAY")
	if binary == "" {
		t.Skip("XMESH_TEST_XRAY not set")
	}
	r, _ := securityGateway(t)
	r.local.Gateway.XrayBinary = binary
	r.local.Gateway.XrayConfigPath = filepath.Join(t.TempDir(), "xray.json")
	r.mu.Lock()
	r.config.Gateway.VMessPort = testPort(t, freeTCPAddress(t))
	r.config.Gateway.VMessPath = "/proxy"
	r.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.xrayLoop(ctx) }()
	r.xrayApply <- struct{}{}
	waitFor(t, 5*time.Second, r.xrayReady.Load)
	r.invalidateAuthorization()
	waitFor(t, 2*time.Second, func() bool { return !r.xrayReady.Load() })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Xray did not stop")
	}
}

func TestGatewayRevocationStopsXrayDuringBlockedReport(t *testing.T) {
	binary := os.Getenv("XMESH_TEST_XRAY")
	if binary == "" {
		t.Skip("XMESH_TEST_XRAY not set")
	}
	r, _ := securityGateway(t)
	r.local.Gateway.XrayBinary = binary
	r.local.Gateway.XrayConfigPath = filepath.Join(t.TempDir(), "xray.json")
	vmessAddress := freeTCPAddress(t)
	r.mu.Lock()
	r.config.Gateway.VMessPort = testPort(t, vmessAddress)
	r.config.Gateway.VMessPath = "/proxy"
	r.mu.Unlock()
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		close(started)
		select {
		case <-release:
		case <-request.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer close(release)
	r.client.ControllerURL = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.xrayLoop(ctx) }()
	r.xrayApply <- struct{}{}
	waitFor(t, 5*time.Second, r.xrayReady.Load)
	r.xrayApply <- struct{}{} // Enter the final status report for a reload.
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Xray did not enter its final report")
	}
	r.invalidateAuthorization()
	waitFor(t, time.Second, func() bool {
		conn, err := net.DialTimeout("tcp", vmessAddress, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
		}
		return err != nil
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Xray loop did not exit")
	}
}
