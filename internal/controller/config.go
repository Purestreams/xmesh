package controller

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Listen                  string `json:"listen"`
	PublicURL               string `json:"public_url"`
	StatePath               string `json:"state_path"`
	AdminUsername           string `json:"admin_username"`
	AdminPasswordHash       string `json:"admin_password_hash,omitempty"`
	SessionSecret           string `json:"session_secret"`
	ReleaseBaseURL          string `json:"release_base_url,omitempty"`
	ReleaseVersion          string `json:"release_version,omitempty"`
	NodeOfflineAfterSeconds int    `json:"node_offline_after_seconds,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read controller config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("decode controller config: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8088"
	}
	if cfg.StatePath == "" {
		cfg.StatePath = "data/controller-state.json"
	}
	if cfg.AdminUsername == "" {
		cfg.AdminUsername = "admin"
	}
	if cfg.AdminPasswordHash == "" {
		cfg.AdminPasswordHash = strings.TrimSpace(os.Getenv("XMESH_ADMIN_PASSWORD_HASH"))
	}
	if cfg.AdminPasswordHash == "" {
		return cfg, errors.New("admin password hash is required (config or XMESH_ADMIN_PASSWORD_HASH)")
	}
	secret, err := base64.RawURLEncoding.DecodeString(cfg.SessionSecret)
	if err != nil || len(secret) < 32 {
		return cfg, errors.New("session_secret must be at least 32 random bytes encoded as base64url without padding")
	}
	if cfg.PublicURL == "" {
		return cfg, errors.New("public_url is required")
	}
	if cfg.NodeOfflineAfterSeconds <= 0 {
		cfg.NodeOfflineAfterSeconds = 45
	}
	return cfg, nil
}

func (c Config) sessionKey() []byte {
	b, _ := base64.RawURLEncoding.DecodeString(c.SessionSecret)
	return b
}
