package controller

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/Purestreams/xmesh/internal/model"
)

func TestSetUserStateRetriesPreserveAccessScope(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		if err := securityState(s); err != nil {
			return err
		}
		route := s.Attachments["route"]
		route.ID = "route2"
		s.Attachments[route.ID] = route
		grant := s.Grants["grant"]
		grant.ID, grant.AttachmentID, grant.VMessUUID = "disabled", route.ID, "00000000-0000-4000-8000-000000000002"
		grant.Enabled, grant.Published = false, false
		s.Grants[grant.ID] = grant
		return nil
	})
	before := state.Snapshot()
	post := func(value string) {
		t.Helper()
		response := postNodeForm("/admin/users/u/state", url.Values{"enabled": {value}}, server.setUserState)
		if response.Code != http.StatusSeeOther {
			t.Fatalf("set state: %d %s", response.Code, response.Body.String())
		}
	}
	post("false")
	disabled := state.Snapshot()
	if disabled.Users["u"].Enabled || disabled.Grants["grant"].Published || disabled.Gateways["g"].DesiredVersion != before.Gateways["g"].DesiredVersion+1 {
		t.Fatal("user was not disabled with one configuration change per Gateway")
	}
	if disabled.Users["other"] != before.Users["other"] || disabled.Grants["other"] != before.Grants["other"] || !reflect.DeepEqual(disabled.Attachments, before.Attachments) || !reflect.DeepEqual(disabled.Links, before.Links) {
		t.Fatal("disabling a user changed another user or shared routes")
	}
	for _, id := range []string{"grant", "disabled"} {
		want := before.Grants[id]
		want.Published = false
		want.AccessVersion = disabled.Gateways["g"].DesiredVersion
		if disabled.Grants[id] != want {
			t.Fatal("user state change altered per-route access or credentials")
		}
	}
	post("false")
	retried := state.Snapshot()
	if retried.Users["u"].Enabled || retried.Gateways["g"].DesiredVersion != disabled.Gateways["g"].DesiredVersion {
		t.Fatal("retry re-enabled the user or issued another configuration update")
	}
	post("true")
	restored := state.Snapshot()
	if !restored.Users["u"].Enabled || restored.Grants["disabled"].Enabled || restored.Grants["grant"].Published || restored.Gateways["g"].DesiredVersion != disabled.Gateways["g"].DesiredVersion+1 {
		t.Fatal("restoring a user changed disabled grants or skipped Gateway acknowledgement")
	}
	post("true")
	if state.Snapshot().Gateways["g"].DesiredVersion != restored.Gateways["g"].DesiredVersion {
		t.Fatal("repeated enable triggered another configuration update")
	}
}

func TestSetUserStateRejectsInvalidRequests(t *testing.T) {
	server, state := testServer(t, securityState)
	before := state.Snapshot()
	for _, value := range []string{"", "toggle", "0"} {
		response := postNodeForm("/admin/users/u/state", url.Values{"enabled": {value}}, server.setUserState)
		if response.Code != http.StatusBadRequest || !reflect.DeepEqual(state.Snapshot(), before) {
			t.Fatal("invalid state changed user access", response.Code)
		}
	}
	response := postNodeForm("/admin/users/missing/state", url.Values{"enabled": {"false"}}, server.setUserState)
	if response.Code != http.StatusNotFound || !reflect.DeepEqual(state.Snapshot(), before) {
		t.Fatal("unknown user changed access", response.Code)
	}
}
