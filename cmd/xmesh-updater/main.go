package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"xmesh/internal/updater"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("xmesh-updater requires Linux root")
	}
	if len(os.Args) < 2 {
		return errors.New("usage: xmesh-updater <run|pair|local> --config PATH [--mode systemd|docker] [--install-dir PATH]")
	}
	command := os.Args[1]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("config", "/etc/xmesh/updater.json", "root-only updater config")
	mode := flags.String("mode", "", "systemd or docker")
	installDir := flags.String("install-dir", "", "Docker installation directory")
	controller := flags.String("controller", "", "Controller HTTPS URL for pairing")
	nodeID := flags.String("node-id", "", "node ID for pairing")
	role := flags.String("role", "", "node role for pairing")
	tokenStdin := flags.Bool("token-stdin", false, "read pairing token on stdin")
	targetVersion := flags.String("version", "", "fixed target release for a local manual upgrade")
	archive := flags.String("archive", "", "verified local release archive")
	sha256sum := flags.String("sha256", "", "release archive SHA256")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	if command == "pair" {
		if !*tokenStdin || *controller == "" || *nodeID == "" || (*role != "gateway" && *role != "agent") {
			return errors.New("pair requires --controller, --node-id, --role and --token-stdin")
		}
		if !strings.HasPrefix(*controller, "https://") {
			return errors.New("Controller URL must use HTTPS")
		}
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
		if err != nil || len(b) > 4096 {
			return errors.New("invalid pairing token")
		}
		payload, _ := json.Marshal(map[string]string{"token": strings.TrimSpace(string(b)), "node_id": *nodeID, "role": *role})
		resp, err := (&http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("pairing redirect refused") }}).Post(strings.TrimSuffix(*controller, "/")+"/api/v1/updater/pair", "application/json", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("pairing failed: %s", resp.Status)
		}
		var result struct {
			Credential string `json:"credential"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result); err != nil {
			return err
		}
		cfg := updater.Config{ControllerURL: strings.TrimSuffix(*controller, "/"), NodeID: *nodeID, Role: *role, Credential: result.Credential, Mode: *mode, InstallDir: *installDir}
		return updater.SaveConfig(*path, cfg)
	}
	if command != "run" && command != "local" {
		return errors.New("unknown updater command")
	}
	cfg, err := updater.LoadConfig(*path)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if command == "local" {
		return updater.LocalUpgrade(ctx, cfg, *targetVersion, *archive, *sha256sum)
	}
	return updater.Run(ctx, cfg, version)
}
