package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	agentpkg "xmesh/internal/agent"
	"xmesh/internal/controller"
	"xmesh/internal/model"
	"xmesh/internal/runtimecfg"
)

func TestFullVMessTCPAndUDPChain(t *testing.T) {
	xray := os.Getenv("XMESH_TEST_XRAY")
	if xray == "" {
		t.Skip("XMESH_TEST_XRAY not set")
	}
	tunnelAddress, socksAddress, vmessAddress, clientSOCKS := freeTCPAddress(t), freeTCPAddress(t), freeTCPAddress(t), freeTCPUDPAddress(t)
	uuid := "00000000-0000-4000-8000-000000000001"
	link := model.Link{ID: "link-vmess", AttachmentID: "node-vmess", URL: "ws://" + tunnelAddress + "/tunnel", Priority: 10, Weight: 1, Connections: 1, MaxStreams: 16, Enabled: true}
	grant := model.Grant{ID: "grant-vmess", AttachmentID: "node-vmess", VMessUUID: uuid, SOCKSUsername: "grant-vmess", SOCKSPassword: "secret-password", Enabled: true}
	gatewayRuntime := New(runtimecfg.Config{NodeID: "gateway-vmess", Gateway: runtimecfg.Gateway{SOCKSListen: socksAddress, TunnelListen: tunnelAddress, TunnelPath: "/tunnel", MaxUDPAssociations: 8, UDPIdleTimeout: runtimecfg.Duration(5 * time.Second)}}, slog.Default())
	gatewayConfig := controller.GatewayConfig{Gateway: model.Gateway{ID: "gateway-vmess", VMessPort: testPort(t, vmessAddress), VMessPath: "/proxy"}, Links: []controller.GatewayLinkConfig{{Link: link, AgentID: "agent-vmess", TunnelToken: "tunnel-secret"}}, Grants: []model.Grant{grant}}
	gatewayRuntime.config = gatewayConfig
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gatewayRuntime.runTunnelServer(ctx)
	go gatewayRuntime.runSOCKS(ctx)
	agentRuntime := agentpkg.New(runtimecfg.Config{NodeID: "agent-vmess"}, slog.Default())
	if err := agentRuntime.ApplyConfig(controller.AgentConfig{Agent: model.Agent{ID: "agent-vmess", AllowedCIDRs: []string{"127.0.0.0/8"}}, GrantIDs: []string{grant.ID}}); err != nil {
		t.Fatal(err)
	}
	go func() {
		for ctx.Err() == nil {
			_ = agentRuntime.RunLink(ctx, controller.AgentLinkConfig{Link: link, GatewayID: "gateway-vmess", TunnelToken: "tunnel-secret"})
			time.Sleep(20 * time.Millisecond)
		}
	}()
	waitFor(t, 5*time.Second, func() bool {
		lease, err := gatewayRuntime.pool.Acquire("agent-vmess")
		if err == nil {
			lease.Release()
			return true
		}
		return false
	})

	serverJSON, err := buildXrayConfig(gatewayConfig, socksAddress)
	if err != nil {
		t.Fatal(err)
	}
	serverJSON = bytes.Replace(serverJSON, []byte(`"warning"`), []byte(`"debug"`), 1)
	serverProcess := startTestXray(t, xray, serverJSON)
	defer stopTestProcess(serverProcess)
	waitTCP(t, vmessAddress, 5*time.Second)
	clientHost, clientPortText, _ := net.SplitHostPort(clientSOCKS)
	clientPort, _ := strconv.Atoi(clientPortText)
	vmessHost, vmessPortText, _ := net.SplitHostPort(vmessAddress)
	vmessPort, _ := strconv.Atoi(vmessPortText)
	clientConfig := map[string]any{"log": map[string]string{"loglevel": "warning"}, "inbounds": []any{map[string]any{"listen": clientHost, "port": clientPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true, "ip": clientHost}}}, "outbounds": []any{map[string]any{"protocol": "vmess", "settings": map[string]any{"vnext": []any{map[string]any{"address": vmessHost, "port": vmessPort, "users": []any{map[string]any{"id": uuid, "alterId": 0, "security": "auto"}}}}}, "streamSettings": map[string]any{"network": "ws", "security": "none", "wsSettings": map[string]any{"path": "/proxy"}}}}}
	clientConfig["log"] = map[string]string{"loglevel": "debug"}
	clientJSON, _ := json.Marshal(clientConfig)
	clientProcess := startTestXray(t, xray, clientJSON)
	defer stopTestProcess(clientProcess)
	waitTCP(t, clientSOCKS, 5*time.Second)

	tcpTarget := startTCPEcho(t)
	defer tcpTarget.Close()
	conn := dialSOCKSNoAuth(t, clientSOCKS, 1, tcpTarget.Addr().String())
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	message := []byte("vmess-tcp")
	_, _ = conn.Write(message)
	reply := make([]byte, len(message))
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reply, message) {
		t.Fatalf("TCP reply %q", reply)
	}
	conn.Close()
	udpTarget := startUDPEcho(t)
	defer udpTarget.Close()
	control := dialSOCKSNoAuth(t, clientSOCKS, 3, "0.0.0.0:0").(*socksTestConn)
	udpClient, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer udpClient.Close()
	udpAddress := udpTarget.LocalAddr().(*net.UDPAddr)
	assertUDPEcho(t, udpClient, control.bound, udpAddress, []byte("vmess-udp"), 8*time.Second)
	control.Close()
}

func dialSOCKSNoAuth(t *testing.T, address string, command byte, target string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	_, _ = conn.Write([]byte{5, 1, 0})
	reply := make([]byte, 2)
	if _, err := io.ReadFull(reader, reply); err != nil || reply[1] != 0 {
		t.Fatalf("method reply %v err=%v", reply, err)
	}
	host, portText, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portText)
	ip := net.ParseIP(host).To4()
	request := []byte{5, command, 0, 1}
	request = append(request, ip...)
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	request = append(request, p[:]...)
	_, _ = conn.Write(request)
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil || header[1] != 0 {
		t.Fatalf("request reply %v err=%v", header, err)
	}
	boundTarget, _, err := readSOCKSAddress(reader, header[3])
	if err != nil {
		t.Fatal(err)
	}
	return &socksTestConn{Conn: conn, bound: &net.UDPAddr{IP: net.ParseIP(boundTarget.Host), Port: boundTarget.Port}}
}

func startTestXray(t *testing.T, binary string, config []byte) *exec.Cmd {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, config, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "run", "-config", path)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("Xray output: %s", output.String())
		}
	})
	return cmd
}
func stopTestProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
func waitTCP(t *testing.T, address string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, func() bool {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		return false
	})
}
func testPort(t *testing.T, address string) int {
	t.Helper()
	_, value, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func freeTCPUDPAddress(t *testing.T) string {
	t.Helper()
	for range 20 {
		tcp, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := tcp.Addr().(*net.TCPAddr).Port
		udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
		if err == nil {
			udp.Close()
			tcp.Close()
			return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		}
		tcp.Close()
	}
	t.Fatal("could not reserve a port available to both TCP and UDP")
	return ""
}
