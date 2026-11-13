package controller

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Purestreams/xmesh/internal/identity"
	"github.com/Purestreams/xmesh/internal/model"
)

// Region is chosen by the administrator; hostnames do not reliably identify location.
func defaultRealityTarget(region string) string {
	switch region {
	case "cn":
		return "api.bilibili.com:443"
	case "overseas":
		return "www.swift.com:443"
	default:
		return ""
	}
}

func validGatewayRegion(region string) bool {
	return region == "" || region == "cn" || region == "overseas"
}

func parseRealityPort(r *http.Request, fallback int) (int, error) {
	if !r.Form.Has("reality_port") {
		return fallback, nil
	}
	value := strings.TrimSpace(r.FormValue("reality_port"))
	if value == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("REALITY TCP port must be between 1 and 65535")
	}
	return port, nil
}

func gatewayRealityURL(host string, port int) string {
	if host == "" {
		return ""
	}
	return "reality://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/tunnel"
}

func realityLinkForNode(gateway model.Gateway, link model.Link) model.Link {
	if link.AutoURL && gateway.PreviousRealityLinkPort != 0 {
		link.URL = gatewayRealityURL(gateway.PublicHost, gateway.PreviousRealityLinkPort)
	}
	return link
}

func confirmGatewayRealityPort(state *model.State, gateway *model.Gateway, status model.NodeStatus) {
	if gateway.RealityPortPendingRevision == 0 || !gateway.Enabled || !status.RealityPortControl || !status.XrayReady ||
		status.AppliedVersion < gateway.RealityPortPendingRevision || status.AppliedVersion > gateway.DesiredVersion {
		return
	}
	previous := gateway.PreviousRealityLinkPort
	gateway.PreviousRealityLinkPort, gateway.RealityPortPendingRevision = 0, 0
	if previous == gateway.RealityLinkPort() {
		return
	}
	agents := map[string]bool{}
	for _, link := range state.Links {
		attachment := state.Attachments[link.AttachmentID]
		if link.AutoURL && attachment.GatewayID == gateway.ID {
			agents[attachment.AgentID] = true
		}
	}
	for id := range agents {
		bumpAgent(state, id)
	}
}

func validateGatewayRealityPort(previous, next model.Gateway) error {
	// Let existing configurations with a locally managed listener remain editable.
	// If VMess is being enabled or either port is changing, prevent a TCP collision.
	if next.VMessEnabled() && next.RealityLinkPort() == next.VMessPort &&
		(next.RealityPort != 0 || previous.VMessPort != next.VMessPort || !previous.VMessEnabled() || previous.RealityPort != next.RealityPort) {
		return errors.New("REALITY and VMess cannot use the same TCP port")
	}
	return nil
}

// provisionReality keeps one REALITY identity per Gateway and one VLESS identity per Link.
func provisionReality(state *model.State, gatewayID, target, linkURL string) (string, string, error) {
	gateway, ok := state.Gateways[gatewayID]
	if !ok {
		return "", "", errors.New("Gateway not found")
	}
	if target == "" {
		target = gateway.RealityTarget
		if target == "" {
			target = defaultRealityTarget(gateway.Region)
		}
	}
	u, err := url.Parse(linkURL)
	if err != nil || u.Scheme != "reality" || u.Hostname() == "" || u.Port() == "" || u.Path == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("REALITY URL must be reality://host:port/tunnel-path")
	}
	linkPort, err := strconv.Atoi(u.Port())
	if err != nil || linkPort <= 0 || linkPort > 65535 {
		return "", "", errors.New("invalid REALITY Link port")
	}
	if target == "" {
		return "", "", errors.New("REALITY target is required")
	}
	name, port, err := net.SplitHostPort(target)
	if err != nil || name == "" || port != "443" || !strings.Contains(name, ".") || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.ContainsAny(name, "/\\?#@ \t\r\n") || net.ParseIP(name) != nil {
		return "", "", errors.New("REALITY target must be a DNS name on port 443")
	}
	if gateway.RealityPrivateKey == "" {
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return "", "", fmt.Errorf("generate REALITY key: %w", err)
		}
		gateway.RealityPrivateKey = base64.RawURLEncoding.EncodeToString(key.Bytes())
		gateway.RealityPublicKey = base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
		gateway.RealityTarget = target
		gateway.RealityName = name
		state.Gateways[gatewayID] = gateway
	} else if gateway.RealityTarget != target {
		return "", "", errors.New("Gateway already uses a different REALITY target")
	}
	uuid, err := identity.UUID()
	if err != nil {
		return "", "", err
	}
	shortID := make([]byte, 8)
	if _, err := rand.Read(shortID); err != nil {
		return "", "", err
	}
	return uuid, hex.EncodeToString(shortID), nil
}
