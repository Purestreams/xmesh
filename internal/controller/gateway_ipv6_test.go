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

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/hy2"
	"github.com/Purestreams/xmesh/internal/model"
)

func TestGatewayDualStackSubscriptionsAndExplicitAddressMode(t *testing.T) {
	const v6 = "2606:4700:4700::1111"
	s, db := testServer(t, nil)
	if err := db.Update(func(state *model.State) error {
		state.NodeStatus["g"] = model.NodeStatus{NodeID: "g", BinaryVersion: gatewayIPv6Minimum}
		state.Users["u"] = model.User{ID: "u", Enabled: true}
		state.Agents["a"] = model.Agent{ID: "a", Name: "Agent", Enabled: true, DesiredVersion: 1}
		state.Attachments["r"] = model.Attachment{ID: "r", GatewayID: "g", AgentID: "a", Enabled: true}
		state.Links["l"] = model.Link{ID: "l", AttachmentID: "r", URL: "reality://manual.example:8443/tunnel", AutoURL: true, Enabled: true}
		state.Grants["grant"] = model.Grant{ID: "grant", UserID: "u", AttachmentID: "r", VMessUUID: "00000000-0000-4000-8000-000000000001", Enabled: true, Published: true}
		gateway := model.Gateway{ID: "g", Name: "Gateway", PublicHost: "manual.example", VMessPort: 8080, VMessPath: "/proxy", Enabled: true, DesiredVersion: 1, CredentialHash: auth.SecretHash("key")}
		if err := configureHY2(state, &gateway, 8080, s.now()); err != nil {
			return err
		}
		state.Gateways["g"] = gateway
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	announce := func(report model.PublicIPReport) int {
		body, _ := json.Marshal(report)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/gateway/address", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer key")
		w := httptest.NewRecorder()
		s.gatewayAddress(w, r)
		return w.Code
	}
	report := model.PublicIPReport{IP: "8.8.8.8", Sources: []string{"ip.sb", "ipify"}, IPv6: v6, IPv6Sources: []string{"ip.sb", "ifconfig.co"}}
	pin := db.Snapshot().Gateways["g"].HY2PinSHA256
	if code := announce(report); code != 204 {
		t.Fatal("dual-stack report", code)
	}
	if g := db.Snapshot().Gateways["g"]; g.PublicHost != "manual.example" || g.DetectedPublicIPv6 != v6 || g.DesiredVersion != 1 || g.HY2PinSHA256 != pin {
		t.Fatal("disabled IPv6 or manual mode was modified", g)
	}
	form := url.Values{"name": {"Gateway"}, "public_host": {"manual.example"}, "vmess_port": {"8080"}, "vmess_path": {"/proxy"}, "address_controls": {"1"}, "public_host_auto": {"1"}, "ipv6_ingress_enabled": {"1"}}
	edit := func() {
		t.Helper()
		w := postNodeForm("/admin/gateways/g/edit", form, s.editGateway)
		if w.Code != 303 {
			t.Fatalf("edit %d %s", w.Code, w.Body.String())
		}
	}
	edit()
	g := db.Snapshot().Gateways["g"]
	if !g.PublicHostAuto || g.ManualPublicHost != "manual.example" || g.PublicHost != "8.8.8.8" || g.IPv6IngressHost() != v6 {
		t.Fatal("explicit auto mode did not ignore the retained manual value", g)
	}
	for _, host := range []string{"8.8.8.8", v6} {
		if err := hy2.Validate(g.HY2Certificate, g.HY2PrivateKey, g.HY2PinSHA256, host, s.now()); err != nil {
			t.Fatal(err)
		}
	}
	subscription := func() []string {
		t.Helper()
		payload, err := BuildSubscription(db.Snapshot(), "u")
		if err != nil {
			t.Fatal(err)
		}
		plain, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			t.Fatal(err)
		}
		if len(plain) == 0 {
			return nil
		}
		return strings.Split(string(plain), "\n")
	}
	if len(subscription()) != 0 {
		t.Fatal("unapplied IPv6 configuration was published")
	}
	ack := func(version uint64, ipv6Ready bool) {
		t.Helper()
		body, _ := json.Marshal(StatusReport{Status: model.NodeStatus{NodeID: "g", BinaryVersion: gatewayIPv6Minimum, Role: model.RoleGateway, Online: true, Ready: true, XrayReady: true, HY2Ready: true, IngressControls: true, IPv6IngressReady: ipv6Ready, AppliedVersion: version}})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/status", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer key")
		w := httptest.NewRecorder()
		s.nodeStatus(w, r)
		if w.Code != 204 {
			t.Fatalf("ack %d %s", w.Code, w.Body.String())
		}
	}
	ack(g.DesiredVersion-1, true)
	if len(subscription()) != 0 {
		t.Fatal("old acknowledgement published new addresses")
	}
	ack(g.DesiredVersion, false)
	if len(subscription()) != 2 {
		t.Fatal("a Gateway without IPv6 support published IPv6 nodes")
	}
	legacyBundle := httptest.NewRecorder()
	s.hy2ClientBundle(legacyBundle, httptest.NewRequest(http.MethodGet, "/?grant=grant&family=6", nil), db.Snapshot(), "u")
	if legacyBundle.Code != http.StatusForbidden {
		t.Fatal("a Gateway without IPv6 support exposed an IPv6 client bundle")
	}
	ack(g.DesiredVersion, true)
	lines := subscription()
	if len(lines) != 4 {
		t.Fatalf("expected IPv4+IPv6 VMess and HY2, got %v", lines)
	}
	counts := map[string]int{}
	for _, line := range lines {
		if strings.HasPrefix(line, "vmess://") {
			raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(line, "vmess://"))
			if err != nil {
				t.Fatal(err)
			}
			var share vmessShare
			if err := json.Unmarshal(raw, &share); err != nil {
				t.Fatal(err)
			}
			counts[share.Address]++
		} else {
			u, err := url.Parse(line)
			if err != nil {
				t.Fatal(err)
			}
			counts[u.Hostname()]++
			if u.Query().Get("pinSHA256") != g.HY2PinSHA256 || u.Query().Has("insecure") {
				t.Fatal("invalid pin", line)
			}
		}
	}
	if counts["8.8.8.8"] != 2 || counts[v6] != 2 {
		t.Fatal("missing address family", counts)
	}
	if err := db.Update(func(state *model.State) error {
		status := state.NodeStatus["g"]
		status.BinaryVersion = gatewayAutoIPMinimum
		state.NodeStatus["g"] = status
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(subscription()) != 2 {
		t.Fatal("older binary published IPv6 despite readiness flag")
	}
	rollbackBundle := httptest.NewRecorder()
	s.hy2ClientBundle(rollbackBundle, httptest.NewRequest(http.MethodGet, "/?grant=grant&family=6", nil), db.Snapshot(), "u")
	if rollbackBundle.Code != http.StatusForbidden {
		t.Fatal("older binary exposed IPv6 strict bundle")
	}
	ack(g.DesiredVersion, true)
	request := httptest.NewRequest(http.MethodGet, "/?grant=grant&family=6", nil)
	bundle := httptest.NewRecorder()
	s.hy2ClientBundle(bundle, request, db.Snapshot(), "u")
	if bundle.Code != 200 {
		t.Fatalf("IPv6 bundle %d %s", bundle.Code, bundle.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(bundle.Body.Bytes()), int64(bundle.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
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
		if bytes.Contains(data, []byte("PRIVATE KEY")) {
			t.Fatal("private key leaked")
		}
		if file.Name == "client.json" && !bytes.Contains(data, []byte("["+v6+"]:8080")) {
			t.Fatal("IPv6 client bundle uses wrong host", string(data))
		}
	}
	// IPv6-only report after an IPv4 query failure retains confirmed IPv4 nodes.
	revision, pin := g.DesiredVersion, g.HY2PinSHA256
	if announce(model.PublicIPReport{IP: v6, Sources: report.IPv6Sources}) != 204 {
		t.Fatal("IPv6-only report rejected")
	}
	if after := db.Snapshot().Gateways["g"]; after.PublicHost != "8.8.8.8" || after.DesiredVersion != revision || after.HY2PinSHA256 != pin {
		t.Fatal("IPv4 probe outage restarted or removed IPv4 ingress")
	}
	// Repeat confirmations and saving unchanged settings must not rotate identity.
	edit()
	if after := db.Snapshot().Gateways["g"]; after.DesiredVersion != revision || after.HY2PinSHA256 != pin {
		t.Fatal("unchanged dual-stack settings restarted the Gateway")
	}
	form.Del("public_host_auto")
	edit()
	if after := db.Snapshot().Gateways["g"]; after.PublicHostAuto || after.PublicHost != "manual.example" {
		t.Fatal("disabling automatic discovery did not restore the retained manual host")
	}
	form.Del("ipv6_ingress_enabled")
	edit()
	ack(db.Snapshot().Gateways["g"].DesiredVersion, false)
	if len(subscription()) != 2 {
		t.Fatal("IPv6 entries remained after disabling generation")
	}
	bundle = httptest.NewRecorder()
	s.hy2ClientBundle(bundle, request, db.Snapshot(), "u")
	if bundle.Code != http.StatusForbidden {
		t.Fatal("disabled IPv6 client bundle remained accessible")
	}
}

func TestGatewayExplicitAutoIgnoresInvalidManualInput(t *testing.T) {
	g := model.Gateway{PublicHost: "8.8.8.8", DetectedPublicIP: "9.9.9.9"}
	form := url.Values{"public_host": {"https://ignored.invalid:8443/path"}, "address_controls": {"1"}, "public_host_auto": {"1"}}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	host, err := gatewayAddressForm(r, &g)
	if err != nil || host != "9.9.9.9" || g.ManualPublicHost != form.Get("public_host") {
		t.Fatal("manual value was not ignored", host, err)
	}
	form.Del("public_host_auto")
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := gatewayAddressForm(r, &g); err == nil {
		t.Fatal("invalid host accepted in manual mode")
	}
}

func TestGatewayCreationExplicitAutoIgnoresManualAddress(t *testing.T) {
	for _, mode := range []string{"create", "quick-setup"} {
		t.Run(mode, func(t *testing.T) {
			s, db := testServer(t, nil)
			form := url.Values{"name": {"Auto"}, "gateway_name": {"Auto"}, "agent_name": {"Exit"}, "region": {"cn"}, "vmess_port": {"8080"}, "vmess_path": {"/proxy"}, "allowed_cidrs": {"0.0.0.0/0"}, "address_controls": {"1"}, "public_host_auto": {"1"}, "ipv6_ingress_enabled": {"1"}, "public_host": {"https://ignored.example:8443/"}}
			r := httptest.NewRequest(http.MethodPost, "/admin/test", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			if mode == "create" {
				s.createGateway(w, r)
			} else {
				s.quickSetup(w, r)
			}
			if w.Code != 303 {
				t.Fatalf("create %d %s", w.Code, w.Body.String())
			}
			for _, g := range db.Snapshot().Gateways {
				if !g.PublicHostAuto || !g.IPv6IngressEnabled || g.PublicHost != "" || g.ManualPublicHost != form.Get("public_host") {
					t.Fatal("manual value took effect in automatic mode", g)
				}
			}
			for _, link := range db.Snapshot().Links {
				if link.URL != "" || !link.AutoURL {
					t.Fatal("ignored manual address leaked into automatic Link", link)
				}
			}
		})
	}
}

func TestGatewayRejectsUnconfirmedIPv6WithoutPartialUpdate(t *testing.T) {
	s, db := testServer(t, func(state *model.State) error {
		state.Gateways["g"] = model.Gateway{ID: "g", Enabled: true, PublicHostAuto: true, IPv6IngressEnabled: true, PublicHost: "8.8.8.8", CredentialHash: auth.SecretHash("key"), DesiredVersion: 1}
		return nil
	})
	for _, bad := range []model.PublicIPReport{
		{IPv6: "8.8.8.8", IPv6Sources: []string{"ip.sb", "ipify"}},
		{IPv6: "fd00::1", IPv6Sources: []string{"ip.sb", "ipify"}},
		{IPv6: "2606:4700:4700::1111", IPv6Sources: []string{"ip.sb", "ip.sb"}},
		{IPv6: "2606:4700:4700::1111", IPv6Sources: []string{"ip.sb", "unknown"}},
		{IPv6Sources: []string{"ip.sb", "ipify"}},
	} {
		bad.IP = "9.9.9.9"
		bad.Sources = []string{"ip.sb", "ipify"}
		body, _ := json.Marshal(bad)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/gateway/address", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer key")
		w := httptest.NewRecorder()
		s.gatewayAddress(w, r)
		g := db.Snapshot().Gateways["g"]
		if w.Code != 400 || g.PublicHost != "8.8.8.8" || g.DesiredVersion != 1 || g.DetectedPublicIP != "" || g.DetectedPublicIPv6 != "" {
			t.Fatal("invalid IPv6 partially applied", w.Code, g)
		}
	}
}
