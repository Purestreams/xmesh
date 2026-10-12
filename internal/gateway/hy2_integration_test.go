package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apernet/quic-go/http3"

	agentpkg "xmesh/internal/agent"
	"xmesh/internal/controller"
	"xmesh/internal/hy2"
	"xmesh/internal/model"
	"xmesh/internal/runtimecfg"
)

func startHY2Client(t *testing.T, server, credential, certificate, pin, serverName string) string {
	t.Helper()
	address := freeTCPUDPAddress(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gateway.pem"), []byte(certificate), 0600); err != nil {
		t.Fatal(err)
	}
	tlsConfig := map[string]any{"sni": serverName, "pinSHA256": pin}
	if certificate != "" {
		tlsConfig["ca"] = "gateway.pem"
	}
	config := map[string]any{"server": server, "auth": credential, "tls": tlsConfig, "lazy": true, "socks5": map[string]any{"listen": address}, "quic": map[string]any{"maxIdleTimeout": "4s", "keepAlivePeriod": "2s"}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("insecure")) {
		t.Fatal("HY2 test must never enable insecure")
	}
	if err := os.WriteFile(filepath.Join(dir, "client.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Getenv("XMESH_TEST_HYSTERIA"), "client", "-c", "client.json")
	command.Dir = dir
	logFile, err := os.Create(filepath.Join(dir, "client.log"))
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopTestProcess(command)
		logFile.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logFile.Name())
			t.Logf("HY2 client: %s", data)
		}
	})
	waitTCP(t, address, 5*time.Second)
	return address
}

func hy2TCP(address, target string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(6 * time.Second))
	if _, err = conn.Write([]byte{5, 1, 0}); err != nil {
		conn.Close()
		return nil, err
	}
	var method [2]byte
	if _, err = io.ReadFull(conn, method[:]); err != nil {
		conn.Close()
		return nil, err
	}
	host, portText, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portText)
	packet := append([]byte{5, 1, 0, 1}, net.ParseIP(host).To4()...)
	packet = append(packet, byte(port>>8), byte(port))
	if _, err = conn.Write(packet); err != nil {
		conn.Close()
		return nil, err
	}
	var header [4]byte
	if _, err = io.ReadFull(conn, header[:]); err != nil {
		conn.Close()
		return nil, err
	}
	if header[1] != 0 {
		conn.Close()
		return nil, errors.New("SOCKS target rejected")
	}
	if _, _, err = readSOCKSAddress(conn, header[3]); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func TestHY2OfficialClientMultiGrantTCPUDPAndRevocation(t *testing.T) {
	xray := os.Getenv("XMESH_TEST_XRAY")
	if xray == "" || os.Getenv("XMESH_TEST_HYSTERIA") == "" {
		t.Skip("real Xray and official Hysteria binaries required")
	}
	tunnel, socks, entrance := freeTCPAddress(t), freeTCPAddress(t), freeTCPUDPAddress(t)
	certificate, key, pin, err := hy2.Generate("127.0.0.1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	grants := []model.Grant{
		{ID: "a", AttachmentID: "route-a", Enabled: true, VMessUUID: "00000000-0000-4000-8000-000000000001", HY2Auth: "independent-hy2-a", SOCKSUsername: "a", SOCKSPassword: "socks-a"},
		{ID: "b", AttachmentID: "route-b", Enabled: true, VMessUUID: "00000000-0000-4000-8000-000000000002", HY2Auth: "independent-hy2-b", SOCKSUsername: "b", SOCKSPassword: "socks-b"},
	}
	links := []controller.GatewayLinkConfig{}
	for _, id := range []string{"a", "b"} {
		links = append(links, controller.GatewayLinkConfig{Link: model.Link{ID: "link-" + id, AttachmentID: "route-" + id, URL: "ws://" + tunnel + "/tunnel", Priority: 10, Weight: 1, Connections: 1, MaxStreams: 16, Enabled: true}, AgentID: "agent-" + id, TunnelToken: "token-" + id})
	}
	config := controller.GatewayConfig{Revision: 1, Gateway: model.Gateway{ID: "gateway", Enabled: true, PublicHost: "127.0.0.1", VMessPort: testPort(t, entrance), VMessPath: "/proxy", HY2Port: testPort(t, entrance), HY2Certificate: certificate, HY2PrivateKey: key, HY2PinSHA256: pin}, Grants: grants, Links: links}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(204)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode(config)
	}))
	defer server.Close()
	r := New(runtimecfg.Config{NodeID: "gateway", ControllerURL: server.URL, Credential: "gateway", Gateway: runtimecfg.Gateway{SOCKSListen: socks, TunnelListen: tunnel, TunnelPath: "/tunnel", XrayBinary: xray, XrayConfigPath: filepath.Join(t.TempDir(), "gateway.json"), MaxUDPAssociations: 32, UDPIdleTimeout: runtimecfg.Duration(5 * time.Second)}}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	go r.runSOCKS(ctx)
	go r.runTunnelServer(ctx)
	done := make(chan error, 1)
	go func() { done <- r.xrayLoop(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Xray did not stop")
		}
	})
	waitFor(t, 5*time.Second, func() bool { return r.xrayReady.Load() })
	for index, link := range links {
		a := agentpkg.New(runtimecfg.Config{NodeID: link.AgentID}, slog.Default())
		aConfig := controller.AgentConfig{Agent: model.Agent{ID: link.AgentID, Enabled: true, AllowedCIDRs: []string{"127.0.0.0/8"}}, GrantIDs: []string{grants[index].ID}, Links: []controller.AgentLinkConfig{{Link: link.Link, GrantIDs: []string{grants[index].ID}}}}
		if err := a.ApplyConfig(aConfig); err != nil {
			t.Fatal(err)
		}
		go func() {
			for ctx.Err() == nil {
				a.RunLink(ctx, controller.AgentLinkConfig{Link: link.Link, GatewayID: "gateway", TunnelToken: link.TunnelToken})
				time.Sleep(20 * time.Millisecond)
			}
		}()
	}
	waitFor(t, 5*time.Second, func() bool { return len(r.pool.Snapshot()) == 2 })
	tcpTarget := startTCPEcho(t)
	defer tcpTarget.Close()
	udpTarget := startUDPEcho(t)
	defer udpTarget.Close()
	clients := []string{}
	for _, grant := range grants {
		client := startHY2Client(t, entrance, grant.HY2Auth, certificate, pin, "127.0.0.1")
		clients = append(clients, client)
		conn, err := hy2TCP(client, tcpTarget.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte("hy2-tcp-" + grant.ID)
		conn.Write(payload)
		reply := make([]byte, len(payload))
		_, err = io.ReadFull(conn, reply)
		conn.Close()
		if err != nil || !bytes.Equal(payload, reply) {
			t.Fatalf("HY2 TCP %s: %v", grant.ID, err)
		}
		control := dialSOCKSNoAuth(t, client, 3, "0.0.0.0:0").(*socksTestConn)
		udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			t.Fatal(err)
		}
		assertUDPEcho(t, udp, control.bound, udpTarget.LocalAddr().(*net.UDPAddr), []byte("hy2-udp-"+grant.ID), 6*time.Second)
		udp.Close()
		control.Close()
	}
	usage, _, _, _ := r.trafficSnapshot()
	if len(usage) != 2 {
		t.Fatalf("missing per-Grant usage: %+v", usage)
	}
	for _, grantUsage := range usage {
		if grantUsage.UploadBytes == 0 || len(grantUsage.Links) != 1 || grantUsage.Links[0].LinkID != "link-"+grantUsage.GrantID {
			t.Fatalf("wrong Grant/Link accounting: %+v", usage)
		}
	}
	t.Run("unauthenticated-http3-never-proxies-websites", func(t *testing.T) {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(certificate)) {
			t.Fatal("invalid public certificate")
		}
		transport := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS13}}
		defer transport.Close()
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		for _, host := range []string{"api.bilibili.com", "127.0.0.1"} {
			request, _ := http.NewRequest("GET", "https://"+entrance+"/", nil)
			request.Host = host
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != 404 || string(body) != "Not Found" {
				t.Fatalf("website masquerade enabled: status=%d body=%q err=%v", response.StatusCode, body, err)
			}
		}
	})
	for _, test := range []struct{ name, auth, cert, pin, sni string }{
		{"wrong-pin", grants[0].HY2Auth, certificate, strings.Repeat("0", 64), "127.0.0.1"},
		{"untrusted", grants[0].HY2Auth, "", pin, "127.0.0.1"},
		{"wrong-name", grants[0].HY2Auth, certificate, pin, "other.example"},
		{"wrong-auth", "invalid-credential", certificate, pin, "127.0.0.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := startHY2Client(t, entrance, test.auth, test.cert, test.pin, test.sni)
			conn, err := hy2TCP(client, tcpTarget.Addr().String())
			if err == nil {
				conn.Close()
				t.Fatal("invalid HY2 client accepted")
			}
		})
	}
	established, err := hy2TCP(clients[0], tcpTarget.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer established.Close()
	mu.Lock()
	config.Revision++
	config.Grants = config.Grants[1:]
	mu.Unlock()
	if err := r.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return r.xrayAppliedRevision.Load() == 2 && r.xrayReady.Load() })
	established.SetDeadline(time.Now().Add(time.Second))
	established.Write([]byte("revoked"))
	var reply [7]byte
	if _, err := io.ReadFull(established, reply[:]); err == nil {
		t.Fatal("revoked connection survived")
	}
	if conn, err := hy2TCP(clients[0], tcpTarget.Addr().String()); err == nil {
		conn.Close()
		t.Fatal("revoked credential accepted")
	}
	// Chrome-compatible QUIC uses a 30s idle timeout; a server restart must
	// expire the old connection before the official client reconnects.
	waitFor(t, 45*time.Second, func() bool {
		conn, err := hy2TCP(clients[1], tcpTarget.Addr().String())
		if err != nil {
			return false
		}
		conn.Close()
		return true
	})
}
