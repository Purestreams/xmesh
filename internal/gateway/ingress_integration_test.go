package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentpkg "github.com/Purestreams/xmesh/internal/agent"
	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/controller"
	"github.com/Purestreams/xmesh/internal/hy2"
	"github.com/Purestreams/xmesh/internal/model"
	"github.com/Purestreams/xmesh/internal/runtimecfg"
	"github.com/Purestreams/xmesh/internal/store"
)

func TestWebIngressSwitchesCloseSocketsAndKeepOtherProtocol(t *testing.T) {
	xray := os.Getenv("XMESH_TEST_XRAY")
	if xray == "" || os.Getenv("XMESH_TEST_HYSTERIA") == "" {
		t.Skip("real Xray and official Hysteria binaries required")
	}
	entrance, tunnel, socks := freeTCPUDPAddress(t), freeTCPAddress(t), freeTCPAddress(t)
	certificate, key, pin, err := hy2.Generate("127.0.0.1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte{7}, 32)
	passwordHash, err := auth.PasswordHash("test-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	grant := model.Grant{ID: "grant", UserID: "user", AttachmentID: "route", Enabled: true, VMessUUID: "00000000-0000-4000-8000-000000000001", HY2Auth: "hy2-secret", SOCKSUsername: "grant", SOCKSPassword: "socks-secret"}
	link := model.Link{ID: "link", AttachmentID: "route", URL: "ws://" + tunnel + "/tunnel", Priority: 10, Weight: 1, Connections: 1, MaxStreams: 16, Enabled: true}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(state *model.State) error {
		state.Gateways["g"] = model.Gateway{ID: "g", Name: "Gateway", Enabled: true, PublicHost: "127.0.0.1", VMessPort: testPort(t, entrance), VMessPath: "/proxy", HY2Port: testPort(t, entrance), HY2Certificate: certificate, HY2PrivateKey: key, HY2PinSHA256: pin, CredentialHash: auth.SecretHash("gateway"), DesiredVersion: 1}
		state.Agents["a"] = model.Agent{ID: "a", Enabled: true, AllowedCIDRs: []string{"127.0.0.0/8"}}
		state.Attachments["route"] = model.Attachment{ID: "route", GatewayID: "g", AgentID: "a", Enabled: true}
		state.Users["user"] = model.User{ID: "user", Enabled: true, SubscriptionToken: "subscription"}
		state.Grants[grant.ID] = grant
		state.Links[link.ID] = link
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	config := controller.Config{AdminUsername: "admin", AdminPasswordHash: passwordHash, SessionSecret: base64.RawURLEncoding.EncodeToString(secret), NodeOfflineAfterSeconds: 45}
	panel, err := controller.New(config, db, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(panel.Handler())
	defer server.Close()
	r := New(runtimecfg.Config{NodeID: "g", ControllerURL: server.URL, Credential: "gateway", PollInterval: runtimecfg.Duration(30 * time.Millisecond), StatusInterval: runtimecfg.Duration(30 * time.Millisecond), Gateway: runtimecfg.Gateway{SOCKSListen: socks, TunnelListen: tunnel, TunnelPath: "/tunnel", XrayBinary: xray, XrayConfigPath: filepath.Join(t.TempDir(), "gateway.json"), MaxUDPAssociations: 16}}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Gateway did not stop")
		}
	})
	waitTCP(t, tunnel, 5*time.Second)
	a := agentpkg.New(runtimecfg.Config{NodeID: "a"}, slog.Default())
	if err := a.ApplyConfig(controller.AgentConfig{Agent: model.Agent{ID: "a", Enabled: true, AllowedCIDRs: []string{"127.0.0.0/8"}}, GrantIDs: []string{grant.ID}, Links: []controller.AgentLinkConfig{{Link: link, GrantIDs: []string{grant.ID}}}}); err != nil {
		t.Fatal(err)
	}
	go func() {
		for ctx.Err() == nil {
			a.RunLink(ctx, controller.AgentLinkConfig{Link: link, GatewayID: "g", TunnelToken: auth.Derive(secret, "tunnel", link.ID)})
			time.Sleep(20 * time.Millisecond)
		}
	}()
	waitFor(t, 5*time.Second, func() bool {
		return r.xrayReady.Load() && len(r.pool.Snapshot()) == 1 && db.Snapshot().Grants[grant.ID].Published
	})
	cookie := auth.SignSession([]byte(auth.Derive(secret, "admin-session", passwordHash)), "admin", time.Now().Add(time.Hour))
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	tcpTarget := startTCPEcho(t)
	defer tcpTarget.Close()
	udpTarget := startUDPEcho(t)
	defer udpTarget.Close()
	for _, mode := range []struct {
		name       string
		vmess, hy2 bool
	}{{"both", true, true}, {"hy2-only", false, true}, {"vmess-only", true, false}, {"neither", false, false}, {"restored", true, true}} {
		t.Run(mode.name, func(t *testing.T) {
			form := url.Values{"ingress_controls": {"1"}, "csrf": {auth.Derive(secret, "csrf", cookie)}}
			if mode.vmess {
				form.Set("vmess_enabled", "1")
			}
			if mode.hy2 {
				form.Set("hy2_enabled", "1")
			}
			request, _ := http.NewRequest("POST", server.URL+"/admin/gateways/g/ingress", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(&http.Cookie{Name: "xmesh_admin", Value: cookie})
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != 303 || response.Header.Get("Location") != "/" {
				t.Fatalf("Web switch: status %d location %s body %s", response.StatusCode, response.Header.Get("Location"), body)
			}
			revision := db.Snapshot().Gateways["g"].DesiredVersion
			waitFor(t, 5*time.Second, func() bool {
				state := db.Snapshot()
				return r.xrayReady.Load() && r.xrayAppliedRevision.Load() == revision && state.NodeStatus["g"].AppliedVersion == revision && state.Grants[grant.ID].Published == (mode.vmess || mode.hy2)
			})
			state := db.Snapshot()
			if state.Gateways["g"].HY2PinSHA256 != pin || len(r.pool.Snapshot()) != 1 {
				t.Fatal("protocol switch changed identity or retired the Agent tunnel")
			}
			response, err = client.Get(server.URL + "/subscription/subscription")
			if err != nil {
				t.Fatal(err)
			}
			body, _ = io.ReadAll(response.Body)
			response.Body.Close()
			plain, err := base64.StdEncoding.DecodeString(string(body))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(plain), "vmess://") != mode.vmess || strings.Contains(string(plain), "hysteria2://") != mode.hy2 {
				t.Fatalf("wrong Web subscription: %s", plain)
			}
			if !mode.vmess {
				if conn, err := net.DialTimeout("tcp", entrance, 300*time.Millisecond); err == nil {
					conn.Close()
					t.Fatal("disabled VMess TCP listener still accepts connections")
				}
			}
			if !mode.hy2 {
				socket, err := net.ListenPacket("udp", entrance)
				if err != nil {
					t.Fatalf("disabled HY2 UDP port is still bound: %v", err)
				}
				socket.Close()
			}
			addresses := []string{}
			if mode.hy2 {
				addresses = append(addresses, startHY2Client(t, entrance, grant.HY2Auth, certificate, pin, "127.0.0.1"))
			}
			if mode.vmess {
				address := freeTCPUDPAddress(t)
				vmess := map[string]any{"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": testPort(t, address), "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true, "ip": "127.0.0.1"}}}, "outbounds": []any{map[string]any{"protocol": "vmess", "settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": testPort(t, entrance), "users": []any{map[string]any{"id": grant.VMessUUID}}}}}, "streamSettings": map[string]any{"network": "ws", "wsSettings": map[string]any{"path": "/proxy"}}}}}
				data, _ := json.Marshal(vmess)
				process := startTestXray(t, xray, data)
				defer stopTestProcess(process)
				waitTCP(t, address, 5*time.Second)
				addresses = append(addresses, address)
			}
			for _, address := range addresses {
				conn, err := hy2TCP(address, tcpTarget.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				payload := []byte("protocol-switch")
				conn.Write(payload)
				reply := make([]byte, len(payload))
				_, err = io.ReadFull(conn, reply)
				conn.Close()
				if err != nil || !bytes.Equal(reply, payload) {
					t.Fatalf("remaining protocol TCP failed: %v", err)
				}
				control := dialSOCKSNoAuth(t, address, 3, "0.0.0.0:0").(*socksTestConn)
				defer control.Close()
				udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
				if err != nil {
					t.Fatal(err)
				}
				defer udp.Close()
				assertUDPEcho(t, udp, control.bound, udpTarget.LocalAddr().(*net.UDPAddr), payload, 5*time.Second)
			}
		})
	}
}
