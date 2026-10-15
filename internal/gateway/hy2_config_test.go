package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/controller"
	"github.com/Purestreams/xmesh/internal/hy2"
	"github.com/Purestreams/xmesh/internal/model"
)

func TestHY2SharedPortConfigRejectsMissingOrDuplicateCredentials(t *testing.T) {
	certificate, key, pin, err := hy2.Generate("edge.example", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	config := controller.GatewayConfig{Gateway: model.Gateway{PublicHost: "edge.example", VMessPort: 8080, VMessPath: "/proxy", HY2Port: 8080, HY2Certificate: certificate, HY2PrivateKey: key, HY2PinSHA256: pin}, Grants: []model.Grant{
		{ID: "a", Enabled: true, VMessUUID: "a", HY2Auth: "secret-a"}, {ID: "b", Enabled: true, VMessUUID: "b", HY2Auth: "secret-b"},
	}}
	payload, err := buildXrayConfig(config, "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	var rendered xrayConfig
	if err := json.Unmarshal(payload, &rendered); err != nil {
		t.Fatal(err)
	}
	if len(rendered.Inbounds) != 2 || rendered.Inbounds[0].Port != rendered.Inbounds[1].Port || rendered.Outbounds[0].Protocol != "blackhole" {
		t.Fatal("missing shared-port listeners or deny default")
	}
	if rendered.Inbounds[1].StreamSettings.HysteriaSettings.Masquerade.Type != "string" || rendered.Inbounds[1].StreamSettings.HysteriaSettings.Masquerade.StatusCode != 404 || strings.Contains(string(payload), "insecure") {
		t.Fatal("unsafe HY2 configuration")
	}
	config.Grants[1].HY2Auth = ""
	if _, err := buildXrayConfig(config, "127.0.0.1:18080"); err == nil {
		t.Fatal("empty credential accepted")
	}
	config.Grants[1].HY2Auth = config.Grants[0].HY2Auth
	if _, err := buildXrayConfig(config, "127.0.0.1:18080"); err == nil {
		t.Fatal("duplicate credential accepted")
	}
}

func TestIngressSwitchesRenderOnlyEnabledListeners(t *testing.T) {
	certificate, key, pin, err := hy2.Generate("edge.example", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, choice := range []struct{ vmess, hy2 bool }{{true, true}, {false, true}, {true, false}, {false, false}} {
		config := controller.GatewayConfig{Gateway: model.Gateway{PublicHost: "edge.example", VMessPort: 8080, VMessPath: "/proxy", VMessDisabled: !choice.vmess, HY2Disabled: !choice.hy2, HY2Port: 8080, HY2Certificate: certificate, HY2PrivateKey: key, HY2PinSHA256: pin}, Grants: []model.Grant{{ID: "grant", Enabled: true, VMessUUID: "uuid", HY2Auth: "secret"}}}
		payload, err := buildXrayConfig(config, "127.0.0.1:18080")
		if err != nil {
			t.Fatal(err)
		}
		var rendered xrayConfig
		if err := json.Unmarshal(payload, &rendered); err != nil {
			t.Fatal(err)
		}
		foundVMess, foundHY2 := false, false
		for _, inbound := range rendered.Inbounds {
			foundVMess = foundVMess || inbound.Protocol == "vmess"
			foundHY2 = foundHY2 || inbound.Protocol == "hysteria"
		}
		if foundVMess != choice.vmess || foundHY2 != choice.hy2 {
			t.Fatalf("wrong listeners for %+v: %s", choice, payload)
		}
		for _, rule := range rendered.Routing.Rules {
			for _, tag := range rule.InboundTag {
				if tag == "xmesh-vmess" && !choice.vmess || tag == "xmesh-hy2" && !choice.hy2 {
					t.Fatal("closed ingress has routing rule")
				}
			}
		}
	}
}
