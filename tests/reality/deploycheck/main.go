package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/identity"
	"xmesh/internal/model"
)

const (
	controllerCredential = "controller-test-password"
	gatewayCredential    = "gateway-test-credential"
	agentCredential      = "agent-test-credential"
	grantUser            = "grant-test"
	grantPassword        = "grant-secret"
)

func main() {
	var err error
	if len(os.Args) >= 3 && os.Args[1] == "setup" {
		err = setup(os.Args[2])
	} else if len(os.Args) == 2 && os.Args[1] == "target" {
		err = target()
	} else if len(os.Args) == 2 && os.Args[1] == "health" {
		err = health()
	} else if len(os.Args) == 2 && os.Args[1] == "probe" {
		err = probe()
	} else {
		err = errors.New("usage: deploycheck setup DIR | target | health | probe")
	}
	if err != nil {
		log.Fatal(err)
	}
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func setup(dir string) error {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	short := make([]byte, 8)
	if _, err := rand.Read(short); err != nil {
		return err
	}
	realityUUID, err := identity.UUID()
	if err != nil {
		return err
	}
	vmessUUID, err := identity.UUID()
	if err != nil {
		return err
	}
	adminHash, err := auth.PasswordHash(controllerCredential)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	state := model.NewState()
	state.Gateways["gateway"] = model.Gateway{ID: "gateway", Name: "Gateway", PublicHost: "gateway", VMessPort: 8080, VMessPath: "/proxy", Enabled: true, CredentialHash: auth.SecretHash(gatewayCredential), DesiredVersion: 1, RealityTarget: "target:443", RealityName: "target.test", RealityPrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()), RealityPublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), CreatedAt: now}
	state.Agents["agent"] = model.Agent{ID: "agent", Name: "Agent", Enabled: true, AllowedCIDRs: []string{"0.0.0.0/0"}, CredentialHash: auth.SecretHash(agentCredential), DesiredVersion: 1, CreatedAt: now}
	state.Attachments["attachment"] = model.Attachment{ID: "attachment", GatewayID: "gateway", AgentID: "agent", Enabled: true, CreatedAt: now}
	token := auth.Derive(secret, "tunnel", "link")
	state.Links["link"] = model.Link{ID: "link", AttachmentID: "attachment", Name: "Docker REALITY", URL: "reality://gateway:8443/tunnel", RealityUUID: realityUUID, RealityShortID: hex.EncodeToString(short), TLSVerify: true, Priority: 10, Weight: 1, Connections: 2, MaxStreams: 256, Enabled: true, TunnelTokenHash: auth.SecretHash(token), CreatedAt: now}
	state.Users["user"] = model.User{ID: "user", Name: "Test user", Enabled: true, CreatedAt: now}
	state.Grants["grant"] = model.Grant{ID: "grant", UserID: "user", AttachmentID: "attachment", VMessUUID: vmessUUID, SOCKSUsername: grantUser, SOCKSPassword: grantPassword, Enabled: true, CreatedAt: now}
	if err := writeJSON(filepath.Join(dir, "controller-data", "controller-state.json"), state); err != nil {
		return err
	}
	controller := map[string]any{"listen": "0.0.0.0:8088", "public_url": "http://controller:8088", "state_path": "/var/lib/xmesh/controller-state.json", "admin_username": "admin", "admin_password_hash": adminHash, "session_secret": base64.RawURLEncoding.EncodeToString(secret)}
	if err := writeJSON(filepath.Join(dir, "controller.json"), controller); err != nil {
		return err
	}
	gateway := map[string]any{"role": "gateway", "node_id": "gateway", "controller_url": "http://controller:8088", "credential": gatewayCredential, "poll_interval": "1s", "status_interval": "1s", "gateway": map[string]any{"socks_listen": "0.0.0.0:18080", "tunnel_listen": "127.0.0.1:18081", "tunnel_path": "/tunnel", "reality_listen": "0.0.0.0:8443", "xray_binary": "/usr/local/lib/xmesh/xray", "xray_config_path": "/var/lib/xmesh/xray.json"}}
	if err := writeJSON(filepath.Join(dir, "gateway.json"), gateway); err != nil {
		return err
	}
	agent := map[string]any{"role": "agent", "node_id": "agent", "controller_url": "http://controller:8088", "credential": agentCredential, "poll_interval": "1s", "status_interval": "1s"}
	if err := writeJSON(filepath.Join(dir, "agent.json"), agent); err != nil {
		return err
	}
	client := map[string]any{"log": map[string]string{"loglevel": "warning"}, "inbounds": []any{map[string]any{"listen": "0.0.0.0", "port": 1080, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true, "ip": "0.0.0.0"}}}, "outbounds": []any{map[string]any{"protocol": "vmess", "settings": map[string]any{"vnext": []any{map[string]any{"address": "gateway", "port": 8080, "users": []any{map[string]any{"id": vmessUUID, "alterId": 0, "security": "auto"}}}}}, "streamSettings": map[string]any{"network": "ws", "security": "none", "wsSettings": map[string]any{"path": "/proxy"}}}}}
	return writeJSON(filepath.Join(dir, "client-xray.json"), client)
}

func target() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "target.test"}, DNSNames: []string{"target.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}))
	if err != nil {
		return err
	}
	// Use an explicit TLS listener so the generated certificate and TLS 1.3 are used.
	tlsListener, err := tls.Listen("tcp", ":443", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}})
	if err != nil {
		return err
	}
	go func() {
		_ = http.Serve(tlsListener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	}()
	tcpListener, err := net.Listen("tcp", ":19000")
	if err != nil {
		return err
	}
	go func() {
		for {
			conn, err := tcpListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = io.Copy(c, c) }(conn)
		}
	}()
	udpConn, err := net.ListenPacket("udp", ":19001")
	if err != nil {
		return err
	}
	log.Print("target TLS/TCP/UDP ready")
	buffer := make([]byte, 65535)
	for {
		n, addr, err := udpConn.ReadFrom(buffer)
		if err != nil {
			return err
		}
		_, _ = udpConn.WriteTo(buffer[:n], addr)
	}
}

func health() error {
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://controller:8088/healthz", nil)
		response, err := http.DefaultClient.Do(request)
		cancel()
		if err == nil && response.StatusCode == http.StatusOK {
			response.Body.Close()
			return nil
		}
		if err == nil {
			last = fmt.Errorf("status %d", response.StatusCode)
			response.Body.Close()
		} else {
			last = err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("controller did not become healthy: %w", last)
}

func probe() error {
	deadline := time.Now().Add(90 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if err := runProbe(); err == nil {
			log.Print("controller + VMess/WS + REALITY + Agent TCP/UDP: PASS")
			return nil
		} else {
			last = err
			log.Printf("waiting for multi-container route: %v", err)
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("multi-container route did not become ready: %w", last)
}

func runProbe() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://controller:8088/healthz", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("controller health: %w", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("controller health status %d", response.StatusCode)
	}
	ips, err := net.LookupIP("target")
	if err != nil {
		return err
	}
	var targetIP net.IP
	for _, ip := range ips {
		if ip.To4() != nil {
			targetIP = ip.To4()
			break
		}
	}
	if targetIP == nil {
		return errors.New("target has no IPv4 address")
	}
	tcpConn, _, err := dialSOCKS(1, targetIP, 19000)
	if err != nil {
		return fmt.Errorf("TCP SOCKS: %w", err)
	}
	defer tcpConn.Close()
	_ = tcpConn.SetDeadline(time.Now().Add(5 * time.Second))
	payload := []byte("xmesh-multi-container-tcp")
	if _, err := tcpConn.Write(payload); err != nil {
		return err
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(tcpConn, reply); err != nil {
		return err
	}
	if !bytes.Equal(reply, payload) {
		return fmt.Errorf("TCP echo mismatch: %q", reply)
	}
	control, bound, err := dialSOCKS(3, net.IPv4zero, 0)
	if err != nil {
		return fmt.Errorf("UDP SOCKS: %w", err)
	}
	defer control.Close()
	if bound.IP.IsUnspecified() {
		ips, err := net.LookupIP("client")
		if err != nil {
			return err
		}
		for _, ip := range ips {
			if ip.To4() != nil {
				bound.IP = ip.To4()
				break
			}
		}
	}
	udpConn, err := net.DialUDP("udp", nil, bound)
	if err != nil {
		return err
	}
	defer udpConn.Close()
	_ = udpConn.SetDeadline(time.Now().Add(5 * time.Second))
	udpPayload := []byte("xmesh-multi-container-udp")
	packet := []byte{0, 0, 0, 1}
	packet = append(packet, targetIP.To4()...)
	packet = binary.BigEndian.AppendUint16(packet, 19001)
	packet = append(packet, udpPayload...)
	if _, err := udpConn.Write(packet); err != nil {
		return err
	}
	buffer := make([]byte, 65535)
	n, err := udpConn.Read(buffer)
	if err != nil {
		return err
	}
	if n < len(udpPayload) || !bytes.Equal(buffer[n-len(udpPayload):n], udpPayload) {
		return errors.New("UDP echo mismatch")
	}
	return nil
}

func dialSOCKS(command byte, targetIP net.IP, targetPort int) (net.Conn, *net.UDPAddr, error) {
	conn, err := net.DialTimeout("tcp", "client:1080", 3*time.Second)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (net.Conn, *net.UDPAddr, error) { conn.Close(); return nil, nil, err }
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		return fail(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(conn, method); err != nil {
		return fail(err)
	}
	if method[0] != 5 || method[1] != 0 {
		return fail(fmt.Errorf("SOCKS method reply %v", method))
	}
	request := []byte{5, command, 0, 1}
	request = append(request, targetIP.To4()...)
	request = binary.BigEndian.AppendUint16(request, uint16(targetPort))
	if _, err := conn.Write(request); err != nil {
		return fail(err)
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return fail(err)
	}
	if header[0] != 5 || header[1] != 0 {
		return fail(fmt.Errorf("SOCKS command reply %v", header))
	}
	if header[3] != 1 {
		return fail(fmt.Errorf("unsupported SOCKS address type %d", header[3]))
	}
	address := make([]byte, 6)
	if _, err := io.ReadFull(conn, address); err != nil {
		return fail(err)
	}
	bound := &net.UDPAddr{IP: net.IP(address[:4]), Port: int(binary.BigEndian.Uint16(address[4:]))}
	_ = conn.SetDeadline(time.Time{})
	return conn, bound, nil
}
