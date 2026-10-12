package controller

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"xmesh/internal/model"
)

func TestGatewayDefaultsEnableBothProtocols(t *testing.T) {
	s, db := testServer(t, nil)
	response := postNodeForm("/admin/gateways/new/edit", url.Values{"name": {"Default"}, "public_host": {"edge.example"}, "vmess_port": {"8080"}}, s.createGateway)
	if response.Code != 303 {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	for _, gateway := range db.Snapshot().Gateways {
		if !gateway.VMessEnabled() || !gateway.HY2Enabled() || gateway.HY2Port != 8080 || gateway.HY2PinSHA256 == "" {
			t.Fatal("new Gateway did not default to both protocols")
		}
	}
}

func TestGatewayIngressSwitchesFilterSubscriptionsAndPreserveIdentity(t *testing.T) {
	s, db := testServer(t, securityState)
	if err := db.Update(func(state *model.State) error {
		gateway := state.Gateways["g"]
		if err := configureHY2(state, &gateway, 8080, s.now()); err != nil {
			return err
		}
		state.Gateways["g"] = gateway
		state.NodeStatus["g"] = model.NodeStatus{HY2Ready: true, IngressControls: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	identity := db.Snapshot().Gateways["g"]
	for _, test := range []struct {
		name       string
		vmess, hy2 bool
	}{{"both", true, true}, {"hy2-only", false, true}, {"vmess-only", true, false}, {"neither", false, false}, {"reenable", true, true}} {
		t.Run(test.name, func(t *testing.T) {
			before := db.Snapshot()
			form := url.Values{"ingress_controls": {"1"}}
			if test.vmess {
				form.Set("vmess_enabled", "1")
			}
			if test.hy2 {
				form.Set("hy2_enabled", "1")
			}
			response := postNodeForm("/admin/gateways/g/ingress", form, s.saveGatewayIngress)
			if response.Code != 303 {
				t.Fatalf("switch: %d %s", response.Code, response.Body.String())
			}
			after := db.Snapshot()
			gateway := after.Gateways["g"]
			if gateway.VMessEnabled() != test.vmess || gateway.HY2Enabled() != test.hy2 {
				t.Fatal("switch was not persisted")
			}
			if gateway.HY2Certificate != identity.HY2Certificate || gateway.HY2PrivateKey != identity.HY2PrivateKey || gateway.HY2PinSHA256 != identity.HY2PinSHA256 || gateway.HY2Port != identity.HY2Port {
				t.Fatal("switch replaced certificate or port")
			}
			if before.Gateways["g"].VMessDisabled != gateway.VMessDisabled || before.Gateways["g"].HY2Disabled != gateway.HY2Disabled {
				if gateway.DesiredVersion != before.Gateways["g"].DesiredVersion+1 || after.Grants["grant"].Published {
					t.Fatal("switch did not require a new applied revision")
				}
			}
			// Simulate acknowledgement to check protocol filtering independently.
			grant := after.Grants["grant"]
			grant.Published = true
			after.Grants["grant"] = grant
			body, err := BuildSubscription(after, "u")
			if err != nil {
				t.Fatal(err)
			}
			plain, err := base64.StdEncoding.DecodeString(body)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(plain), "vmess://") != test.vmess || strings.Contains(string(plain), "hysteria2://") != test.hy2 {
				t.Fatalf("wrong protocols published: %s", plain)
			}
			request := httptest.NewRequest("GET", "/subscription/old-subscription-token?format=hysteria2&grant=grant", nil)
			bundle := httptest.NewRecorder()
			s.hy2ClientBundle(bundle, request, after, "u")
			if test.hy2 && bundle.Code != 200 || !test.hy2 && bundle.Code != 403 {
				t.Fatal("disabled HY2 profile remained downloadable")
			}
			// Missing switches in old edit forms must preserve the existing choice.
			edit := url.Values{"name": {"Renamed"}, "public_host": {"example.invalid"}, "vmess_port": {"8080"}, "vmess_path": {"/proxy"}}
			if response := postNodeForm("/admin/gateways/g/edit", edit, s.editGateway); response.Code != 303 {
				t.Fatal(response.Body.String())
			}
			if updated := db.Snapshot().Gateways["g"]; updated.VMessEnabled() != test.vmess || updated.HY2Enabled() != test.hy2 {
				t.Fatal("metadata edit reset switches")
			}
		})
	}
	request := httptest.NewRequest("POST", "/admin/gateways/g/ingress", strings.NewReader("ingress_controls=1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != 303 || response.Header().Get("Location") != "/login" {
		t.Fatal("unauthenticated protocol change was not redirected to login")
	}
}

func TestLegacyGatewayCannotReceiveDisabledProtocolCredentials(t *testing.T) {
	s, db := testServer(t, securityState)
	if err := db.Update(func(state *model.State) error {
		gateway := state.Gateways["g"]
		gateway.VMessDisabled = true
		gateway.HY2Disabled = true
		gateway.HY2Port = 8080
		state.Gateways["g"] = gateway
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, supports := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
		request.Header.Set("Authorization", "Bearer gateway-secret")
		if supports {
			request.Header.Set("X-XMesh-Ingress-Controls", "1")
		}
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		var config GatewayConfig
		if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
			t.Fatal(err)
		}
		if !supports && (len(config.Grants) != 0 || config.Gateway.HY2Port != 0) {
			t.Fatal("legacy Gateway received closed ingress credentials")
		}
		if supports && len(config.Grants) == 0 {
			t.Fatal("capable Gateway lost its configuration")
		}
	}
}
