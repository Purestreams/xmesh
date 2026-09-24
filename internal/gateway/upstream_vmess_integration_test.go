package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"xmesh/internal/controller"
	"xmesh/internal/model"
	"xmesh/internal/runtimecfg"
)

func TestFullVMessUpstreamTCPAndUDP(t *testing.T) {
	runVMessUpstream(t, false, "ws")
}

func TestFullVMessUpstreamTLSTCPAndUDP(t *testing.T) {
	runVMessUpstream(t, true, "ws")
}

func TestFullVMessUpstreamRawTCPAndUDP(t *testing.T) {
	runVMessUpstream(t, false, "tcp")
}

func runVMessUpstream(t *testing.T, withTLS bool, network string) {
	xray := os.Getenv("XMESH_TEST_XRAY")
	if xray == "" {
		t.Skip("XMESH_TEST_XRAY not set")
	}
	upstreamAddress, gatewayAddress := freeTCPAddress(t), freeTCPAddress(t)
	clientSOCKS, statsAddress, localSOCKS := freeTCPUDPAddress(t), freeTCPAddress(t), freeTCPAddress(t)
	upstreamUUID := "00000000-0000-4000-8000-000000000021"
	clientUUID := "00000000-0000-4000-8000-000000000022"
	upstreamHost, upstreamPortText, _ := net.SplitHostPort(upstreamAddress)
	upstreamPort, _ := strconv.Atoi(upstreamPortText)
	upstreamStream := map[string]any{"network": network, "security": "none"}
	if network == "ws" {
		upstreamStream["wsSettings"] = map[string]any{"path": "/upstream"}
	}
	if withTLS {
		certificatePath, keyPath := writeTestTLSCert(t)
		upstreamStream["security"] = "tls"
		upstreamStream["tlsSettings"] = map[string]any{"certificates": []any{map[string]any{"certificateFile": certificatePath, "keyFile": keyPath}}}
		t.Setenv("SSL_CERT_FILE", certificatePath)
	}
	upstreamConfig := map[string]any{
		"log":       map[string]string{"loglevel": "warning"},
		"inbounds":  []any{map[string]any{"listen": upstreamHost, "port": upstreamPort, "protocol": "vmess", "settings": map[string]any{"clients": []any{map[string]any{"id": upstreamUUID, "alterId": 0}}}, "streamSettings": upstreamStream}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
	}
	upstreamJSON, _ := json.Marshal(upstreamConfig)
	upstreamProcess := startTestXray(t, xray, upstreamJSON)
	defer stopTestProcess(upstreamProcess)
	waitTCP(t, upstreamAddress, 5*time.Second)
	grant := model.Grant{ID: "grant-upstream", AttachmentID: "route-upstream", VMessUUID: clientUUID, Enabled: true}
	config := controller.GatewayConfig{Gateway: model.Gateway{VMessPort: testPort(t, gatewayAddress), VMessPath: "/proxy"}, Grants: []model.Grant{grant}, Upstreams: []controller.GatewayUpstreamConfig{{AttachmentID: grant.AttachmentID, UpstreamID: "external-1", Endpoint: model.VMessEndpoint{Address: upstreamHost, Port: upstreamPort, UUID: upstreamUUID, Network: network, Path: "/upstream", TLS: withTLS, ServerName: "localhost"}}}}
	gatewayJSON, err := buildXrayConfigWithStats(config, localSOCKS, "", "", statsAddress)
	if err != nil {
		t.Fatal(err)
	}
	gatewayProcess := startTestXray(t, xray, gatewayJSON)
	defer stopTestProcess(gatewayProcess)
	waitTCP(t, gatewayAddress, 5*time.Second)
	waitTCP(t, statsAddress, 5*time.Second)
	clientHost, clientPortText, _ := net.SplitHostPort(clientSOCKS)
	clientPort, _ := strconv.Atoi(clientPortText)
	gatewayHost, gatewayPortText, _ := net.SplitHostPort(gatewayAddress)
	gatewayPort, _ := strconv.Atoi(gatewayPortText)
	clientConfig := map[string]any{"log": map[string]string{"loglevel": "warning"}, "inbounds": []any{map[string]any{"listen": clientHost, "port": clientPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true, "ip": clientHost}}}, "outbounds": []any{map[string]any{"protocol": "vmess", "settings": map[string]any{"vnext": []any{map[string]any{"address": gatewayHost, "port": gatewayPort, "users": []any{map[string]any{"id": clientUUID, "alterId": 0, "security": "auto"}}}}}, "streamSettings": map[string]any{"network": "ws", "security": "none", "wsSettings": map[string]any{"path": "/proxy"}}}}}
	clientJSON, _ := json.Marshal(clientConfig)
	clientProcess := startTestXray(t, xray, clientJSON)
	defer stopTestProcess(clientProcess)
	waitTCP(t, clientSOCKS, 5*time.Second)

	tcpTarget := startTCPEcho(t)
	defer tcpTarget.Close()
	conn := dialSOCKSNoAuth(t, clientSOCKS, 1, tcpTarget.Addr().String())
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	message := []byte("vmess-upstream-tcp")
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
	conn.Close()
	udpTarget := startUDPEcho(t)
	defer udpTarget.Close()
	control := dialSOCKSNoAuth(t, clientSOCKS, 3, "0.0.0.0:0").(*socksTestConn)
	udpClient, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer udpClient.Close()
	assertUDPEcho(t, udpClient, control.bound, udpTarget.LocalAddr().(*net.UDPAddr), []byte("vmess-upstream-udp"), 8*time.Second)
	control.Close()
	runtime := New(runtimecfg.Config{Gateway: runtimecfg.Gateway{StatsListen: statsAddress}}, slog.Default())
	runtime.config = config
	runtime.xrayStatsConfig = config
	runtime.xrayReady.Store(true)
	runtime.collectXrayStats(context.Background())
	grants, links, upload, download := runtime.trafficSnapshot()
	if len(grants) != 1 || upload < uint64(len(message)) || download < uint64(len(message)) || links[grant.AttachmentID].UploadBytes == 0 {
		t.Fatalf("missing upstream usage: grants=%+v links=%+v upload=%d download=%d", grants, links, upload, download)
	}
}

func writeTestTLSCert(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificatePath, keyPath := filepath.Join(t.TempDir(), "server.crt"), filepath.Join(t.TempDir(), "server.key")
	if err := os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certificatePath, keyPath
}
