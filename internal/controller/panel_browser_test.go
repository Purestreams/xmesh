package controller

import (
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"xmesh/internal/model"
)

// Opt-in local fixture for tests/panel.browser.cjs. It never opens production state.
func TestPanelBrowserFixture(t *testing.T) {
	address := os.Getenv("XMESH_PANEL_TEST_ADDR")
	if address == "" {
		t.Skip("set XMESH_PANEL_TEST_ADDR for browser tests")
	}
	now := time.Now().UTC()
	server, _ := testServer(t, func(s *model.State) error {
		for i, name := range []string{"Hong Kong Edge", "Singapore Edge", "Tokyo Standby"} {
			id := "g" + strconv.Itoa(i+1)
			s.Gateways[id] = model.Gateway{ID: id, Name: name, PublicHost: "edge" + strconv.Itoa(i+1) + ".example", RealityTarget: "target.example:443", VMessPort: 8080, VMessPath: "/proxy", Enabled: i != 2, DesiredVersion: 2, CredentialHash: "fixture-only"}
			s.NodeStatus[id] = model.NodeStatus{NodeID: id, Online: i == 0, Ready: i == 0, XrayReady: i == 0, AppliedVersion: 2, LastSeen: now, BinaryVersion: "v0.3.0", TCPConnections: 12, TunnelConnections: 2}
		}
		for i, name := range []string{"Home Network", "Office Network"} {
			id := "a" + strconv.Itoa(i+1)
			s.Agents[id] = model.Agent{ID: id, Name: name, Enabled: true, AllowedCIDRs: []string{"0.0.0.0/0"}, DesiredVersion: 2, CredentialHash: "fixture-only"}
			s.NodeStatus[id] = model.NodeStatus{NodeID: id, Online: true, Ready: true, AppliedVersion: 2, LastSeen: now, BinaryVersion: "v0.3.0", TunnelConnections: 2}
		}
		for i := range 2 {
			id := "r" + strconv.Itoa(i+1)
			gateway := "g" + strconv.Itoa(i+1)
			link := "l" + strconv.Itoa(i+1)
			s.Attachments[id] = model.Attachment{ID: id, GatewayID: gateway, AgentID: "a1", Enabled: true}
			s.Links[link] = model.Link{ID: link, AttachmentID: id, Name: "Route " + strconv.Itoa(i+1), URL: "reality://edge.example:8443/tunnel", Enabled: true, Priority: 10, Weight: 1, Connections: 2, MaxStreams: 256}
			for _, reporter := range []string{gateway, "a1"} {
				s.LinkStatus[reporter+"/"+link] = model.LinkStatus{LinkID: link, ReporterNodeID: reporter, Online: i == 0, Ready: i == 0, LastSeen: now, RTTMillis: float64(35 + i*20), Connections: 2, ActiveStreams: 6, UploadBytes: 23450000, DownloadBytes: 64320000}
			}
		}
		s.Users["u1"] = model.User{ID: "u1", Name: "Personal", Enabled: true, SubscriptionToken: "fixture-subscription"}
		s.Users["u2"] = model.User{ID: "u2", Name: "Team", Enabled: true, SubscriptionToken: "fixture-team"}
		s.Grants["grant1"] = model.Grant{ID: "grant1", UserID: "u1", AttachmentID: "r1", Enabled: true, Published: true}
		s.UsageHistory = map[string][]model.UsageBucket{"u1/l1": {{At: now.Truncate(5 * time.Minute), UploadBytes: 1024, DownloadBytes: 2048}}}
		s.LinkHistory = map[string][]model.LinkSample{}
		for i := range 60 {
			s.LinkHistory["l1"] = append(s.LinkHistory["l1"], model.LinkSample{At: now.Add(time.Duration(i-59) * 5 * time.Minute), Generation: 1, Ready: true, RTTMillis: float64(25 + i%9*4), UploadBytes: uint64(i * i * 240000), DownloadBytes: uint64(i * i * 740000)})
		}
		return nil
	})
	server.now = time.Now
	server.cfg.PublicURL = "http://" + address
	server.cfg.NodeOfflineAfterSeconds = 86400
	t.Logf("Browser fixture: http://%s (admin / a-strong-test-password)", address)
	if err := http.ListenAndServe(address, server.Handler()); err != nil {
		t.Fatal(err)
	}
}
