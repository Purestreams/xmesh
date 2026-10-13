package agent

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xtaci/smux"
	"xmesh/internal/audit"
	"xmesh/internal/protocol"
	"xmesh/internal/runtimecfg"
)

func TestAgentAuditDefaultOffAndHotToggle(t *testing.T) {
	r := New(runtimecfg.Config{NodeID: "a"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cfg := securityAgentConfig()
	if err := r.ApplyConfig(cfg); err != nil {
		t.Fatal(err)
	}
	req := protocol.Message{GrantID: "grant-a", Host: "example.com", Port: 443, RequestID: "request"}
	if r.newAuditTrace("link-a", req) != nil {
		t.Fatal("audit enabled by default")
	}
	cfg.AuditGrants = map[string]audit.Policy{"grant-a": {Generation: 1}}
	if err := r.ApplyConfig(cfg); err != nil {
		t.Fatal(err)
	}
	trace := r.newAuditTrace("link-a", req)
	if trace == nil {
		t.Fatal("audit not enabled")
	}
	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.activeStreams[nil] = &streamAuthorization{linkID: "link-a", grantID: "grant-a", cancel: cancel, target: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 443}}
	if r.newAuditTrace("link-b", req) != nil {
		t.Fatal("cross-link grant audited")
	}
	trace.emit(audit.Event{Kind: "tcp", Result: "attempt"})
	if len(r.auditQueue) != 1 {
		t.Fatal("event not queued")
	}
	cfg.AuditGrants["grant-a"] = audit.Policy{Generation: 2}
	if err := r.ApplyConfig(cfg); err != nil {
		t.Fatal(err)
	}
	trace.emit(audit.Event{Kind: "tcp", Result: "closed"})
	if streamCtx.Err() != nil {
		t.Fatal("audit-only update interrupted an established stream")
	}
	if len(r.auditQueue) != 1 {
		t.Fatal("stale generation collected")
	}
	var reports int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reports++; w.WriteHeader(204) }))
	defer server.Close()
	r.client.ControllerURL = server.URL
	r.flushAudit(context.Background())
	if reports != 0 || len(r.auditQueue) != 0 {
		t.Fatal("old queued event survived generation update")
	}
}

func TestAgentAuditQueueBoundedAndReportLossCounted(t *testing.T) {
	r := New(runtimecfg.Config{NodeID: "a"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cfg := securityAgentConfig()
	cfg.AuditGrants = map[string]audit.Policy{"grant-a": {Generation: 1}}
	if err := r.ApplyConfig(cfg); err != nil {
		t.Fatal(err)
	}
	trace := r.newAuditTrace("link-a", protocol.Message{GrantID: "grant-a", Host: "example.com", Port: 443})
	for i := 0; i < audit.QueueSize+1; i++ {
		trace.emit(audit.Event{Kind: "tcp", Result: "attempt"})
	}
	if len(r.auditQueue) != audit.QueueSize || r.auditDrops[auditDropKey{"grant-a", 1}] != 1 {
		t.Fatal("queue is not bounded")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	r.client.ControllerURL = server.URL
	r.flushAudit(context.Background())
	if r.auditDrops[auditDropKey{"grant-a", 1}] != audit.MaxBatch+1 {
		t.Fatal("failed-report events not counted as lost")
	}
}

func TestAgentAuditRecordsRealDNSAndTCPBytes(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		conn, err := target.Accept()
		if err == nil {
			defer conn.Close()
			io.Copy(conn, conn)
		}
	}()
	r := New(runtimecfg.Config{NodeID: "a"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cfg := securityAgentConfig()
	cfg.AuditGrants = map[string]audit.Policy{"grant-a": {Generation: 1}}
	if err := r.ApplyConfig(cfg); err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	muxConfig, err := protocol.NewSMuxConfig()
	if err != nil {
		t.Fatal(err)
	}
	server, err := smux.Server(left, muxConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := smux.Client(right, muxConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		stream, err := client.AcceptStream()
		if err == nil {
			r.handleStream(ctx, "link-a", stream)
		}
	}()
	stream, err := server.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(5 * time.Second))
	req := protocol.Message{Type: protocol.TypeTCP, Version: protocol.Version, GrantID: "grant-a", RequestID: "connection", Host: "localhost", Port: target.Addr().(*net.TCPAddr).Port}
	if err := protocol.WriteMessage(stream, req); err != nil {
		t.Fatal(err)
	}
	response, err := protocol.ReadMessage(stream)
	if err != nil || !response.Success {
		t.Fatal("connection failed", err, response)
	}
	payload := []byte("audit-payload")
	if _, err = stream.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err = io.ReadFull(stream, reply); err != nil {
		t.Fatal(err)
	}
	stream.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connection did not close")
	}
	seen := map[string]audit.Event{}
	for len(r.auditQueue) > 0 {
		e := <-r.auditQueue
		if e.GrantID != "grant-a" || e.ConnectionID != "connection" {
			t.Fatal("wrong event attribution")
		}
		seen[e.Kind+"/"+e.Result] = e
	}
	if _, ok := seen["tcp/attempt"]; !ok {
		t.Fatal("missing attempt")
	}
	if e := seen["dns/resolved"]; len(e.IPs) == 0 || e.Host != "localhost" {
		t.Fatal("missing DNS result", e)
	}
	if e := seen["tcp/connected"]; e.IP != "127.0.0.1" {
		t.Fatal("wrong actual IP", e)
	}
	if e := seen["tcp/closed"]; e.UploadBytes != uint64(len(payload)) || e.DownloadBytes != uint64(len(payload)) {
		t.Fatal("wrong per-connection bytes", e)
	}
}
