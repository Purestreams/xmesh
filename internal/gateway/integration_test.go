package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	agentpkg "xmesh/internal/agent"
	"xmesh/internal/controller"
	"xmesh/internal/model"
	"xmesh/internal/protocol"
	"xmesh/internal/runtimecfg"
)

func TestSOCKSTCPAndUDPOverTunnel(t *testing.T) {
	tunnelAddress := freeTCPAddress(t)
	socksAddress := freeTCPAddress(t)
	link := model.Link{ID: "link-1", AttachmentID: "node-1", URL: "ws://" + tunnelAddress + "/tunnel", TLSVerify: false, Priority: 10, Weight: 1, Connections: 1, MaxStreams: 16, Enabled: true}
	grant := model.Grant{ID: "grant-1", AttachmentID: "node-1", SOCKSUsername: "grant-1", SOCKSPassword: "secret-password", Enabled: true}
	gatewayRuntime := New(runtimecfg.Config{NodeID: "gateway-1", Gateway: runtimecfg.Gateway{SOCKSListen: socksAddress, TunnelListen: tunnelAddress, TunnelPath: "/tunnel", MaxUDPAssociations: 8, UDPIdleTimeout: runtimecfg.Duration(5 * time.Second)}}, slog.Default())
	gatewayRuntime.config = controller.GatewayConfig{Gateway: model.Gateway{ID: "gateway-1"}, Links: []controller.GatewayLinkConfig{{Link: link, AgentID: "agent-1", TunnelToken: "tunnel-secret"}}, Grants: []model.Grant{grant}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 2)
	go func() { errCh <- gatewayRuntime.runTunnelServer(ctx) }()
	go func() { errCh <- gatewayRuntime.runSOCKS(ctx) }()
	agentRuntime := agentpkg.New(runtimecfg.Config{NodeID: "agent-1"}, slog.Default())
	if err := agentRuntime.ApplyConfig(controller.AgentConfig{Agent: model.Agent{ID: "agent-1", AllowedCIDRs: []string{"127.0.0.0/8"}}, GrantIDs: []string{"grant-1"}}); err != nil {
		t.Fatal(err)
	}
	go func() {
		for ctx.Err() == nil {
			if err := agentRuntime.RunLink(ctx, controller.AgentLinkConfig{Link: link, GatewayID: "gateway-1", TunnelToken: "tunnel-secret"}); err != nil {
				t.Logf("agent link retry: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	waitFor(t, 5*time.Second, func() bool {
		lease, err := gatewayRuntime.pool.Acquire("agent-1")
		if err == nil {
			lease.Release()
			return true
		}
		return false
	})

	tcpTarget := startTCPEcho(t)
	defer tcpTarget.Close()
	conn := dialSOCKS(t, socksAddress, grant.SOCKSUsername, grant.SOCKSPassword, 1, tcpTarget.Addr().String())
	message := []byte("tcp-through-xmesh")
	if _, err := conn.Write(message); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(message))
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != string(message) {
		t.Fatalf("TCP reply %q", reply)
	}
	conn.Close()

	udpTarget := startUDPEcho(t)
	defer udpTarget.Close()
	secondUDPTarget := startUDPEcho(t)
	defer secondUDPTarget.Close()
	control := dialSOCKS(t, socksAddress, grant.SOCKSUsername, grant.SOCKSPassword, 3, "0.0.0.0:0")
	defer control.Close()
	// dialSOCKS exposes the bound relay through the helper's wrapped connection.
	bound := control.(*socksTestConn).bound
	udpClient, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer udpClient.Close()
	target := udpTarget.LocalAddr().(*net.UDPAddr)
	secondTarget := secondUDPTarget.LocalAddr().(*net.UDPAddr)
	for _, payload := range [][]byte{
		[]byte("udp-through-xmesh"),
		bytes.Repeat([]byte{0x5a}, 8_000),
		bytes.Repeat([]byte{0x6b}, 32_000),
	} {
		assertUDPEcho(t, udpClient, bound, target, payload, 5*time.Second)
	}
	assertUDPEcho(t, udpClient, bound, secondTarget, []byte("second-target-same-association"), 5*time.Second)
}

// These tiny test hooks keep the integration test in control of the controller-free runtime.
type socksTestConn struct {
	net.Conn
	bound *net.UDPAddr
}

func dialSOCKS(t *testing.T, address, user, password string, command byte, target string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	_, _ = conn.Write([]byte{5, 1, 2})
	reply := make([]byte, 2)
	if _, err := io.ReadFull(reader, reply); err != nil || reply[1] != 2 {
		t.Fatalf("method reply %v err=%v", reply, err)
	}
	auth := []byte{1, byte(len(user))}
	auth = append(auth, user...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, password...)
	_, _ = conn.Write(auth)
	if _, err := io.ReadFull(reader, reply); err != nil || reply[1] != 0 {
		t.Fatalf("auth reply %v err=%v", reply, err)
	}
	host, portText, _ := net.SplitHostPort(target)
	port, _ := net.LookupPort("tcp", portText)
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
	bound := &net.UDPAddr{IP: net.ParseIP(boundTarget.Host), Port: boundTarget.Port}
	return &socksTestConn{Conn: conn, bound: bound}
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}
func startTCPEcho(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	return listener
}
func startUDPEcho(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, addr, err := conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			if _, err := conn.WriteToUDP(buffer[:n], addr); err != nil {
				t.Logf("UDP echo write %d bytes: %v", n, err)
				return
			}
		}
	}()
	return conn
}
func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met")
}
func encodeSOCKSUDPForTest(target *net.UDPAddr, payload []byte) ([]byte, error) {
	return encodeSOCKSUDP(protocol.Datagram{Host: target.IP.String(), Port: target.Port, Payload: payload})
}
func decodeSOCKSUDPForTest(packet []byte) ([]byte, error) {
	d, err := decodeSOCKSUDP(packet)
	return d.Payload, err
}

func assertUDPEcho(t *testing.T, client *net.UDPConn, relay, target *net.UDPAddr, want []byte, timeout time.Duration) {
	t.Helper()
	packet, err := encodeSOCKSUDPForTest(target, want)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(timeout)
	buffer := make([]byte, 65535)
	for time.Now().Before(deadline) {
		if _, err := client.WriteToUDP(packet, relay); err != nil {
			t.Fatal(err)
		}
		attemptDeadline := time.Now().Add(500 * time.Millisecond)
		if attemptDeadline.After(deadline) {
			attemptDeadline = deadline
		}
		_ = client.SetReadDeadline(attemptDeadline)
		for {
			n, _, err := client.ReadFromUDP(buffer)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					break
				}
				t.Fatalf("read %d-byte UDP echo: %v", len(want), err)
			}
			got, err := decodeSOCKSUDPForTest(buffer[:n])
			if err == nil && bytes.Equal(got, want) {
				return
			}
		}
	}
	t.Fatalf("read %d-byte UDP echo: timed out after %s", len(want), timeout)
}
