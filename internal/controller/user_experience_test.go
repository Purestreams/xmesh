package controller

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/model"
)

func TestUserProfileAndRouteNameAreMetadataOnly(t *testing.T) {
	server, store := testServer(t, securityState)
	before := store.Snapshot()
	response := postNodeForm("/admin/users/u/edit", url.Values{"name": {" 客户 <甲> "}, "notes": {" 给客户的备注 "}}, server.editUser)
	if response.Code != http.StatusSeeOther {
		t.Fatal(response.Code, response.Body.String())
	}
	after := store.Snapshot()
	want := before.Users["u"]
	want.Name, want.Notes = "客户 <甲>", "给客户的备注"
	if after.Users["u"] != want || !reflect.DeepEqual(after.Grants, before.Grants) || !reflect.DeepEqual(after.Gateways, before.Gateways) {
		t.Fatal("metadata edit changed access or credentials")
	}
	response = postNodeForm("/admin/attachments/route/edit", url.Values{"name": {" 香港专线 <A> "}}, server.editRouteName)
	if response.Code != http.StatusSeeOther {
		t.Fatal(response.Code)
	}
	after = store.Snapshot()
	if routeDisplayName(after, after.Attachments["route"]) != "香港专线 <A>" || !reflect.DeepEqual(after.Gateways, before.Gateways) || !reflect.DeepEqual(after.Grants, before.Grants) {
		t.Fatal("route naming changed configuration")
	}
	payload, err := BuildSubscription(after, "u")
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := base64.StdEncoding.DecodeString(payload)
	// VMess labels are encoded inside each URI; verify the common naming source as well.
	found := false
	for _, line := range strings.Split(string(plain), "\n") {
		if strings.HasPrefix(line, "vmess://") {
			found = true
			raw, decodeErr := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(line, "vmess://"))
			if decodeErr != nil || !strings.Contains(string(raw), "香港专线") {
				t.Fatal("client label did not use route name")
			}
		}
	}
	if !found {
		t.Fatal("subscription did not contain the expected route")
	}
	before = after
	for _, value := range []string{"", strings.Repeat("名", 121), "bad\nname"} {
		response = postNodeForm("/admin/users/u/edit", url.Values{"name": {value}}, server.editUser)
		if response.Code != http.StatusBadRequest || !reflect.DeepEqual(store.Snapshot(), before) {
			t.Fatal("invalid profile edit changed state")
		}
	}
}

func TestArchivePreservesGrantsAndRestoresDisabled(t *testing.T) {
	server, store := testServer(t, securityState)
	before := store.Snapshot()
	response := postNodeForm("/admin/users/u/archive", url.Values{"archived": {"true"}}, server.archiveUser)
	if response.Code != http.StatusSeeOther {
		t.Fatal(response.Code, response.Body.String())
	}
	after := store.Snapshot()
	if !after.Users["u"].Archived || after.Users["u"].Enabled || after.Grants["grant"].Published || !after.Grants["grant"].Enabled || after.Grants["other"] != before.Grants["other"] || after.Gateways["g"].DesiredVersion != before.Gateways["g"].DesiredVersion+1 {
		t.Fatal("archive access scope incorrect")
	}
	if after.Grants["grant"].VMessUUID != before.Grants["grant"].VMessUUID || after.Users["u"].SubscriptionToken != before.Users["u"].SubscriptionToken {
		t.Fatal("archive rotated credentials")
	}
	response = postNodeForm("/admin/users/u/state", url.Values{"enabled": {"true"}}, server.setUserState)
	if response.Code != http.StatusBadRequest || !reflect.DeepEqual(store.Snapshot(), after) {
		t.Fatal("archived user was enabled")
	}
	response = postNodeForm("/admin/users/u/archive", url.Values{"archived": {"true"}}, server.archiveUser)
	retried := store.Snapshot()
	retried.Revision = after.Revision
	if response.Code != http.StatusSeeOther || !reflect.DeepEqual(retried, after) {
		t.Fatal("repeated archive changed state")
	}
	response = postNodeForm("/admin/users/u/archive", url.Values{"archived": {"false"}}, server.archiveUser)
	restored := store.Snapshot()
	if response.Code != http.StatusSeeOther || restored.Users["u"].Archived || restored.Users["u"].Enabled || !restored.Grants["grant"].Enabled || restored.Grants["grant"].Published {
		t.Fatal("restore implicitly enabled access")
	}
}

func TestDisableAcknowledgementTracksSpecificAccessChange(t *testing.T) {
	server, store := testServer(t, securityState)
	postNodeForm("/admin/grants/grant/state", url.Values{"enabled": {"false"}}, server.setGrantState)
	state := store.Snapshot()
	grant := state.Grants["grant"]
	if grant.AccessVersion != state.Gateways["g"].DesiredVersion || grantAccess(state, grant).State != "pending" {
		t.Fatal("disable not tracked as pending")
	}
	status := state.NodeStatus["g"]
	status.AppliedVersion = grant.AccessVersion
	state.NodeStatus["g"] = status
	gateway := state.Gateways["g"]
	gateway.DesiredVersion++
	state.Gateways["g"] = gateway
	if grantAccess(state, grant).State != "disabled" {
		t.Fatal("unrelated later config obscured acknowledgement")
	}
	status.ApplyError = "failed"
	state.NodeStatus["g"] = status
	if grantAccess(state, grant).State != "pending" {
		t.Fatal("configuration error was reported as confirmed")
	}
}

func TestGrantDeletionUsesHumanNamesAndIsScoped(t *testing.T) {
	server, store := testServer(t, securityState)
	before := store.Snapshot()
	response := postNodeForm("/admin/grants/grant/delete", url.Values{"confirm_name": {"wrong"}}, server.deleteGrant)
	if response.Code != http.StatusBadRequest || !reflect.DeepEqual(before, store.Snapshot()) {
		t.Fatal("wrong name deleted access")
	}
	name := before.Users["u"].Name + " / " + routeDisplayName(before, before.Attachments["route"])
	response = postNodeForm("/admin/grants/grant/delete", url.Values{"confirm_name": {name}}, server.deleteGrant)
	after := store.Snapshot()
	if response.Code != http.StatusSeeOther || len(after.Grants) != len(before.Grants)-1 || after.Grants["other"] != before.Grants["other"] || !reflect.DeepEqual(before.Links, after.Links) {
		t.Fatal("named deletion affected shared access")
	}
}

func TestSubscriptionDeliveryMatchesActualPayload(t *testing.T) {
	for _, scenario := range []string{"ready", "disabled-user", "archived", "disabled-route", "hy2-pending"} {
		t.Run(scenario, func(t *testing.T) {
			state := model.NewState()
			if err := securityState(&state); err != nil {
				t.Fatal(err)
			}
			user, route, gateway := state.Users["u"], state.Attachments["route"], state.Gateways["g"]
			switch scenario {
			case "disabled-user":
				user.Enabled = false
			case "archived":
				user.Archived = true
			case "disabled-route":
				route.Enabled = false
			case "hy2-pending":
				gateway.VMessDisabled = true
				gateway.HY2Port = 443
			}
			state.Users["u"], state.Attachments["route"], state.Gateways["g"] = user, route, gateway
			payload, err := BuildSubscription(state, "u")
			if scenario == "disabled-user" || scenario == "archived" {
				if err == nil {
					t.Fatal("unavailable user received a subscription")
				}
				payload = ""
			} else if err != nil {
				t.Fatal(err)
			}
			plain, err := base64.StdEncoding.DecodeString(payload)
			if err != nil {
				t.Fatal(err)
			}
			available := len(plain) > 0
			if grantImportable(state, state.Grants["grant"], time.Now()) != available {
				t.Fatal("delivery count does not match actual client payload")
			}
			want := "已发布 0 / 已授权 1"
			if available {
				want = "已发布 1 / 已授权 1"
			}
			if subscriptionCounts(state, time.Now())["u"] != want {
				t.Fatal("subscription summary overstates availability")
			}
		})
	}
}
