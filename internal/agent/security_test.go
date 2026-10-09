package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xtaci/smux"
	"xmesh/internal/controller"
	"xmesh/internal/model"
	"xmesh/internal/protocol"
	"xmesh/internal/runtimecfg"
)

func securityAgentConfig() controller.AgentConfig {
	return controller.AgentConfig{Agent: model.Agent{ID: "a", Enabled: true, AllowedCIDRs: []string{"127.0.0.0/8"}}, Links: []controller.AgentLinkConfig{{Link: model.Link{ID: "link-a", AttachmentID: "route-a", Enabled: true}, GatewayID: "gateway-a", GrantIDs: []string{"grant-a"}}, {Link: model.Link{ID: "link-b", AttachmentID: "route-b", Enabled: true}, GatewayID: "gateway-b", GrantIDs: []string{"grant-b"}}}, GrantIDs: []string{"grant-a", "grant-b"}}
}

func TestAgentAuthorizationRejectedExpiredAndDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unauthorized", 401) }))
	defer server.Close()
	r := New(runtimecfg.Config{NodeID: "a", ControllerURL: server.URL, Credential: "old"}, slog.Default())
	if err := r.ApplyConfig(securityAgentConfig()); err != nil {
		t.Fatal(err)
	}
	if !r.grantAllowed("link-a", "grant-a") || r.grantAllowed("link-a", "grant-b") {
		t.Fatal("incorrect route scope")
	}
	r.authorizationUntil = time.Now().Add(-time.Second)
	if r.grantAllowed("link-a", "grant-a") {
		t.Fatal("expired grant accepted")
	}
	if err := r.ApplyConfig(securityAgentConfig()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.workers["worker"] = cancel
	if err := r.refresh(context.Background()); err == nil {
		t.Fatal("401 not returned")
	}
	if ctx.Err() == nil || r.grantAllowed("link-a", "grant-a") || len(r.config.Links) != 0 {
		t.Fatal("rejected identity retained authorization")
	}
	if err := r.ApplyConfig(controller.AgentConfig{Agent: model.Agent{ID: "a"}}); err != nil {
		t.Fatal("disabled config requires an access policy", err)
	}
}

func TestAgentDoesNotRestoreConfigFetchedBeforeRevocation(t *testing.T) {
	r := New(runtimecfg.Config{NodeID: "a"}, slog.Default())
	if err := r.ApplyConfig(securityAgentConfig()); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		r.invalidateAuthorization()
		_ = json.NewEncoder(w).Encode(securityAgentConfig())
	}))
	defer server.Close()
	r.client.ControllerURL = server.URL
	if err := r.refresh(context.Background()); err == nil {
		t.Fatal("configuration fetched before revocation was accepted")
	}
	if r.grantAllowed("link-a", "grant-a") {
		t.Fatal("revocation was undone by an in-flight configuration request")
	}
}

func TestAgentRejectsCrossRouteGrantOverSMux(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		c, err := target.Accept()
		if err == nil {
			defer c.Close()
			_, _ = io.Copy(c, c)
		}
	}()
	r := New(runtimecfg.Config{NodeID: "a"}, slog.Default())
	if err := r.ApplyConfig(securityAgentConfig()); err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	config, err := protocol.NewSMuxConfig()
	if err != nil {
		t.Fatal(err)
	}
	server, err := smux.Server(left, config)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := smux.Client(right, config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	r.workers["worker"] = workerCancel
	go func() {
		for {
			stream, err := client.AcceptStream()
			if err != nil {
				return
			}
			go r.handleStream(workerCtx, "link-a", stream)
		}
	}()
	for _, test := range []struct {
		id      string
		allowed bool
	}{{"missing", false}, {"grant-b", false}, {"grant-a", true}} {
		stream, err := server.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
		if err := protocol.WriteMessage(stream, protocol.Message{Type: protocol.TypeTCP, Version: protocol.Version, GrantID: test.id, Host: "127.0.0.1", Port: target.Addr().(*net.TCPAddr).Port}); err != nil {
			t.Fatal(err)
		}
		response, err := protocol.ReadMessage(stream)
		if err != nil {
			t.Fatal(err)
		}
		if response.Success != test.allowed {
			t.Fatalf("grant %s: %+v", test.id, response)
		}
		if test.allowed {
			payload := []byte("authorized-route")
			if _, err := stream.Write(payload); err != nil {
				t.Fatal(err)
			}
			reply := make([]byte, len(payload))
			if _, err := io.ReadFull(stream, reply); err != nil {
				t.Fatal(err)
			}
			if string(reply) != string(payload) {
				t.Fatal(string(reply))
			}
			r.invalidateAuthorization()
			_ = stream.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := stream.Read(make([]byte, 1)); err == nil {
				t.Fatal("revoked Agent stream remained open")
			} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("worker cancellation did not close established Agent traffic")
			}
		}
		_ = stream.Close()
	}
}

func TestAgentExpiredAuthorizationStopsWorkers(t *testing.T) {
	r := New(runtimecfg.Config{NodeID: "a"}, slog.Default())
	if err := r.ApplyConfig(securityAgentConfig()); err != nil {
		t.Fatal(err)
	}
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	r.mu.Lock()
	r.workers["worker"] = workerCancel
	r.authorizationUntil = time.Now().Add(-time.Second)
	r.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.watchAuthorization(ctx)
	select {
	case <-workerCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("expired authorization did not stop workers")
	}
	if r.grantAllowed("link-a", "grant-a") {
		t.Fatal("expired worker retained grant authorization")
	}
}
