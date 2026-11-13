package controller

import (
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/model"
)

func TestDashboardRouteStateMatchesAvailabilityReason(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*model.State)
		want   string
	}{
		{"ready", func(*model.State) {}, "ready"},
		{"no public address", func(st *model.State) { g := st.Gateways["g"]; g.PublicHost = ""; st.Gateways["g"] = g }, "pending"},
		{"ingress protocols disabled", func(st *model.State) {
			g := st.Gateways["g"]
			g.VMessDisabled, g.HY2Disabled = true, true
			st.Gateways["g"] = g
		}, "disabled"},
		{"no transport", func(st *model.State) { l := st.Links["link"]; l.Enabled = false; st.Links["link"] = l }, "disabled"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s, _ := testServer(t, func(st *model.State) error {
				if err := securityState(st); err != nil {
					return err
				}
				now := time.Unix(1800000000, 0)
				for _, id := range []string{"g", "a"} {
					st.NodeStatus[id] = model.NodeStatus{NodeID: id, Online: true, Ready: true, XrayReady: true, AppliedVersion: 1, LastSeen: now}
					st.LinkStatus[id+"/link"] = model.LinkStatus{LinkID: "link", ReporterNodeID: id, Online: true, Ready: true, LastSeen: now}
				}
				scenario.change(st)
				return nil
			})
			route := dashboardRequest(t, s).Routes[0]
			if route.State != scenario.want || route.Issue.State != scenario.want {
				t.Fatalf("state %q and reason %+v disagree; want %q", route.State, route.Issue, scenario.want)
			}
		})
	}
}

func TestDashboardExternalRouteStateMatchesAvailabilityReason(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*model.State)
		want   string
	}{
		{"configured", func(*model.State) {}, "configured"},
		{"no public address", func(st *model.State) { g := st.Gateways["g"]; g.PublicHost = ""; st.Gateways["g"] = g }, "pending"},
		{"protocols disabled", func(st *model.State) {
			g := st.Gateways["g"]
			g.VMessDisabled, g.HY2Disabled = true, true
			st.Gateways["g"] = g
		}, "disabled"},
		{"exit disabled", func(st *model.State) { u := st.Upstreams["exit"]; u.Enabled = false; st.Upstreams["exit"] = u }, "disabled"},
		{"no selected exit", func(st *model.State) { u := st.Upstreams["exit"]; u.Endpoint.UUID = ""; st.Upstreams["exit"] = u }, "pending"},
		{"successful probe", func(st *model.State) {
			r := st.Attachments["route"]
			p := completedProbe(time.Unix(1800000000, 0), "")
			p.Revision = 1
			r.UpstreamProbe = &p
			st.Attachments["route"] = r
		}, "ready"},
		{"failed probe", func(st *model.State) {
			r := st.Attachments["route"]
			p := completedProbe(time.Unix(1800000000, 0), "")
			p.Revision = 1
			p.Targets[1].Success = false
			r.UpstreamProbe = &p
			st.Attachments["route"] = r
		}, "probe_failed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s, _ := testServer(t, func(st *model.State) error {
				if err := securityState(st); err != nil {
					return err
				}
				st.Attachments["route"] = model.Attachment{ID: "route", GatewayID: "g", UpstreamID: "exit", Enabled: true}
				st.Upstreams["exit"] = model.VMessUpstream{ID: "exit", Enabled: true, Endpoint: model.VMessEndpoint{UUID: "node", Address: "exit.example"}}
				st.NodeStatus["g"] = model.NodeStatus{NodeID: "g", Online: true, Ready: true, XrayReady: true, ExternalUpstreams: true, UpstreamProbes: true, AppliedVersion: 1, LastSeen: time.Unix(1800000000, 0)}
				scenario.change(st)
				return nil
			})
			route := dashboardRequest(t, s).Routes[0]
			if route.State != scenario.want || route.Issue.State != scenario.want {
				t.Fatalf("state %q and reason %+v disagree; want %q", route.State, route.Issue, scenario.want)
			}
		})
	}
}
