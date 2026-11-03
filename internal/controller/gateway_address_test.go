package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/hy2"
	"github.com/Purestreams/xmesh/internal/model"
)

func TestNormalizeGatewayPublicHost(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		invalid     bool
	}{
		{"", "", false}, {" edge.example ", "edge.example", false},
		{"8.8.8.8", "8.8.8.8", false}, {"[2606:4700:4700::1111]", "2606:4700:4700::1111", false},
		{"::ffff:8.8.8.8", "8.8.8.8", false}, {"2606:4700:4700::1111", "2606:4700:4700::1111", false},
		{"https://edge.example", "", true}, {"edge.example:8443", "", true},
		{"[::1]:8443", "", true}, {"[edge.example]", "", true}, {"fe80::1%eth0", "", true},
	} {
		got, err := normalizeGatewayPublicHost(tc.input)
		if got != tc.want || (err != nil) != tc.invalid {
			t.Fatalf("normalize %q = %q, %v", tc.input, got, err)
		}
	}
}

func TestGatewayPublicHostValidationAcrossForms(t *testing.T) {
	for _, mode := range []string{"create", "edit", "quick-setup"} {
		for _, host := range []string{"https://edge.example", "edge.example:8443", "[2606:4700:4700::1111]"} {
			t.Run(mode+"/"+host, func(t *testing.T) {
				s, db := testServer(t, func(state *model.State) error {
					if mode == "edit" {
						state.Gateways["g"] = model.Gateway{ID: "g", Name: "Gateway", PublicHost: "old.example", VMessPort: 8080, VMessPath: "/proxy", DesiredVersion: 1}
					}
					return nil
				})
				form := url.Values{"name": {"Gateway"}, "gateway_name": {"Gateway"}, "agent_name": {"Agent"}, "public_host": {host}, "region": {"cn"}, "vmess_port": {"8080"}, "vmess_path": {"/proxy"}, "allowed_cidrs": {"0.0.0.0/0"}}
				r := httptest.NewRequest(http.MethodPost, "/admin/test", strings.NewReader(form.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.SetPathValue("id", "g")
				w := httptest.NewRecorder()
				switch mode {
				case "create":
					s.createGateway(w, r)
				case "edit":
					s.editGateway(w, r)
				default:
					s.quickSetup(w, r)
				}
				if strings.HasPrefix(host, "[") {
					if w.Code != http.StatusSeeOther {
						t.Fatalf("IPv6 rejected: %d %s", w.Code, w.Body.String())
					}
					for _, g := range db.Snapshot().Gateways {
						if g.PublicHost != "2606:4700:4700::1111" {
							t.Fatal("bracketed host persisted", g.PublicHost)
						}
					}
					for _, link := range db.Snapshot().Links {
						if link.URL != "reality://[2606:4700:4700::1111]:8443/tunnel" {
							t.Fatal("invalid IPv6 Link", link.URL)
						}
					}
				} else if w.Code != http.StatusBadRequest {
					t.Fatalf("invalid host accepted: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestGatewaySwitchToAutomaticKeepsWorkingAddressUntilConsensus(t *testing.T) {
	s, db := testServer(t, nil)
	if err := db.Update(func(state *model.State) error {
		gateway := model.Gateway{ID: "g", Name: "Gateway", PublicHost: "edge.example", Enabled: true, CredentialHash: auth.SecretHash("key"), VMessPort: 8080, VMessPath: "/proxy", DesiredVersion: 3}
		if err := configureHY2(state, &gateway, 8080, s.now()); err != nil {
			return err
		}
		state.NodeStatus[gateway.ID] = model.NodeStatus{NodeID: gateway.ID, BinaryVersion: gatewayAutoIPMinimum}
		state.Gateways[gateway.ID] = gateway
		state.Agents["a"] = model.Agent{ID: "a", DesiredVersion: 2}
		state.Attachments["r"] = model.Attachment{ID: "r", GatewayID: "g", AgentID: "a"}
		state.Links["l"] = model.Link{ID: "l", AttachmentID: "r", AutoURL: true, URL: "reality://edge.example:8443/tunnel"}
		state.Grants["grant"] = model.Grant{ID: "grant", AttachmentID: "r", Published: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pin := db.Snapshot().Gateways["g"].HY2PinSHA256
	w := postNodeForm("/admin/gateways/g/edit", url.Values{"name": {"Gateway"}, "public_host": {""}, "vmess_port": {"8080"}, "vmess_path": {"/proxy"}}, s.editGateway)
	if w.Code != http.StatusSeeOther {
		t.Fatal(w.Body.String())
	}
	state := db.Snapshot()
	g := state.Gateways["g"]
	if !g.PublicHostAuto || g.PublicHost != "edge.example" || g.DesiredVersion != 3 || g.HY2PinSHA256 != pin || state.Agents["a"].DesiredVersion != 2 || state.Links["l"].URL != "reality://edge.example:8443/tunnel" || !state.Grants["grant"].Published {
		t.Fatal("switching to automatic interrupted the working configuration")
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/gateway/address", strings.NewReader(`{"ip":"8.8.8.8","sources":["ip.sb","ipify"]}`))
	r.Header.Set("Authorization", "Bearer key")
	w = httptest.NewRecorder()
	s.gatewayAddress(w, r)
	state = db.Snapshot()
	if w.Code != http.StatusNoContent || state.Gateways["g"].PublicHost != "8.8.8.8" || state.Gateways["g"].DesiredVersion != 4 || state.Links["l"].URL != "reality://8.8.8.8:8443/tunnel" || state.Grants["grant"].Published {
		t.Fatalf("confirmed address was not applied: %d %s", w.Code, w.Body.String())
	}
}

func TestGatewayAutomaticAddressAndManualOverride(t *testing.T) {
	s, db := testServer(t, nil)
	form := url.Values{"gateway_name": {"Auto"}, "agent_name": {"Exit"}, "region": {"cn"}, "vmess_port": {"8080"}, "vmess_path": {"/proxy"}, "hy2_port": {"8080"}, "allowed_cidrs": {"0.0.0.0/0"}}
	request := httptest.NewRequest(http.MethodPost, "/admin/quick-setup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	created := httptest.NewRecorder()
	s.quickSetup(created, request)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("blank address create: %d %s", created.Code, created.Body.String())
	}
	var gatewayID, agentID, linkID string
	for id, g := range db.Snapshot().Gateways {
		gatewayID = id
		if !g.PublicHostAuto || g.PublicHost != "" || g.HY2Certificate != "" {
			t.Fatalf("unexpected initial address state: %+v", g)
		}
	}
	for id, link := range db.Snapshot().Links {
		linkID = id
		if !link.AutoURL || link.URL != "" {
			t.Fatalf("pending address leaked into a Link: %+v", link)
		}
	}
	pendingEdit := postNodeForm("/admin/links/"+linkID+"/edit", url.Values{"name": {"Pending"}, "url": {""}, "tls_verify": {"on"}}, s.editLink)
	if pendingEdit.Code != http.StatusSeeOther || !db.Snapshot().Links[linkID].AutoURL {
		t.Fatalf("editing a pending automatic Link: %d %s", pendingEdit.Code, pendingEdit.Body.String())
	}
	if err := db.Update(func(state *model.State) error {
		gateway := state.Gateways[gatewayID]
		gateway.CredentialHash = auth.SecretHash("gateway-key")
		state.NodeStatus[gatewayID] = model.NodeStatus{NodeID: gatewayID, BinaryVersion: gatewayAutoIPMinimum}
		state.Gateways[gatewayID] = gateway
		for id, agent := range state.Agents {
			agentID = id
			agent.CredentialHash = auth.SecretHash("agent-key")
			state.Agents[id] = agent
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	configRequest := func(key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.nodeConfig(w, r)
		return w
	}
	var pendingGateway GatewayConfig
	if err := json.Unmarshal(configRequest("gateway-key").Body.Bytes(), &pendingGateway); err != nil || pendingGateway.Gateway.HY2Port != 0 {
		t.Fatalf("pending Gateway cannot start without HY2 identity: %+v %v", pendingGateway, err)
	}
	var pendingAgent AgentConfig
	if err := json.Unmarshal(configRequest("agent-key").Body.Bytes(), &pendingAgent); err != nil || len(pendingAgent.Links) != 0 {
		t.Fatalf("pending Link was sent to Agent: %+v %v", pendingAgent, err)
	}
	announce := func(ip, credential string, sources ...string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(model.PublicIPReport{IP: ip, Sources: sources})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/gateway/address", strings.NewReader(string(body)))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("X-Forwarded-For", "8.8.8.8")
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		s.gatewayAddress(w, r)
		return w
	}
	for _, invalid := range []struct {
		ip, key string
		sources []string
	}{
		{"8.8.8.8", "agent-key", []string{"ip.sb", "ipify"}},
		{"8.8.8.8", "wrong", []string{"ip.sb", "ipify"}},
		{"8.8.8.8", "gateway-key", []string{"ip.sb", "ip.sb"}},
		{"10.0.0.1", "gateway-key", []string{"ip.sb", "ipify"}},
		{"8.8.8.8", "gateway-key", []string{"ip.sb", "unknown"}},
	} {
		if w := announce(invalid.ip, invalid.key, invalid.sources...); w.Code < 400 || db.Snapshot().Gateways[gatewayID].PublicHost != "" {
			t.Fatalf("invalid announcement accepted: %d %s", w.Code, w.Body.String())
		}
	}
	if w := announce("8.8.8.8", "gateway-key", "ipify", "ip.sb"); w.Code != 204 {
		t.Fatalf("announcement: %d %s", w.Code, w.Body.String())
	}
	first := db.Snapshot().Gateways[gatewayID]
	if first.PublicHost != "8.8.8.8" || first.DetectedPublicIP != "8.8.8.8" || first.ObservedPublicIP != "8.8.8.8" || first.PublicIPSources != "ip.sb, ipify" || first.DesiredVersion != 2 || db.Snapshot().Agents[agentID].DesiredVersion != 2 {
		t.Fatalf("automatic address not applied: %+v", first)
	}
	if err := hy2.Validate(first.HY2Certificate, first.HY2PrivateKey, first.HY2PinSHA256, first.PublicHost, s.now()); err != nil {
		t.Fatal(err)
	}
	if link := db.Snapshot().Links[linkID]; link.URL != "reality://8.8.8.8:8443/tunnel" {
		t.Fatal(link)
	}
	if w := announce("8.8.8.8", "gateway-key", "ip.sb", "ifconfig.co"); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	if repeated := db.Snapshot().Gateways[gatewayID]; repeated.DesiredVersion != first.DesiredVersion || repeated.HY2PinSHA256 != first.HY2PinSHA256 {
		t.Fatal("unchanged address rotated the certificate or restarted Gateway")
	}
	edit := func(host string) {
		w := postNodeForm("/admin/gateways/"+gatewayID+"/edit", url.Values{"name": {"Auto"}, "public_host": {host}, "vmess_port": {"8080"}, "vmess_path": {"/proxy"}}, s.editGateway)
		if w.Code != 303 {
			t.Fatalf("edit: %d %s", w.Code, w.Body.String())
		}
	}
	edit("edge.example")
	manual := db.Snapshot().Gateways[gatewayID]
	if w := announce("9.9.9.9", "gateway-key", "ip.sb", "ipify"); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	if g := db.Snapshot().Gateways[gatewayID]; g.PublicHostAuto || g.PublicHost != "edge.example" || g.DetectedPublicIP != "9.9.9.9" || g.DesiredVersion != manual.DesiredVersion || g.HY2PinSHA256 != manual.HY2PinSHA256 {
		t.Fatalf("manual override was replaced or restarted: %+v", g)
	}
	edit("")
	if g := db.Snapshot().Gateways[gatewayID]; !g.PublicHostAuto || g.PublicHost != "9.9.9.9" || db.Snapshot().Links[linkID].URL != "reality://9.9.9.9:8443/tunnel" {
		t.Fatalf("clearing override did not restore automatic address: %+v", g)
	}
	customURL := "reality://custom.example:8443/tunnel"
	w := postNodeForm("/admin/links/"+linkID+"/edit", url.Values{"name": {"Custom"}, "url": {customURL}}, s.editLink)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("custom Link: %d %s", w.Code, w.Body.String())
	}
	if w := announce("1.1.1.1", "gateway-key", "ip.sb", "ipify"); w.Code != http.StatusNoContent {
		t.Fatal(w.Body.String())
	}
	if link := db.Snapshot().Links[linkID]; link.AutoURL || link.URL != customURL {
		t.Fatal("automatic discovery replaced a manually edited Link")
	}
	if err := db.Update(func(state *model.State) error {
		pending := state.Links[linkID]
		pending.AutoURL, pending.URL = true, ""
		state.Links[linkID] = pending
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w = postNodeForm("/admin/links/"+linkID+"/edit", url.Values{"name": {"Custom"}, "url": {customURL}}, s.editLink)
	if w.Code != http.StatusSeeOther || db.Snapshot().Links[linkID].AutoURL {
		t.Fatalf("setting a manual URL on a pending REALITY Link: %d %s", w.Code, w.Body.String())
	}
	// An explicit override can happen to equal the generated URL. Legacy URL
	// inference must not turn it back into an automatic Link on the next change.
	w = postNodeForm("/admin/links/"+linkID+"/edit", url.Values{"name": {"Custom"}, "url": {"reality://1.1.1.1:8443/tunnel"}}, s.editLink)
	if w.Code != http.StatusSeeOther {
		t.Fatal(w.Body.String())
	}
	if w := announce("8.8.8.8", "gateway-key", "ip.sb", "ipify"); w.Code != http.StatusNoContent {
		t.Fatal(w.Body.String())
	}
	if link := db.Snapshot().Links[linkID]; link.URL != "reality://1.1.1.1:8443/tunnel" || !link.URLOverride {
		t.Fatal("legacy inference replaced an explicit override matching the old default")
	}
}

func TestGatewayObservedAddressTrustsOnlyConfiguredProxyHops(t *testing.T) {
	for _, tc := range []struct{ peer, forwarded, want string }{
		{"127.0.0.1:1234", "8.8.8.8", "8.8.8.8"},
		{"127.0.0.1:1234", "1.1.1.1, 8.8.8.8", "8.8.8.8"},
		{"8.8.8.8:1234", "1.1.1.1", "8.8.8.8"},
		{"127.0.0.1:1234", "garbage", ""},
		{"127.0.0.1:1234", "10.0.0.1", ""},
	} {
		s := &Server{}
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", tc.forwarded)
		if got := s.observedPublicIP(r); got != tc.want {
			t.Fatalf("peer=%s forwarded=%s got=%s want=%s", tc.peer, tc.forwarded, got, tc.want)
		}
	}
}
