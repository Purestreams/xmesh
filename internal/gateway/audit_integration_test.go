package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	agentpkg "xmesh/internal/agent"
	"xmesh/internal/audit"
	"xmesh/internal/auth"
	"xmesh/internal/controller"
	"xmesh/internal/model"
	"xmesh/internal/nodeclient"
	"xmesh/internal/runtimecfg"
	"xmesh/internal/store"
)

func TestAuditTCPThroughGatewayAgentAndController(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	tunnel, socks := freeTCPAddress(t), freeTCPAddress(t)
	link := model.Link{ID: "link", AttachmentID: "route", URL: "ws://" + tunnel + "/tunnel", Enabled: true, Connections: 1, MaxStreams: 8, Weight: 1, Priority: 10}
	grant := model.Grant{ID: "grant", UserID: "u", AttachmentID: "route", Enabled: true, VMessUUID: "00000000-0000-4000-8000-000000000001", SOCKSUsername: "user", SOCKSPassword: "password"}
	if err = state.Update(func(st *model.State) error {
		st.Users["u"] = model.User{ID: "u", Name: "audited", Enabled: true, AuditEnabled: true, AuditGeneration: 1}
		st.Gateways["g"] = model.Gateway{ID: "g", Enabled: true, CredentialHash: auth.SecretHash("gateway"), VMessPort: 8080, VMessPath: "/proxy"}
		st.Agents["a"] = model.Agent{ID: "a", Name: "actual exit", Enabled: true, CredentialHash: auth.SecretHash("agent"), AllowedCIDRs: []string{"127.0.0.0/8"}}
		st.Attachments["route"] = model.Attachment{ID: "route", AgentID: "a", GatewayID: "g", Enabled: true}
		st.Links["link"] = link
		st.Grants["grant"] = grant
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte{7}, 32)
	cfg := controller.Config{PublicURL: "http://127.0.0.1", AdminUsername: "admin", AdminPasswordHash: "test-password-hash", SessionSecret: base64.RawURLEncoding.EncodeToString(secret)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	panel, err := controller.New(cfg, state, logger)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(panel.Handler())
	defer server.Close()
	r := New(runtimecfg.Config{NodeID: "g", ControllerURL: server.URL, Credential: "gateway", Gateway: runtimecfg.Gateway{SOCKSListen: socks, TunnelListen: tunnel, TunnelPath: "/tunnel", MaxUDPAssociations: 4}}, logger)
	config, err := nodeclient.FetchGateway(context.Background(), r.client)
	if err != nil {
		t.Fatal(err)
	}
	r.config = config
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.runTunnelServer(ctx)
	go r.runSOCKS(ctx)
	a := agentpkg.New(runtimecfg.Config{NodeID: "a", ControllerURL: server.URL, Credential: "agent"}, logger)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Agent did not stop")
		}
	})
	waitFor(t, 5*time.Second, func() bool {
		lease, err := r.pool.Acquire("a")
		if err != nil {
			return false
		}
		lease.Release()
		return true
	})
	target := startTCPEcho(t)
	defer target.Close()
	conn := dialSOCKS(t, socks, "user", "password", 1, target.Addr().String())
	payload := []byte("full-audit-path")
	if _, err = conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	cookie := auth.SignSession([]byte(auth.Derive(secret, "admin-session", cfg.AdminPasswordHash)), "admin", time.Now().Add(time.Hour))
	var final audit.Event
	waitFor(t, 6*time.Second, func() bool {
		request, _ := http.NewRequest("GET", server.URL+"/admin/users/u/audit/data", nil)
		request.AddCookie(&http.Cookie{Name: "xmesh_admin", Value: cookie})
		response, err := server.Client().Do(request)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return false
		}
		var data struct {
			View audit.View `json:"view"`
		}
		if json.NewDecoder(response.Body).Decode(&data) != nil {
			return false
		}
		for _, event := range data.View.Events {
			if event.Result == "closed" {
				final = event
				return data.View.Summary.Attempts == 1
			}
		}
		return false
	})
	if final.UserID != "u" || final.AgentID != "a" || final.GatewayID != "g" || final.LinkID != "link" || final.IP != "127.0.0.1" || final.UploadBytes != uint64(len(payload)) || final.DownloadBytes != uint64(len(payload)) {
		t.Fatalf("wrong end-to-end audit: %+v", final)
	}
}
