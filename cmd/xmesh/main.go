package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"xmesh/internal/agent"
	"xmesh/internal/auth"
	"xmesh/internal/controller"
	"xmesh/internal/gateway"
	"xmesh/internal/identity"
	"xmesh/internal/model"
	"xmesh/internal/runtimecfg"
	"xmesh/internal/store"
	"xmesh/internal/updater"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "controller":
		err = runController(logger, os.Args[2:])
	case "gateway":
		err = runNode(logger, model.RoleGateway, os.Args[2:])
	case "agent":
		err = runNode(logger, model.RoleAgent, os.Args[2:])
	case "hash-password":
		err = hashPassword()
	case "generate-secret":
		err = generateSecret()
	case "enroll":
		err = enrollNode(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		logger.Error("xmesh stopped", "error", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: xmesh <controller|gateway|agent|enroll|hash-password|generate-secret|version> [options]")
}

func runNode(logger *slog.Logger, role model.Role, args []string) error {
	flags := flag.NewFlagSet(string(role), flag.ContinueOnError)
	configPath := flags.String("config", "/etc/xmesh/node.json", "node config file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := runtimecfg.Load(*configPath)
	if err != nil {
		return err
	}
	if cfg.Role != role {
		return fmt.Errorf("config role is %s, command requires %s", cfg.Role, role)
	}
	cfg.BinaryVersion = version
	cfg.InstanceID, err = identity.Token(16)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch role {
	case model.RoleGateway:
		return gateway.New(cfg, logger).Run(ctx)
	case model.RoleAgent:
		return agent.New(cfg, logger).Run(ctx)
	default:
		return fmt.Errorf("unsupported role %q", role)
	}
}

func runController(logger *slog.Logger, args []string) error {
	flags := flag.NewFlagSet("controller", flag.ContinueOnError)
	configPath := flags.String("config", "/etc/xmesh/controller.json", "controller config file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := controller.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	state, err := store.Open(cfg.StatePath)
	if err != nil {
		return err
	}
	server, err := controller.New(cfg, state, logger)
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr: cfg.Listen, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: 2 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	logger.Info("controller listening", "address", cfg.Listen, "version", version)
	err = httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func hashPassword() error {
	password := os.Getenv("XMESH_ADMIN_PASSWORD")
	if password == "" {
		return fmt.Errorf("XMESH_ADMIN_PASSWORD is required")
	}
	hash, err := auth.PasswordHash(password)
	if err != nil {
		return err
	}
	fmt.Println(hash)
	return nil
}

func generateSecret() error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	fmt.Println(base64.RawURLEncoding.EncodeToString(b))
	return nil
}

func enrollNode(args []string) error {
	flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
	controllerURL := flags.String("controller", "", "controller public URL")
	token := flags.String("token", "", "one-time enrollment token")
	tokenStdin := flags.Bool("token-stdin", false, "read one-time enrollment token from stdin")
	replace := flags.Bool("replace", false, "rotate credentials in an existing node config")
	role := flags.String("role", "", "gateway or agent")
	output := flags.String("output", "/etc/xmesh/node.json", "node config output")
	updaterOutput := flags.String("updater-output", "", "root-only host updater configuration output")
	updaterMode := flags.String("updater-mode", "", "systemd or docker updater mode")
	updaterInstallDir := flags.String("updater-install-dir", "", "Docker node installation directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *controllerURL == "" || (*role != "gateway" && *role != "agent") {
		return fmt.Errorf("controller, token, and a valid role are required")
	}
	if *tokenStdin {
		if *token != "" {
			return fmt.Errorf("use either --token or --token-stdin")
		}
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
		if err != nil || len(b) > 4096 {
			return fmt.Errorf("invalid token on stdin")
		}
		*token = strings.TrimSpace(string(b))
	}
	if *token == "" {
		return fmt.Errorf("enrollment token is required")
	}
	var existing map[string]any
	if *replace {
		b, err := os.ReadFile(*output)
		if err != nil {
			return fmt.Errorf("read existing node identity: %w", err)
		}
		if err := json.Unmarshal(b, &existing); err != nil || existing["role"] != *role {
			return fmt.Errorf("invalid existing node identity")
		}
		if nodeID, ok := existing["node_id"].(string); !ok || nodeID == "" {
			return fmt.Errorf("invalid existing node identity")
		}
	} else if _, err := os.Stat(*output); err == nil {
		return fmt.Errorf("refusing to replace existing node identity at %s", *output)
	} else if !os.IsNotExist(err) {
		return err
	}
	wantUpdater := *updaterOutput != ""
	if wantUpdater {
		if *updaterMode != "systemd" && *updaterMode != "docker" {
			return fmt.Errorf("--updater-mode must be systemd or docker")
		}
		if *updaterMode == "docker" && !filepath.IsAbs(*updaterInstallDir) {
			return fmt.Errorf("--updater-install-dir must be absolute for Docker")
		}
		if *replace {
			if _, err := os.Stat(*updaterOutput); err == nil {
				wantUpdater = false // Business credential rotation keeps the paired helper identity.
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	request := controller.EnrollmentRequest{Token: *token, Role: model.Role(*role), WantUpdater: wantUpdater}
	if *replace {
		request.NodeID = existing["node_id"].(string)
	}
	payload, _ := json.Marshal(request)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Post(strings.TrimSuffix(*controllerURL, "/")+"/api/v1/enroll", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("enrollment failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var result controller.EnrollmentResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return err
	}
	if string(result.Role) != *role {
		return fmt.Errorf("enrollment role %s does not match requested role %s", result.Role, *role)
	}
	if wantUpdater {
		if result.UpdaterCredential == "" {
			return fmt.Errorf("controller did not issue updater credentials")
		}
		if err := updater.SaveConfig(*updaterOutput, updater.Config{ControllerURL: strings.TrimSuffix(*controllerURL, "/"), NodeID: result.NodeID, Role: *role, Credential: result.UpdaterCredential, Mode: *updaterMode, InstallDir: *updaterInstallDir}); err != nil {
			return err
		}
	}
	if *replace {
		if existing["node_id"] != result.NodeID {
			return fmt.Errorf("enrollment node %s does not match existing identity", result.NodeID)
		}
		existing["credential"] = result.Credential
		b, err := json.MarshalIndent(existing, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(*output), ".node-rotate-*")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return err
		}
		if _, err := file.Write(b); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		return os.Rename(file.Name(), *output)
	}
	config := map[string]any{"role": result.Role, "node_id": result.NodeID, "controller_url": strings.TrimSuffix(*controllerURL, "/"), "credential": result.Credential, "poll_interval": "15s", "status_interval": "10s", "gateway": map[string]any{"socks_listen": "127.0.0.1:18080", "tunnel_listen": "127.0.0.1:18081", "tunnel_path": "/tunnel", "xray_binary": "/usr/local/lib/xmesh/xray", "xray_config_path": "/var/lib/xmesh/xray.json", "reality_listen": "0.0.0.0:8443", "udp_idle_timeout": "2m", "max_udp_associations": 1024}}
	b, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(b); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
