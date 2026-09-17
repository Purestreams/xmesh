package controller

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"xmesh/internal/model"
)

func TestSubscriptionAndPanelSortByDisplayedNodeName(t *testing.T) {
	state := model.NewState()
	state.Users["u"] = model.User{ID: "u", Enabled: true}
	for _, gateway := range []model.Gateway{
		{ID: "g1", Name: "Tencent-BJ-10M", Enabled: true},
		{ID: "g2", Name: "HK-Mega", Enabled: true},
		{ID: "g3", Name: "alpha", Enabled: true},
	} {
		state.Gateways[gateway.ID] = gateway
	}
	for _, agent := range []model.Agent{
		{ID: "a1", Name: "CN-Guangzhou-CT-30M", Enabled: true},
		{ID: "a2", Name: "CN-Beijing-Tencent-10M", Enabled: true},
		{ID: "a3", Name: "HK-HGC-1Gbps", Enabled: true},
	} {
		state.Agents[agent.ID] = agent
	}
	for _, route := range []model.Attachment{
		{ID: "1", GatewayID: "g1", AgentID: "a1", Enabled: true},
		{ID: "2", GatewayID: "g2", AgentID: "a1", Enabled: true},
		{ID: "3", GatewayID: "g1", AgentID: "a2", Enabled: true},
		{ID: "4", GatewayID: "g2", AgentID: "a2", Enabled: true},
		{ID: "5", GatewayID: "g2", AgentID: "a3", Enabled: true},
		{ID: "6", GatewayID: "g3", AgentID: "a3", Enabled: true},
		{ID: "7", GatewayID: "g2", AgentID: "a2", Enabled: true},
	} {
		state.Attachments[route.ID] = route
		state.Links[route.ID] = model.Link{ID: route.ID, AttachmentID: route.ID, Enabled: true}
		state.Grants[route.ID] = model.Grant{ID: route.ID, UserID: "u", AttachmentID: route.ID, VMessUUID: route.ID, Enabled: true, Published: true}
	}
	wantIDs := []string{"6", "4", "7", "2", "5", "3", "1"}
	wantNames := []string{"alpha / HK-HGC-1Gbps", "HK-Mega / CN-Beijing-Tencent-10M", "HK-Mega / CN-Beijing-Tencent-10M", "HK-Mega / CN-Guangzhou-CT-30M", "HK-Mega / HK-HGC-1Gbps", "Tencent-BJ-10M / CN-Beijing-Tencent-10M", "Tencent-BJ-10M / CN-Guangzhou-CT-30M"}
	for range 10 {
		var panelIDs []string
		for _, route := range sortedAttachments(state) {
			panelIDs = append(panelIDs, route.ID)
		}
		if !reflect.DeepEqual(panelIDs, wantIDs) {
			t.Fatalf("panel order: %v", panelIDs)
		}
		payload, err := BuildSubscription(state, "u")
		if err != nil {
			t.Fatal(err)
		}
		plain, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			t.Fatal(err)
		}
		var gotIDs, gotNames []string
		for _, line := range strings.Split(string(plain), "\n") {
			encoded, ok := strings.CutPrefix(line, "vmess://")
			if !ok {
				t.Fatal("invalid subscription entry")
			}
			decoded, err := base64.RawStdEncoding.DecodeString(encoded)
			if err != nil {
				t.Fatal(err)
			}
			var share vmessShare
			if err := json.Unmarshal(decoded, &share); err != nil {
				t.Fatal(err)
			}
			gotIDs, gotNames = append(gotIDs, share.ID), append(gotNames, share.Name)
		}
		if !reflect.DeepEqual(gotIDs, wantIDs) || !reflect.DeepEqual(gotNames, wantNames) {
			t.Fatalf("subscription order: %v %v", gotIDs, gotNames)
		}
	}
}
