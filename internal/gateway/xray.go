package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"xmesh/internal/atomicfile"
	"xmesh/internal/controller"
)

type xrayConfig struct {
	Log       map[string]string `json:"log"`
	Inbounds  []xrayInbound     `json:"inbounds"`
	Outbounds []xrayOutbound    `json:"outbounds"`
	Routing   xrayRouting       `json:"routing"`
	API       *xrayAPI          `json:"api,omitempty"`
	Stats     *struct{}         `json:"stats,omitempty"`
	Policy    *xrayPolicy       `json:"policy,omitempty"`
}
type xrayAPI struct {
	Tag      string   `json:"tag"`
	Services []string `json:"services"`
}
type xrayPolicy struct {
	Levels map[string]map[string]bool `json:"levels"`
}
type xrayInbound struct {
	Listen         string              `json:"listen"`
	Port           int                 `json:"port"`
	Protocol       string              `json:"protocol"`
	Tag            string              `json:"tag"`
	Settings       xrayInboundSettings `json:"settings"`
	StreamSettings *xrayStreamSettings `json:"streamSettings,omitempty"`
}
type xrayInboundSettings struct {
	Clients    []xrayClient `json:"clients"`
	Decryption string       `json:"decryption,omitempty"`
	Address    string       `json:"address,omitempty"`
}
type xrayClient struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	AlterID int    `json:"alterId"`
}
type xrayStreamSettings struct {
	Network         string           `json:"network"`
	Security        string           `json:"security"`
	WSSettings      xrayWSSettings   `json:"wsSettings,omitempty"`
	RealitySettings any              `json:"realitySettings,omitempty"`
	TLSSettings     *xrayTLSSettings `json:"tlsSettings,omitempty"`
}
type xrayTLSSettings struct {
	ServerName string `json:"serverName,omitempty"`
}
type xrayRealitySettings struct {
	Target      string   `json:"target"`
	ServerNames []string `json:"serverNames"`
	PrivateKey  string   `json:"privateKey"`
	ShortIDs    []string `json:"shortIds"`
}
type xrayRealityClientSettings struct {
	ServerName  string `json:"serverName"`
	Fingerprint string `json:"fingerprint"`
	Password    string `json:"password"`
	ShortID     string `json:"shortId"`
	SpiderX     string `json:"spiderX"`
}
type xrayWSSettings struct {
	Path string `json:"path"`
	Host string `json:"host,omitempty"`
}
type xrayOutbound struct {
	Protocol       string               `json:"protocol"`
	Tag            string               `json:"tag"`
	Settings       xrayOutboundSettings `json:"settings"`
	StreamSettings *xrayStreamSettings  `json:"streamSettings,omitempty"`
}
type xrayOutboundSettings struct {
	Servers    []xraySOCKSServer `json:"servers,omitempty"`
	Redirect   string            `json:"redirect,omitempty"`
	VNext      []xrayVMessServer `json:"vnext,omitempty"`
	Address    string            `json:"address,omitempty"`
	Port       int               `json:"port,omitempty"`
	ID         string            `json:"id,omitempty"`
	Encryption string            `json:"encryption,omitempty"`
	Flow       string            `json:"flow,omitempty"`
}
type xrayVMessServer struct {
	Address string          `json:"address"`
	Port    int             `json:"port"`
	Users   []xrayVMessUser `json:"users"`
}
type xrayVMessUser struct {
	ID       string `json:"id"`
	AlterID  int    `json:"alterId"`
	Security string `json:"security"`
}
type xraySOCKSServer struct {
	Address string          `json:"address"`
	Port    int             `json:"port"`
	Users   []xraySOCKSUser `json:"users"`
}
type xraySOCKSUser struct {
	User string `json:"user"`
	Pass string `json:"pass"`
}
type xrayRouting struct {
	DomainStrategy string     `json:"domainStrategy"`
	Rules          []xrayRule `json:"rules"`
}
type xrayRule struct {
	Type        string   `json:"type"`
	InboundTag  []string `json:"inboundTag"`
	User        []string `json:"user"`
	OutboundTag string   `json:"outboundTag"`
}

func buildXrayConfig(config controller.GatewayConfig, socksAddress string) ([]byte, error) {
	return buildXrayConfigWithReality(config, socksAddress, "", "")
}

func buildXrayConfigWithReality(config controller.GatewayConfig, socksAddress, realityAddress, tunnelAddress string) ([]byte, error) {
	return buildXrayConfigWithStats(config, socksAddress, realityAddress, tunnelAddress, "")
}

func buildXrayConfigWithStats(config controller.GatewayConfig, socksAddress, realityAddress, tunnelAddress, statsAddress string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(socksAddress)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	result := xrayConfig{Log: map[string]string{"loglevel": "warning"}, Routing: xrayRouting{DomainStrategy: "AsIs"}}
	upstreams := make(map[string]controller.GatewayUpstreamConfig, len(config.Upstreams))
	if len(config.Upstreams) > 0 {
		// Xray defaults to the first outbound when no rule matches.
		result.Outbounds = append(result.Outbounds, xrayOutbound{Protocol: "blackhole", Tag: "xmesh-deny", Settings: xrayOutboundSettings{}})
	}
	for _, upstream := range config.Upstreams {
		if upstream.AttachmentID == "" || upstream.Endpoint.UUID == "" {
			return nil, errors.New("invalid external upstream")
		}
		upstreams[upstream.AttachmentID] = upstream
		if upstream.Endpoint.Protocol == "vless" {
			if upstream.Endpoint.Flow != "xtls-rprx-vision" || upstream.Endpoint.Network != "raw" || upstream.Endpoint.PublicKey == "" || upstream.Endpoint.ServerName == "" {
				return nil, errors.New("invalid VLESS REALITY upstream")
			}
			stream := xrayStreamSettings{Network: "raw", Security: "reality", RealitySettings: &xrayRealityClientSettings{ServerName: upstream.Endpoint.ServerName, Fingerprint: upstream.Endpoint.Fingerprint, Password: upstream.Endpoint.PublicKey, ShortID: upstream.Endpoint.ShortID, SpiderX: upstream.Endpoint.SpiderX}}
			result.Outbounds = append(result.Outbounds, xrayOutbound{Protocol: "vless", Tag: "upstream-" + upstream.AttachmentID, Settings: xrayOutboundSettings{Address: upstream.Endpoint.Address, Port: upstream.Endpoint.Port, ID: upstream.Endpoint.UUID, Encryption: "none", Flow: upstream.Endpoint.Flow}, StreamSettings: &stream})
			continue
		}
		if upstream.Endpoint.Protocol != "" && upstream.Endpoint.Protocol != "vmess" {
			return nil, errors.New("unsupported external upstream protocol")
		}
		cipher := upstream.Endpoint.Cipher
		if cipher == "" {
			cipher = "auto"
		}
		stream := xrayStreamSettings{Network: upstream.Endpoint.Network, Security: "none"}
		if stream.Network == "ws" {
			stream.WSSettings = xrayWSSettings{Path: upstream.Endpoint.Path, Host: upstream.Endpoint.Host}
		}
		if upstream.Endpoint.TLS {
			stream.Security = "tls"
			stream.TLSSettings = &xrayTLSSettings{ServerName: upstream.Endpoint.ServerName}
		}
		result.Outbounds = append(result.Outbounds, xrayOutbound{Protocol: "vmess", Tag: "upstream-" + upstream.AttachmentID, Settings: xrayOutboundSettings{VNext: []xrayVMessServer{{Address: upstream.Endpoint.Address, Port: upstream.Endpoint.Port, Users: []xrayVMessUser{{ID: upstream.Endpoint.UUID, AlterID: 0, Security: cipher}}}}}, StreamSettings: &stream})
	}
	inbound := xrayInbound{Listen: "0.0.0.0", Port: config.Gateway.VMessPort, Protocol: "vmess", Tag: "xmesh-vmess", Settings: xrayInboundSettings{}, StreamSettings: &xrayStreamSettings{Network: "ws", Security: "none", WSSettings: xrayWSSettings{Path: config.Gateway.VMessPath, Host: config.Gateway.VMessHost}}}
	sort.Slice(config.Grants, func(i, j int) bool { return config.Grants[i].ID < config.Grants[j].ID })
	for _, grant := range config.Grants {
		if !grant.Enabled {
			continue
		}
		email, tag := "grant-"+grant.ID, "grant-"+grant.ID
		inbound.Settings.Clients = append(inbound.Settings.Clients, xrayClient{ID: grant.VMessUUID, Email: email, AlterID: 0})
		if _, external := upstreams[grant.AttachmentID]; external {
			tag = "upstream-" + grant.AttachmentID
		} else {
			result.Outbounds = append(result.Outbounds, xrayOutbound{Protocol: "socks", Tag: tag, Settings: xrayOutboundSettings{Servers: []xraySOCKSServer{{Address: host, Port: port, Users: []xraySOCKSUser{{User: grant.SOCKSUsername, Pass: grant.SOCKSPassword}}}}}})
		}
		result.Routing.Rules = append(result.Routing.Rules, xrayRule{Type: "field", InboundTag: []string{"xmesh-vmess"}, User: []string{email}, OutboundTag: tag})
	}
	result.Inbounds = []xrayInbound{inbound}
	if len(config.Upstreams) > 0 && statsAddress != "" {
		statsHost, statsPortText, err := net.SplitHostPort(statsAddress)
		if err != nil || statsHost != "127.0.0.1" {
			return nil, errors.New("Xray stats must listen on 127.0.0.1")
		}
		statsPort, err := strconv.Atoi(statsPortText)
		if err != nil || statsPort < 1 || statsPort > 65535 {
			return nil, errors.New("invalid Xray stats port")
		}
		result.Inbounds = append(result.Inbounds, xrayInbound{Listen: statsHost, Port: statsPort, Protocol: "dokodemo-door", Tag: "xmesh-stats-api", Settings: xrayInboundSettings{Address: statsHost}})
		result.API = &xrayAPI{Tag: "xmesh-api", Services: []string{"StatsService"}}
		result.Stats = &struct{}{}
		result.Policy = &xrayPolicy{Levels: map[string]map[string]bool{"0": {"statsUserUplink": true, "statsUserDownlink": true}}}
		result.Routing.Rules = append(result.Routing.Rules, xrayRule{Type: "field", InboundTag: []string{"xmesh-stats-api"}, OutboundTag: "xmesh-api"})
	}
	if config.Gateway.RealityPrivateKey != "" {
		realityHost, realityPortText, err := net.SplitHostPort(realityAddress)
		if err != nil {
			return nil, fmt.Errorf("REALITY listen: %w", err)
		}
		realityPort, err := strconv.Atoi(realityPortText)
		if err != nil || realityPort <= 0 || realityPort > 65535 {
			return nil, errors.New("invalid REALITY listen port")
		}
		if realityHost == "" {
			realityHost = "0.0.0.0"
		}
		if _, _, err := net.SplitHostPort(tunnelAddress); err != nil {
			return nil, fmt.Errorf("tunnel listen: %w", err)
		}
		realityInbound := xrayInbound{Listen: realityHost, Port: realityPort, Protocol: "vless", Tag: "xmesh-reality", Settings: xrayInboundSettings{Decryption: "none"}, StreamSettings: &xrayStreamSettings{Network: "raw", Security: "reality", RealitySettings: &xrayRealitySettings{Target: config.Gateway.RealityTarget, ServerNames: []string{config.Gateway.RealityName}, PrivateKey: config.Gateway.RealityPrivateKey}}}
		for _, link := range config.Links {
			if !link.Enabled || link.RealityUUID == "" {
				continue
			}
			realityInbound.Settings.Clients = append(realityInbound.Settings.Clients, xrayClient{ID: link.RealityUUID, Email: "link-" + link.ID})
			settings := realityInbound.StreamSettings.RealitySettings.(*xrayRealitySettings)
			settings.ShortIDs = append(settings.ShortIDs, link.RealityShortID)
		}
		if len(realityInbound.Settings.Clients) > 0 {
			result.Inbounds = append(result.Inbounds, realityInbound)
			result.Outbounds = append(result.Outbounds, xrayOutbound{Protocol: "freedom", Tag: "xmesh-reality-local", Settings: xrayOutboundSettings{Redirect: tunnelAddress}})
			result.Routing.Rules = append(result.Routing.Rules, xrayRule{Type: "field", InboundTag: []string{"xmesh-reality"}, OutboundTag: "xmesh-reality-local"})
		}
	}
	return json.MarshalIndent(result, "", "  ")
}

var errXrayReload = errors.New("xray reload requested")

// Validate the next config while the current Xray process is still serving.
// A rejected revision must not trigger xrayApply and stop the working process.
func (r *Runtime) validateXrayCandidate(ctx context.Context, config controller.GatewayConfig) error {
	payload, err := buildXrayConfigWithStats(config, r.local.Gateway.SOCKSListen, r.local.Gateway.RealityListen, r.local.Gateway.TunnelListen, r.local.Gateway.StatsListen)
	if err != nil {
		return fmt.Errorf("build Xray config: %w", err)
	}
	dir := filepath.Dir(r.local.Gateway.XrayConfigPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".xray-candidate-*.json")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(payload); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	validateCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(validateCtx, r.local.Gateway.XrayBinary, "run", "-test", "-config", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("xray validation: %w: %s", err, bytes.TrimSpace(output))
	}
	return nil
}

func (r *Runtime) xrayLoop(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return nil
	case <-r.xrayApply:
	}
	for {
		r.mu.RLock()
		config := r.config
		r.mu.RUnlock()
		payload, err := buildXrayConfigWithStats(config, r.local.Gateway.SOCKSListen, r.local.Gateway.RealityListen, r.local.Gateway.TunnelListen, r.local.Gateway.StatsListen)
		if err != nil {
			r.setXray(false, err.Error())
			return err
		}
		if err := writeProtectedAtomic(r.local.Gateway.XrayConfigPath, payload); err != nil {
			r.setXray(false, err.Error())
			return err
		}
		validateCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		output, err := exec.CommandContext(validateCtx, r.local.Gateway.XrayBinary, "run", "-test", "-config", r.local.Gateway.XrayConfigPath).CombinedOutput()
		cancel()
		if err != nil {
			r.setXray(false, fmt.Sprintf("xray validation: %v: %s", err, bytes.TrimSpace(output)))
			select {
			case <-ctx.Done():
				return nil
			case <-r.xrayApply:
				continue
			case <-time.After(5 * time.Second):
				continue
			}
		}
		err = r.runXrayUntilChange(ctx, config)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errXrayReload) {
			continue
		}
		r.setXray(false, err.Error())
		select {
		case <-ctx.Done():
			return nil
		case <-r.xrayApply:
			continue
		case <-time.After(2 * time.Second):
			continue
		}
	}
}

func (r *Runtime) runXrayUntilChange(ctx context.Context, config controller.GatewayConfig) error {
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(childCtx, r.local.Gateway.XrayBinary, "run", "-config", r.local.Gateway.XrayConfigPath)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start xray: %w", err)
	}
	r.mu.Lock()
	r.xrayStatsConfig = config
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.xrayStatsConfig = controller.GatewayConfig{}
		r.mu.Unlock()
		r.setXray(false, "")
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(750 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		return fmt.Errorf("xray exited during startup: %w", err)
	case <-timer.C:
		r.setXray(true, "")
		r.xrayAppliedRevision.Store(config.Revision)
	}
	select {
	case <-ctx.Done():
		r.collectXrayStats(context.Background())
		cancel()
		<-done
		return nil
	case <-r.xrayApply:
		r.collectXrayStats(context.Background())
		reportCtx, reportCancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := r.report(reportCtx); err != nil {
			r.logger.Warn("report traffic before Xray reload", "error", err)
			r.mu.Lock()
			if r.pendingStats == nil {
				r.pendingStats = map[uint64]pendingStatsConfig{}
			}
			r.pendingStats[config.Revision] = pendingStatsConfig{config: config, expiresAt: time.Now().Add(pendingStatsLifetime)}
			r.mu.Unlock()
		}
		reportCancel()
		cancel()
		<-done
		return errXrayReload
	case err := <-done:
		r.collectXrayStats(context.Background())
		return fmt.Errorf("xray exited: %w", err)
	}
}

func (r *Runtime) setXray(ready bool, message string) {
	r.xrayReady.Store(ready)
	r.xrayError.Store(message)
}

func writeProtectedAtomic(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".xray-*.json")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(payload); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return atomicfile.Replace(name, path)
}
