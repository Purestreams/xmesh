package controller

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
)

func TestMaintenanceDashboardIncludesEmbeddedNodeImpact(t *testing.T) {
	s, state := testServer(t, func(st *model.State) error {
		if err := securityState(st); err != nil {
			return err
		}
		st.Gateways["g2"] = model.Gateway{ID: "g2", Enabled: true}
		st.Agents["embedded"] = model.Agent{ID: "embedded", GatewayID: "g", Enabled: true}
		st.Attachments["embedded-route"] = model.Attachment{ID: "embedded-route", GatewayID: "g2", AgentID: "embedded", Enabled: true}
		st.Links["embedded-link"] = model.Link{ID: "embedded-link", AttachmentID: "embedded-route", Enabled: true}
		st.Grants["embedded-grant"] = model.Grant{ID: "embedded-grant", UserID: "u", AttachmentID: "embedded-route", Enabled: true}
		return nil
	})
	check := func(wantUsers int) {
		t.Helper()
		nodes := map[string]dashboardNode{}
		for _, node := range dashboardRequest(t, s).Nodes {
			nodes[node.ID] = node
		}
		if nodes["embedded"].GatewayID != "g" || nodes["a"].GatewayID != "" {
			t.Fatal("standalone and embedded Agents cannot be distinguished")
		}
		if got := nodes["g"].Impact; got != (deletionImpact{Assignments: 2, Links: 2, Grants: wantUsers + 1, Users: wantUsers}) {
			t.Fatalf("Gateway impact omits embedded routes or counts the same user twice: %+v", got)
		}
	}
	check(2)
	if err := state.Update(func(st *model.State) error { delete(st.Grants, "other"); return nil }); err != nil {
		t.Fatal(err)
	}
	check(1)
}

func TestMaintenanceHistoryNamesAndStableOrder(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s, state := testServer(t, func(st *model.State) error {
		st.Gateways["g"] = model.Gateway{ID: "g", Name: "用户的 <入口>", Enabled: true}
		st.UpgradeTasks["b"] = model.UpgradeTask{ID: "b", NodeID: "g", Role: model.RoleGateway, Stage: "succeeded", CreatedAt: now}
		st.UpgradeTasks["a"] = model.UpgradeTask{ID: "a", NodeID: "deleted-node", Role: model.RoleAgent, Stage: "succeeded", CreatedAt: now}
		st.UpgradeTasks["newest"] = model.UpgradeTask{ID: "newest", NodeID: "g", Role: model.RoleGateway, Stage: "succeeded", CreatedAt: now.Add(time.Second)}
		return nil
	})
	for range 30 {
		var ids []string
		for _, task := range sortedUpgradeTasks(state.Snapshot()) {
			ids = append(ids, task.ID)
		}
		if !reflect.DeepEqual(ids, []string{"newest", "a", "b"}) {
			t.Fatalf("same-batch records change order between refreshes: %v", ids)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.SignSession(s.cfg.adminSessionKey(), "admin", s.now().Add(time.Hour))})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	body := w.Body.String()
	start := strings.Index(body, `id="upgrade-task-table"`)
	if w.Code != 200 || start < 0 {
		t.Fatal("maintenance history missing", w.Code)
	}
	body = body[start:]
	for _, want := range []string{"用户的 &lt;入口&gt;", "deleted-node", `data-upgrade-task-id="b"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("history lost node identity %q", want)
		}
	}
}

func TestUpdaterViewsTrackPendingPairingUntilActivationOrExpiry(t *testing.T) {
	now := time.Unix(1800000000, 0)
	st := model.State{Gateways: map[string]model.Gateway{"g": {ID: "g"}}, Updaters: map[string]model.Updater{}}
	check := func(want string) {
		t.Helper()
		if got := updaterViews(st, now)["g"].State; got != want {
			t.Fatalf("pairing lifecycle status: got %q want %q", got, want)
		}
	}
	check("未配对")
	up := model.Updater{NodeID: "g", Role: model.RoleGateway, PairTokenHash: auth.SecretHash("pair"), PairExpiresAt: now.Add(time.Minute)}
	st.Updaters["g"] = up
	check("配对中")
	up.CredentialHash, up.LastSeen = auth.SecretHash("old-helper"), now
	st.Updaters["g"] = up
	check("配对中") // A healthy old helper must not hide the unfinished re-pair.
	up.PairExpiresAt = now
	st.Updaters["g"] = up
	check("在线")
	up.PairTokenHash, up.PairExpiresAt = "", time.Time{}
	up.CredentialHash = auth.SecretHash("new-helper")
	st.Updaters["g"] = up
	check("在线")
}
