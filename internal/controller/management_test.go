package controller

import (
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/model"
)

func TestManagementRevisionTracksFormsWithoutHeartbeatChurn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes bool
		mutate  func(*model.State)
	}{
		{"rename", true, func(st *model.State) { g := st.Gateways["g"]; g.Name = "new"; st.Gateways["g"] = g }},
		{"policy", true, func(st *model.State) {
			a := st.Agents["a"]
			a.AllowedCIDRs = []string{"10.0.0.0/8"}
			st.Agents["a"] = a
		}},
		{"link policy", true, func(st *model.State) { l := st.Links["l"]; l.Weight = 3; st.Links["l"] = l }},
		{"deletion", true, func(st *model.State) { delete(st.Agents, "a") }},
		{"heartbeat", false, func(st *model.State) {
			st.NodeStatus["g"] = model.NodeStatus{Online: true, LastSeen: time.Now(), TCPConnections: 5}
			st.LinkStatus["g/l"] = model.LinkStatus{UploadBytes: 100, RTTMillis: 35}
		}},
		{"address checks", false, func(st *model.State) {
			g := st.Gateways["g"]
			g.PublicIPCheckedAt = time.Now()
			g.PublicIPv6CheckedAt = time.Now()
			g.ObservedPublicIP = "203.0.113.1"
			st.Gateways["g"] = g
		}},
		{"probe results", false, func(st *model.State) {
			r := st.Attachments["r"]
			r.ProbeRequestID = "new-probe"
			r.ProbeRequestedAt = time.Now()
			r.UpstreamProbe = &model.UpstreamProbeStatus{CompletedAt: time.Now()}
			r.UpstreamProbeFailureSince = time.Now()
			r.UpstreamProbeFailureVersion = 2
			st.Attachments["r"] = r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := model.NewState()
			st.Gateways["g"] = model.Gateway{ID: "g", Name: "gateway", PublicHost: "edge.example"}
			st.Agents["a"] = model.Agent{ID: "a", AllowedCIDRs: []string{"0.0.0.0/0"}}
			st.Attachments["r"] = model.Attachment{ID: "r", GatewayID: "g", AgentID: "a"}
			st.Links["l"] = model.Link{ID: "l", AttachmentID: "r", Weight: 1}
			before := managementRevision(st)
			tc.mutate(&st)
			if changed := managementRevision(st) != before; changed != tc.changes {
				t.Fatalf("changed=%v want=%v", changed, tc.changes)
			}
		})
	}
}
