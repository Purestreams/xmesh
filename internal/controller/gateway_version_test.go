package controller

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
)

func TestGatewayFeatureVersions(t *testing.T) {
	for _, tc := range []struct {
		version    string
		auto, ipv6 bool
	}{
		{"", false, false}, {"dev", false, false}, {"v0.4.5", false, false},
		{"v0.4.6", true, false}, {"v0.4.7", true, true}, {"0.4.7", true, true},
		{"v0.4.10", true, true}, {"v0.10.0", true, true}, {"v1.0.0", true, true},
		{"v0.4.7+build.1", true, true}, {"v0.4.7-rc.1", false, false},
		{"v0.4.70garbage", false, false}, {"v00.4.7", false, false},
		{"v0.4", false, false}, {"v999999999999999999999.4.7", false, false},
	} {
		if gatewayAutoIPSupported(tc.version) != tc.auto || gatewayIPv6Supported(tc.version) != tc.ipv6 {
			t.Errorf("unexpected feature support for %q", tc.version)
		}
	}
}

func TestGatewayAddressActivationChecksVersionAtomically(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		auto, ipv6    bool
		want          int
	}{
		{"unknown-auto", "", true, false, 400},
		{"old-auto", "v0.4.5", true, false, 400},
		{"supported-auto", "v0.4.6", true, false, 303},
		{"old-ipv6", "v0.4.6", false, true, 400},
		{"dev-ipv6", "dev", false, true, 400},
		{"supported-ipv6", "v0.4.7", false, true, 303},
		{"old-manual-edit", "v0.4.1", false, false, 303},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := testServer(t, func(state *model.State) error {
				state.Gateways["g"] = model.Gateway{ID: "g", Name: "Gateway", PublicHost: "edge.example", Enabled: true, VMessPort: 8080, VMessPath: "/proxy", DesiredVersion: 1, CredentialHash: "installed"}
				state.NodeStatus["g"] = model.NodeStatus{NodeID: "g", BinaryVersion: tc.version}
				return nil
			})
			before := db.Snapshot().Gateways["g"]
			form := url.Values{"name": {"Renamed"}, "public_host": {"edge.example"}, "vmess_port": {"8080"}, "vmess_path": {"/proxy"}, "address_controls": {"1"}}
			if tc.auto {
				form.Set("public_host_auto", "1")
			}
			if tc.ipv6 {
				form.Set("ipv6_ingress_enabled", "1")
			}
			w := postNodeForm("/admin/gateways/g/edit", form, s.editGateway)
			if w.Code != tc.want {
				t.Fatalf("edit = %d: %s", w.Code, w.Body.String())
			}
			if tc.want == 400 && !reflect.DeepEqual(before, db.Snapshot().Gateways["g"]) {
				t.Fatal("rejected feature activation modified the Gateway")
			}
		})
	}
}

func TestGatewayAddressAnnouncementChecksReportedVersion(t *testing.T) {
	for _, version := range []string{"", "dev", "v0.4.5", "v0.4.6", "v0.4.7"} {
		for _, extra := range []bool{false, true} {
			s, db := testServer(t, func(state *model.State) error {
				state.Gateways["g"] = model.Gateway{ID: "g", Enabled: true, PublicHost: "edge.example", PublicHostAuto: true, CredentialHash: auth.SecretHash("key"), DesiredVersion: 1}
				state.NodeStatus["g"] = model.NodeStatus{NodeID: "g", BinaryVersion: version, IPv6IngressReady: true}
				return nil
			})
			body := `{"ip":"8.8.8.8","sources":["ip.sb","ipify"]}`
			if extra {
				body = strings.TrimSuffix(body, "}") + `,"ipv6":"2606:4700:4700::1111","ipv6_sources":["ip.sb","ipify"]}`
			}
			r := httptest.NewRequest(http.MethodPost, "/api/v1/gateway/address", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer key")
			w := httptest.NewRecorder()
			s.gatewayAddress(w, r)
			want := http.StatusNoContent
			if !gatewayAutoIPSupported(version) || extra && !gatewayIPv6Supported(version) {
				want = http.StatusBadRequest
			}
			if w.Code != want {
				t.Fatalf("announce %q extra=%v = %d: %s", version, extra, w.Code, w.Body.String())
			}
			if want == 400 && db.Snapshot().Gateways["g"].PublicHost != "edge.example" {
				t.Fatal("rejected version changed address")
			}
		}
	}
}
