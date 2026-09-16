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
	"runtime"
	"sort"
	"strconv"
	"time"

	"xmesh/internal/controller"
)

type xrayConfig struct {
	Log       map[string]string `json:"log"`
	Inbounds  []xrayInbound     `json:"inbounds"`
	Outbounds []xrayOutbound    `json:"outbounds"`
	Routing   xrayRouting       `json:"routing"`
}
type xrayInbound struct {
	Listen         string              `json:"listen"`
	Port           int                 `json:"port"`
	Protocol       string              `json:"protocol"`
	Tag            string              `json:"tag"`
	Settings       xrayInboundSettings `json:"settings"`
	StreamSettings xrayStreamSettings  `json:"streamSettings"`
}
type xrayInboundSettings struct {
	Clients []xrayClient `json:"clients"`
}
type xrayClient struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	AlterID int    `json:"alterId"`
}
type xrayStreamSettings struct {
	Network    string         `json:"network"`
	Security   string         `json:"security"`
	WSSettings xrayWSSettings `json:"wsSettings"`
}
type xrayWSSettings struct {
	Path string `json:"path"`
	Host string `json:"host,omitempty"`
}
type xrayOutbound struct {
	Protocol string               `json:"protocol"`
	Tag      string               `json:"tag"`
	Settings xrayOutboundSettings `json:"settings"`
}
type xrayOutboundSettings struct {
	Servers []xraySOCKSServer `json:"servers"`
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
	host, portText, err := net.SplitHostPort(socksAddress)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	result := xrayConfig{Log: map[string]string{"loglevel": "warning"}, Routing: xrayRouting{DomainStrategy: "AsIs"}}
	inbound := xrayInbound{Listen: "0.0.0.0", Port: config.Gateway.VMessPort, Protocol: "vmess", Tag: "xmesh-vmess", Settings: xrayInboundSettings{}, StreamSettings: xrayStreamSettings{Network: "ws", Security: "none", WSSettings: xrayWSSettings{Path: config.Gateway.VMessPath, Host: config.Gateway.VMessHost}}}
	sort.Slice(config.Grants, func(i, j int) bool { return config.Grants[i].ID < config.Grants[j].ID })
	for _, grant := range config.Grants {
		if !grant.Enabled {
			continue
		}
		email, tag := "grant-"+grant.ID, "grant-"+grant.ID
		inbound.Settings.Clients = append(inbound.Settings.Clients, xrayClient{ID: grant.VMessUUID, Email: email, AlterID: 0})
		result.Outbounds = append(result.Outbounds, xrayOutbound{Protocol: "socks", Tag: tag, Settings: xrayOutboundSettings{Servers: []xraySOCKSServer{{Address: host, Port: port, Users: []xraySOCKSUser{{User: grant.SOCKSUsername, Pass: grant.SOCKSPassword}}}}}})
		result.Routing.Rules = append(result.Routing.Rules, xrayRule{Type: "field", InboundTag: []string{"xmesh-vmess"}, User: []string{email}, OutboundTag: tag})
	}
	result.Inbounds = []xrayInbound{inbound}
	return json.MarshalIndent(result, "", "  ")
}

var errXrayReload = errors.New("xray reload requested")

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
		payload, err := buildXrayConfig(config, r.local.Gateway.SOCKSListen)
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
		err = r.runXrayUntilChange(ctx, config.Revision)
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

func (r *Runtime) runXrayUntilChange(ctx context.Context, revision uint64) error {
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(childCtx, r.local.Gateway.XrayBinary, "run", "-config", r.local.Gateway.XrayConfigPath)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start xray: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(750 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		return fmt.Errorf("xray exited during startup: %w", err)
	case <-timer.C:
		r.setXray(true, "")
		r.xrayAppliedRevision.Store(revision)
	}
	select {
	case <-ctx.Done():
		cancel()
		<-done
		return nil
	case <-r.xrayApply:
		cancel()
		<-done
		return errXrayReload
	case err := <-done:
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
	if err := os.Rename(name, path); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(name, path)
}
