package runtimecfg

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"xmesh/internal/model"
)

type Config struct {
	Role              model.Role `json:"role"`
	NodeID            string     `json:"node_id"`
	ControllerURL     string     `json:"controller_url"`
	Credential        string     `json:"credential"`
	PollInterval      Duration   `json:"poll_interval"`
	StatusInterval    Duration   `json:"status_interval"`
	WriteStallTimeout Duration   `json:"write_stall_timeout,omitempty"`
	MaxActiveStreams  int        `json:"max_active_streams,omitempty"`
	Gateway           Gateway    `json:"gateway,omitempty"`
}

type Gateway struct {
	SOCKSListen        string   `json:"socks_listen"`
	TunnelListen       string   `json:"tunnel_listen"`
	TunnelPath         string   `json:"tunnel_path"`
	XrayBinary         string   `json:"xray_binary"`
	XrayConfigPath     string   `json:"xray_config_path"`
	UDPIdleTimeout     Duration `json:"udp_idle_timeout"`
	MaxUDPAssociations int      `json:"max_udp_associations"`
	MaxUDPQueueBytes   int      `json:"max_udp_queue_bytes,omitempty"`
}

type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var value string
	if err := json.Unmarshal(b, &value); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Value(fallback time.Duration) time.Duration {
	if d <= 0 {
		return fallback
	}
	return time.Duration(d)
}

func Load(path string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read node config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("decode node config: %w", err)
	}
	if cfg.Role != model.RoleGateway && cfg.Role != model.RoleAgent {
		return cfg, fmt.Errorf("role must be gateway or agent")
	}
	if cfg.NodeID == "" || cfg.ControllerURL == "" || cfg.Credential == "" {
		return cfg, fmt.Errorf("node_id, controller_url, and credential are required")
	}
	if cfg.Gateway.SOCKSListen == "" {
		cfg.Gateway.SOCKSListen = "127.0.0.1:18080"
	}
	if cfg.Gateway.TunnelListen == "" {
		cfg.Gateway.TunnelListen = "127.0.0.1:18081"
	}
	if cfg.Gateway.TunnelPath == "" {
		cfg.Gateway.TunnelPath = "/tunnel"
	}
	if cfg.Gateway.XrayBinary == "" {
		cfg.Gateway.XrayBinary = "/usr/local/bin/xray"
	}
	if cfg.Gateway.XrayConfigPath == "" {
		cfg.Gateway.XrayConfigPath = "/var/lib/xmesh/xray.json"
	}
	if cfg.Gateway.MaxUDPAssociations <= 0 {
		cfg.Gateway.MaxUDPAssociations = 1024
	}
	if cfg.Gateway.MaxUDPQueueBytes <= 0 {
		cfg.Gateway.MaxUDPQueueBytes = 256 << 10
	}
	if cfg.MaxActiveStreams <= 0 {
		cfg.MaxActiveStreams = 1024
	}
	return cfg, nil
}
