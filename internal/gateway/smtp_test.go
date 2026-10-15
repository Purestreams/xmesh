package gateway

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	xrouter "github.com/xtls/xray-core/app/router"
	xnet "github.com/xtls/xray-core/common/net"
	xprotocol "github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	routingsession "github.com/xtls/xray-core/features/routing/session"
	"github.com/xtls/xray-core/infra/conf/serial"

	"github.com/Purestreams/xmesh/internal/controller"
	"github.com/Purestreams/xmesh/internal/hy2"
	"github.com/Purestreams/xmesh/internal/model"
	"github.com/Purestreams/xmesh/internal/runtimecfg"
)

func TestDefaultSMTPRoutesAcrossIngressesAndExits(t *testing.T) {
	certificate, certificateKey, pin, err := hy2.Generate("edge.example", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	realityKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, exit := range []string{"agent", "vmess", "vless"} {
		for _, ingress := range []string{"vmess", "hy2", "both"} {
			t.Run(exit+"/"+ingress, func(t *testing.T) {
				config := controller.GatewayConfig{
					Gateway: model.Gateway{VMessPort: 8080, VMessPath: "/proxy", VMessDisabled: ingress == "hy2", HY2Disabled: ingress == "vmess", PublicHost: "edge.example", HY2Port: 8080, HY2Certificate: certificate, HY2PrivateKey: certificateKey, HY2PinSHA256: pin, RealityPrivateKey: base64.RawURLEncoding.EncodeToString(realityKey.Bytes()), RealityTarget: "example.com:443", RealityName: "example.com"},
					Grants:  []model.Grant{{ID: "grant", AttachmentID: "route", Enabled: true, VMessUUID: "00000000-0000-4000-8000-000000000001", HY2Auth: "secret"}},
					Links:   []controller.GatewayLinkConfig{{Link: model.Link{ID: "link", Enabled: true, RealityUUID: "00000000-0000-4000-8000-000000000002", RealityShortID: "0123456789abcdef"}}},
				}
				want := "grant-grant"
				if exit != "agent" {
					endpoint := model.VMessEndpoint{Address: "exit.example", Port: 443, UUID: "00000000-0000-4000-8000-000000000003", Network: "tcp"}
					if exit == "vless" {
						endpoint.Protocol, endpoint.Network, endpoint.Flow = "vless", "raw", "xtls-rprx-vision"
						endpoint.PublicKey, endpoint.ServerName = base64.RawURLEncoding.EncodeToString(realityKey.PublicKey().Bytes()), "example.com"
						endpoint.Fingerprint, endpoint.ShortID = "chrome", "0123456789abcdef"
					}
					config.Upstreams = []controller.GatewayUpstreamConfig{{AttachmentID: "route", Endpoint: endpoint}}
					want = "upstream-route"
				}
				payload, err := buildXrayConfigWithStats(config, "127.0.0.1:18080", "127.0.0.1:18443", "127.0.0.1:25", "127.0.0.1:25")
				if err != nil {
					t.Fatal(err)
				}
				var rendered xrayConfig
				if err := json.Unmarshal(payload, &rendered); err != nil {
					t.Fatal(err)
				}
				if len(rendered.Outbounds) == 0 || rendered.Outbounds[0].Tag != "xmesh-deny" || rendered.Outbounds[0].Protocol != "blackhole" {
					t.Fatal("missing default block outbound")
				}
				// Build and exercise the pinned Xray router rather than only comparing JSON.
				built, err := serial.LoadJSONConfig(bytes.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				var routerConfig *xrouter.Config
				for _, app := range built.App {
					instance, err := app.GetInstance()
					if err != nil {
						t.Fatal(err)
					}
					if candidate, ok := instance.(*xrouter.Config); ok {
						routerConfig = candidate
					}
				}
				if routerConfig == nil {
					t.Fatal("missing Xray router")
				}
				var router xrouter.Router
				if err := router.Init(context.Background(), routerConfig, nil, nil, nil); err != nil {
					t.Fatal(err)
				}
				defer router.Close()
				assertRoute := func(tag, host string, port int, network xnet.Network, user, expected string) {
					t.Helper()
					ctx := &routingsession.Context{Inbound: &session.Inbound{Tag: tag, User: &xprotocol.MemoryUser{Email: user}}, Outbound: &session.Outbound{Target: xnet.Destination{Address: xnet.ParseAddress(host), Port: xnet.Port(port), Network: network}}}
					route, err := router.PickRoute(ctx)
					if err != nil {
						t.Fatalf("%s %s:%d %v: %v", tag, host, port, network, err)
					}
					if route.GetOutboundTag() != expected {
						t.Fatalf("%s %s:%d %v routed to %s, want %s", tag, host, port, network, route.GetOutboundTag(), expected)
					}
				}
				for _, inbound := range rendered.Inbounds {
					if inbound.Tag != "xmesh-vmess" && inbound.Tag != "xmesh-hy2" {
						continue
					}
					for _, host := range []string{"192.0.2.1", "2001:db8::1", "smtp.example"} {
						for _, port := range []int{25, 465, 587, 2525} {
							assertRoute(inbound.Tag, host, port, xnet.Network_TCP, "grant-grant", "xmesh-deny")
							assertRoute(inbound.Tag, host, port, xnet.Network_UDP, "grant-grant", want)
						}
						for _, port := range []int{80, 443, 2526} {
							assertRoute(inbound.Tag, host, port, xnet.Network_TCP, "grant-grant", want)
						}
					}
				}
				assertRoute("xmesh-reality", "127.0.0.1", 25, xnet.Network_TCP, "link-link", "xmesh-reality-local")
				if exit != "agent" {
					assertRoute("xmesh-stats-api", "127.0.0.1", 25, xnet.Network_TCP, "", "xmesh-api")
				}
			})
		}
	}
}

func TestSOCKSDefaultSMTPBlockBeforeTunnelSelection(t *testing.T) {
	for _, port := range []int{25, 465, 587, 2525} {
		t.Run(fmt.Sprint(port), func(t *testing.T) {
			r := New(runtimecfg.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			r.config = controller.GatewayConfig{Gateway: model.Gateway{Enabled: true}, Grants: []model.Grant{{ID: "grant", AttachmentID: "route", Enabled: true, SOCKSUsername: "u", SOCKSPassword: "p"}}, Links: []controller.GatewayLinkConfig{{Link: model.Link{AttachmentID: "route"}, AgentID: "agent"}}}
			server, client := net.Pipe()
			defer client.Close()
			_ = client.SetDeadline(time.Now().Add(3 * time.Second))
			done := make(chan struct{})
			go func() { defer close(done); r.handleSOCKS(context.Background(), server, nil) }()
			for _, handshake := range [][]byte{{5, 1, 2}, {1, 1, 'u', 1, 'p'}} {
				if _, err := client.Write(handshake); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadFull(client, make([]byte, 2)); err != nil {
					t.Fatal(err)
				}
			}
			request := []byte{5, 1, 0, 3, 12}
			request = append(request, "smtp.invalid"...)
			request = binary.BigEndian.AppendUint16(request, uint16(port))
			if _, err := client.Write(request); err != nil {
				t.Fatal(err)
			}
			reply := make([]byte, 10)
			if _, err := io.ReadFull(client, reply); err != nil || reply[1] != 2 {
				t.Fatalf("expected ruleset denial before selecting an unavailable tunnel: %v err=%v", reply, err)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("blocked connection did not close")
			}
		})
	}
}
