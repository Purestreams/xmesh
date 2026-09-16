package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/coder/websocket"
	_ "github.com/xtls/xray-core/app/dispatcher"
	_ "github.com/xtls/xray-core/app/log"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	_ "github.com/xtls/xray-core/app/router"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/json"
	_ "github.com/xtls/xray-core/proxy/vless/outbound"
	_ "github.com/xtls/xray-core/transport/internet/reality"
	_ "github.com/xtls/xray-core/transport/internet/tcp"

	"xmesh/internal/controller"
)

type realityCore struct {
	config   string
	instance *core.Instance
}

func buildRealityClientConfig(link controller.AgentLinkConfig) ([]byte, error) {
	u, err := url.Parse(link.URL)
	if err != nil || u.Scheme != "reality" || u.Hostname() == "" || u.Port() == "" || u.Path == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid REALITY Link URL")
	}
	remotePort, err := strconv.Atoi(u.Port())
	if err != nil || remotePort <= 0 || remotePort > 65535 {
		return nil, errors.New("invalid REALITY port")
	}
	if link.RealityUUID == "" || link.RealityShortID == "" || link.RealityPublicKey == "" || link.RealityName == "" {
		return nil, errors.New("incomplete REALITY credentials")
	}
	config := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"outbounds": []any{map[string]any{"protocol": "vless", "tag": "xmesh-reality", "settings": map[string]any{"vnext": []any{map[string]any{"address": u.Hostname(), "port": remotePort, "users": []any{map[string]any{"id": link.RealityUUID, "encryption": "none"}}}}}, "streamSettings": map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{"serverName": link.RealityName, "fingerprint": "chrome", "publicKey": link.RealityPublicKey, "shortId": link.RealityShortID}}}},
	}
	return json.MarshalIndent(config, "", "  ")
}

// dialReality embeds the upstream Xray core. No helper process or local TCP
// listener is needed: core.Dial returns the authenticated REALITY connection.
func (r *Runtime) dialReality(ctx context.Context, link controller.AgentLinkConfig) (*websocket.Conn, func(), error) {
	payload, err := buildRealityClientConfig(link)
	if err != nil {
		return nil, nil, err
	}
	instance, err := r.realityInstance(link.ID, payload)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {}
	u, _ := url.Parse(link.URL)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
		return core.Dial(dialCtx, instance, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), 18081))
	}}
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dialCtx, "ws://127.0.0.1"+u.EscapedPath(), &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport, Timeout: 20 * time.Second}, Host: link.HTTPHost, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil, nil, fmt.Errorf("REALITY tunnel dial: %w", err)
	}
	return ws, cleanup, nil
}

func (r *Runtime) realityInstance(id string, payload []byte) (*core.Instance, error) {
	r.realityMu.Lock()
	defer r.realityMu.Unlock()
	if current := r.realities[id]; current != nil && current.config == string(payload) {
		return current.instance, nil
	}
	if current := r.realities[id]; current != nil {
		_ = current.instance.Close()
		delete(r.realities, id)
	}
	instance, err := core.StartInstance("json", payload)
	if err != nil {
		return nil, fmt.Errorf("start REALITY core: %w", err)
	}
	r.realities[id] = &realityCore{config: string(payload), instance: instance}
	return instance, nil
}

func (r *Runtime) closeRealities() {
	r.realityMu.Lock()
	defer r.realityMu.Unlock()
	for id, current := range r.realities {
		_ = current.instance.Close()
		delete(r.realities, id)
	}
}
