package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/controller"
	"github.com/Purestreams/xmesh/internal/model"
	"github.com/Purestreams/xmesh/internal/runtimecfg"
)

func TestSOCKSHandshakeStopsWithRuntimeContext(t *testing.T) {
	r := New(runtimecfg.Config{}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() { defer close(done); r.handleSOCKS(ctx, server, nil) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handshake retained a cancelled runtime")
	}
	if _, err := client.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("control socket not closed: %v", err)
	}
}

func TestXrayConfigWriteFailureRemainsRetryable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	r := New(runtimecfg.Config{NodeID: "g", Gateway: runtimecfg.Gateway{SOCKSListen: "127.0.0.1:18080", XrayConfigPath: filepath.Join(blocker, "gateway.json")}}, slog.Default())
	r.config = controller.GatewayConfig{Gateway: model.Gateway{ID: "g", Enabled: true, VMessPort: 8080}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	r.xrayApply <- struct{}{}
	go func() { done <- r.xrayLoop(ctx) }()
	waitFor(t, time.Second, func() bool { value, _ := r.xrayError.Load().(string); return value != "" })
	select {
	case err := <-done:
		t.Fatalf("temporary write failure killed component: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry loop did not stop")
	}
}

func TestRuntimeComponentFailureStopsSiblingListeners(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	tunnelAddress := freeTCPAddress(t)
	controllerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(controller.GatewayConfig{Gateway: model.Gateway{ID: "g", Enabled: false}})
	}))
	defer controllerServer.Close()
	r := New(runtimecfg.Config{NodeID: "g", ControllerURL: controllerServer.URL, Gateway: runtimecfg.Gateway{SOCKSListen: busy.Addr().String(), TunnelListen: tunnelAddress, TunnelPath: "/tunnel"}}, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.Run(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("component failure did not return promptly: %v", err)
	}
	listener, err := net.Listen("tcp", tunnelAddress)
	if err != nil {
		t.Fatalf("sibling tunnel listener survived Run: %v", err)
	}
	_ = listener.Close()
}

func TestUDPAssociationStopsWithRuntimeContext(t *testing.T) {
	existing, _ := securityGateway(t)
	address := freeTCPAddress(t)
	r := New(runtimecfg.Config{NodeID: "g", Gateway: runtimecfg.Gateway{SOCKSListen: address, MaxUDPAssociations: 4}}, slog.Default())
	r.config = existing.config
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.runSOCKS(ctx) }()
	waitFor(t, 3*time.Second, func() bool {
		conn, err := net.DialTimeout("tcp", address, 20*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
	control := dialSOCKS(t, address, "user", "pass", 3, "0.0.0.0:0")
	defer control.Close()
	cancel()
	_ = control.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := control.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("UDP control survived shutdown: %v", err)
	}
	waitFor(t, time.Second, func() bool { return r.udpAssociations.Load() == 0 })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS listener survived shutdown")
	}
}
