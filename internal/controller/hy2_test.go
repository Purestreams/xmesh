package controller

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/model"
)

func TestHY2ProvisioningSubscriptionBundleAndRevocation(t *testing.T) {
	s, db := testServer(t, securityState)
	if err := db.Update(func(state *model.State) error {
		gateway := state.Gateways["g"]
		if err := configureHY2(state, &gateway, 8080, s.now()); err != nil {
			return err
		}
		state.Gateways["g"] = gateway
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := db.Snapshot()
	if before.Grants["grant"].HY2Auth == "" || before.Grants["grant"].HY2Auth == before.Grants["other"].HY2Auth {
		t.Fatal("missing independent Grant credentials")
	}
	subscription, _ := BuildSubscription(before, "u")
	plain, _ := base64.StdEncoding.DecodeString(subscription)
	if strings.Contains(string(plain), "hysteria2://") {
		t.Fatal("unapplied HY2 published")
	}
	if err := db.Update(func(state *model.State) error {
		state.NodeStatus["g"] = model.NodeStatus{HY2Ready: true}
		state.GrantStatus["g/grant"] = model.GrantStatus{GrantID: "grant", UploadBytes: 123, DownloadBytes: 456}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	panelRequest := httptest.NewRequest("GET", "/", nil)
	panelRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.SignSession(s.cfg.adminSessionKey(), "admin", s.now().Add(time.Hour))})
	panelResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(panelResponse, panelRequest)
	if panelResponse.Code != 200 || !strings.Contains(panelResponse.Body.String(), "下载 HY2 严格验证客户端包") || !strings.Contains(panelResponse.Body.String(), "↑ 123 ↓ 456") {
		t.Fatal("HY2 panel broke Grant usage or client download")
	}
	subscription, _ = BuildSubscription(db.Snapshot(), "u")
	plain, _ = base64.StdEncoding.DecodeString(subscription)
	lines := strings.Split(string(plain), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected VMess and HY2, got %d", len(lines))
	}
	u, err := url.Parse(lines[1])
	if err != nil || u.Scheme != "hysteria2" || u.Host != "example.invalid:8080" || u.Query().Get("pinSHA256") != before.Gateways["g"].HY2PinSHA256 || u.Query().Has("insecure") {
		t.Fatalf("invalid HY2 subscription: %v", err)
	}
	request := httptest.NewRequest("GET", "/subscription/old-subscription-token?format=hysteria2&grant=grant", nil)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("bundle status %d: %s", response.Code, response.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name] = data
		if bytes.Contains(data, []byte("PRIVATE KEY")) || bytes.Contains(data, []byte(before.Grants["other"].HY2Auth)) {
			t.Fatal("bundle leaked another identity")
		}
	}
	var config map[string]any
	if err := json.Unmarshal(files["client.json"], &config); err != nil {
		t.Fatal(err)
	}
	if config["tls"].(map[string]any)["ca"] != "gateway.pem" || string(files["gateway.pem"]) != before.Gateways["g"].HY2Certificate || bytes.Contains(files["client.json"], []byte("insecure")) {
		t.Fatal("bundle lacks strict certificate trust")
	}
	foreign := httptest.NewRecorder()
	s.Handler().ServeHTTP(foreign, httptest.NewRequest("GET", "/subscription/old-subscription-token?format=hysteria2&grant=other", nil))
	if foreign.Code != 403 {
		t.Fatal("another user's Grant exported")
	}
	// A port-only edit preserves the identity; a host edit rotates it and unpublishes access.
	form := url.Values{"name": {"Edge"}, "public_host": {"example.invalid"}, "vmess_port": {"8080"}, "vmess_path": {"/proxy"}, "hy2_port": {"8443"}}
	if response := postNodeForm("/admin/gateways/g/edit", form, s.editGateway); response.Code != 303 {
		t.Fatal(response.Body.String())
	}
	after := db.Snapshot()
	if after.Gateways["g"].HY2PinSHA256 != before.Gateways["g"].HY2PinSHA256 || after.Grants["grant"].Published {
		t.Fatal("port edit changed identity or published prematurely")
	}
	form.Set("public_host", "new.example")
	if response := postNodeForm("/admin/gateways/g/edit", form, s.editGateway); response.Code != 303 {
		t.Fatal(response.Body.String())
	}
	if db.Snapshot().Gateways["g"].HY2PinSHA256 == before.Gateways["g"].HY2PinSHA256 {
		t.Fatal("host edit did not rotate identity")
	}
	request = httptest.NewRequest("POST", "/admin/users/u/reset-subscription", nil)
	request.SetPathValue("id", "u")
	s.resetSubscription(httptest.NewRecorder(), request)
	if db.Snapshot().Grants["grant"].HY2Auth == before.Grants["grant"].HY2Auth {
		t.Fatal("subscription reset retained HY2 credential")
	}
	response = httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/subscription/old-subscription-token?format=hysteria2&grant=grant", nil))
	if response.Code != 404 {
		t.Fatal("old subscription token survived reset")
	}
}
