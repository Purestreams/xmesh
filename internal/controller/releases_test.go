package controller

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"xmesh/internal/model"
)

func TestReleaseAssetsAreFetchedOnceAndVerified(t *testing.T) {
	version := "v1.2.3"
	assets := map[string]string{}
	var manifest strings.Builder
	for _, name := range releaseAssets(version)[1:] {
		assets[name] = "content of " + name
		hash := sha256.Sum256([]byte(assets[name]))
		fmt.Fprintf(&manifest, "%x  %s\n", hash, name)
	}
	assets["SHA256SUMS"] = manifest.String()
	var fetches atomic.Int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		name := strings.TrimPrefix(r.URL.Path, "/"+version+"/")
		if name == "xmesh-"+version+"-linux-arm64.tar.gz" {
			_, _ = w.Write([]byte("corrupt download"))
			return
		}
		if payload, ok := assets[name]; ok {
			_, _ = w.Write([]byte(payload))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	server, _ := testServer(t, nil)
	server.cfg.ReleaseBaseURL = upstream.URL
	server.cfg.ReleaseVersion = version
	server.cfg.ReleaseDir = t.TempDir()
	server.releaseHTTPClient = upstream.Client()
	handler := server.Handler()
	requestAsset := func(name string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/releases/"+version+"/"+name, nil))
		return recorder
	}
	for i := 0; i < 2; i++ {
		response := requestAsset("install.sh")
		if response.Code != 200 || response.Body.String() != assets["install.sh"] {
			t.Fatalf("download %d: status=%d body=%q", i, response.Code, response.Body.String())
		}
	}
	if got := fetches.Load(); got != 2 { // manifest and install.sh, each once
		t.Fatalf("upstream fetched %d times, want 2", got)
	}
	if err := os.WriteFile(filepath.Join(server.releaseDirectory(), "install.sh"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if response := requestAsset("install.sh"); response.Code != 200 || response.Body.String() != assets["install.sh"] {
		t.Fatalf("tampered cache was not repaired: status=%d body=%q", response.Code, response.Body.String())
	}
	if got := fetches.Load(); got != 3 {
		t.Fatalf("tampered cache was not refetched: %d upstream requests", got)
	}
	if response := requestAsset("xmesh-" + version + "-linux-arm64.tar.gz"); response.Code != 502 {
		t.Fatalf("corrupt archive status=%d", response.Code)
	}
	if _, err := os.Stat(filepath.Join(server.releaseDirectory(), "xmesh-"+version+"-linux-arm64.tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("corrupt archive persisted: %v", err)
	}
	if response := requestAsset("unknown"); response.Code != 404 {
		t.Fatalf("unknown asset status=%d", response.Code)
	}
}

func TestReleaseManifestRejectsMissingOrDuplicateAssets(t *testing.T) {
	version := "v1"
	var manifest strings.Builder
	for _, name := range releaseAssets(version)[1:] {
		fmt.Fprintf(&manifest, "%064x  %s\n", 1, name)
	}
	if _, err := parseReleaseManifest(version, []byte(manifest.String())); err != nil {
		t.Fatal(err)
	}
	first := releaseAssets(version)[1]
	if _, err := parseReleaseManifest(version, []byte(manifest.String()+fmt.Sprintf("%064x  %s\n", 2, first))); err == nil {
		t.Fatal("duplicate asset accepted")
	}
	if _, err := parseReleaseManifest(version, []byte(strings.Replace(manifest.String(), first, "missing", 1))); err == nil {
		t.Fatal("unexpected asset accepted")
	}
}

func TestReleaseManifestAcceptsPackagedAssets(t *testing.T) {
	// Keep this list independent of releaseAssets: it mirrors package.sh's SHA256SUMS inputs.
	version := "v0.2.2"
	packaged := []string{
		"xmesh-" + version + "-linux-amd64.tar.gz",
		"xmesh-" + version + "-linux-arm64.tar.gz",
		"xmesh-" + version + "-windows-amd64.exe",
		"install.sh", "install-docker.sh", "install-controller.sh", "backup-controller.sh", "THIRD_PARTY_NOTICES.md",
	}
	assets := make(map[string]string, len(packaged)+1)
	var manifest strings.Builder
	for _, name := range packaged {
		assets[name] = "content of " + name
		hash := sha256.Sum256([]byte(assets[name]))
		fmt.Fprintf(&manifest, "%x  %s\n", hash, name)
	}
	if _, err := parseReleaseManifest(version, []byte(manifest.String())); err != nil {
		t.Fatalf("Controller rejected package.sh manifest: %v", err)
	}
	assets["SHA256SUMS"] = manifest.String()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/"+version+"/")
		if payload, ok := assets[name]; ok {
			_, _ = w.Write([]byte(payload))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	server, _ := testServer(t, nil)
	server.cfg.ReleaseBaseURL = upstream.URL
	server.cfg.ReleaseVersion = version
	server.cfg.ReleaseDir = t.TempDir()
	server.releaseHTTPClient = upstream.Client()
	for _, name := range []string{"SHA256SUMS", "install-docker.sh", "THIRD_PARTY_NOTICES.md"} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/releases/"+version+"/"+name, nil))
		if response.Code != http.StatusOK || response.Body.String() != assets[name] {
			t.Fatalf("cached %s: status=%d body=%q", name, response.Code, response.Body.String())
		}
	}
}

func TestEnrollmentOffersBothSourcesAndBothInstallModes(t *testing.T) {
	server, _ := testServer(t, func(s *model.State) error {
		s.Agents["a1"] = model.Agent{ID: "a1", Name: "Agent", Enabled: true}
		return nil
	})
	server.cfg.ReleaseBaseURL = "https://github.com/example/xmesh/releases/download"
	server.cfg.ReleaseVersion = "v1.2.3"
	server.cfg.ReleaseDir = t.TempDir()
	request := httptest.NewRequest(http.MethodPost, "/admin/enrollments", strings.NewReader("role=agent&node_id=a1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.createEnrollment(response, request)
	if response.Code != 200 {
		t.Fatalf("enrollment response=%d: %s", response.Code, response.Body.String())
	}
	command := response.Body.String()
	for _, fragment := range []string{"Controller on-demand cache", "GitHub release", "install.sh", "install-docker.sh", "--role 'agent'", "--release-base-url 'https://panel.example/releases'"} {
		if !strings.Contains(command, fragment) {
			t.Fatalf("missing %q from command", fragment)
		}
	}
	if strings.Contains(command, "--enrollment-token") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token leaked into command or response can be cached")
	}
	if strings.Contains(command, "--vmess-port") {
		t.Fatal("Agent command contains a Gateway-only port option")
	}
}

func TestGatewayInstallAndUpgradeCommandsUseConfiguredPort(t *testing.T) {
	server, _ := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Name: "Gateway", VMessPort: 8086, Enabled: true, CredentialHash: "existing-credential-hash"}
		return nil
	})
	server.cfg.ReleaseBaseURL = "https://github.com/example/xmesh/releases/download"
	server.cfg.ReleaseVersion = "v0.2.5"
	server.cfg.ReleaseDir = t.TempDir()
	request := httptest.NewRequest(http.MethodPost, "/admin/enrollments", strings.NewReader("role=gateway&node_id=g"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.createEnrollment(response, request)
	if response.Code != http.StatusOK || strings.Count(response.Body.String(), "'--vmess-port' '8086'") != 8 {
		t.Fatalf("Gateway install/rotation commands lost configured port: %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/admin/upgrade/gateway/g", nil)
	request.SetPathValue("role", "gateway")
	request.SetPathValue("id", "g")
	response = httptest.NewRecorder()
	server.upgradeOptions(response, request)
	if response.Code != http.StatusOK || strings.Count(response.Body.String(), "'--vmess-port' '8086'") != 4 {
		t.Fatalf("Gateway upgrade commands lost configured port: %d %s", response.Code, response.Body.String())
	}
}

func TestQuickSetupCreatesCompleteRouteAtomically(t *testing.T) {
	server, state := testServer(t, nil)
	form := url.Values{"gateway_name": {"Gateway"}, "agent_name": {"Agent"}, "public_host": {"gateway.example"}, "vmess_port": {"8081"}, "vmess_path": {"/vmess"}, "allowed_cidrs": {"10.0.0.0/8"}, "link_url": {"wss://gateway.example/tunnel"}}
	request := httptest.NewRequest(http.MethodPost, "/admin/quick-setup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.quickSetup(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("quick setup status=%d: %s", response.Code, response.Body.String())
	}
	snapshot := state.Snapshot()
	if len(snapshot.Gateways) != 1 || len(snapshot.Agents) != 1 || len(snapshot.Attachments) != 1 || len(snapshot.Links) != 1 {
		t.Fatalf("incomplete route: gateways=%d agents=%d attachments=%d links=%d", len(snapshot.Gateways), len(snapshot.Agents), len(snapshot.Attachments), len(snapshot.Links))
	}
	for _, link := range snapshot.Links {
		if !link.TLSVerify || link.TunnelTokenHash == "" {
			t.Fatal("quick route lacks TLS verification or tunnel credential")
		}
	}
	for _, gateway := range snapshot.Gateways {
		if gateway.VMessPort != 8081 || gateway.VMessPath != "/vmess" {
			t.Fatal("quick route lost Gateway settings")
		}
	}
	for _, agent := range snapshot.Agents {
		if len(agent.AllowedCIDRs) != 1 || agent.AllowedCIDRs[0] != "10.0.0.0/8" {
			t.Fatal("quick route lost Agent allowlist")
		}
	}
	bad := url.Values{"gateway_name": {"Bad"}, "agent_name": {"Bad"}, "public_host": {"example"}, "link_url": {"ws://example/tunnel"}}
	request = httptest.NewRequest(http.MethodPost, "/admin/quick-setup", strings.NewReader(bad.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	server.quickSetup(response, request)
	if response.Code != 400 || len(state.Snapshot().Gateways) != 1 {
		t.Fatal("invalid quick route changed state")
	}
}

func TestRegeneratingEnrollmentRevokesPreviousUnusedToken(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Gateways["g1"] = model.Gateway{ID: "g1", Enabled: true}
		return nil
	})
	server.cfg.ReleaseBaseURL = "https://github.com/example/xmesh/releases/download"
	server.cfg.ReleaseVersion = "v1.2.3"
	server.cfg.ReleaseDir = t.TempDir()
	for i := 0; i < 2; i++ {
		request := httptest.NewRequest(http.MethodPost, "/admin/enrollments", strings.NewReader("role=gateway&node_id=g1"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		server.createEnrollment(response, request)
		if response.Code != 200 {
			t.Fatalf("generate %d status=%d: %s", i, response.Code, response.Body.String())
		}
	}
	statuses := enrollmentStatuses(state.Snapshot(), server.now())
	active := 0
	for _, enrollment := range statuses {
		if enrollment.State == "active" {
			active++
		}
	}
	if len(statuses) != 2 || active != 1 {
		t.Fatalf("want one active token after regeneration, got %#v", statuses)
	}
	for _, enrollment := range statuses {
		if enrollment.State != "active" {
			continue
		}
		request := httptest.NewRequest(http.MethodPost, "/admin/enrollments/"+enrollment.ID+"/revoke", nil)
		request.SetPathValue("id", enrollment.ID)
		response := httptest.NewRecorder()
		server.revokeEnrollment(response, request)
		if response.Code != http.StatusSeeOther {
			t.Fatal("revoke did not expire active token")
		}
	}
	for _, enrollment := range enrollmentStatuses(state.Snapshot(), server.now()) {
		if enrollment.State == "active" {
			t.Fatal("active token remains after revoke")
		}
	}
}
