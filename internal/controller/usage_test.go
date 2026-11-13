package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/model"
)

func TestGrantUsageWindowsAndRestart(t *testing.T) {
	s := model.NewState()
	s.Users["u"] = model.User{ID: "u", Name: "user"}
	s.Grants["g"] = model.Grant{ID: "g", UserID: "u", AttachmentID: "r"}
	s.Links["l1"] = model.Link{ID: "l1", Name: "first", AttachmentID: "r"}
	s.Links["l2"] = model.Link{ID: "l2", Name: "second", AttachmentID: "r"}
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	report := func(at time.Time, instance string, a, b uint64) {
		t.Helper()
		if err := sampleGrantUsage(&s, instance, "g", []model.GrantLinkUsage{{LinkID: "l1", UploadBytes: a, DownloadBytes: a * 2}, {LinkID: "l2", UploadBytes: b, DownloadBytes: b * 2}}, at); err != nil {
			t.Fatal(err)
		}
	}
	report(now.Add(-8*time.Hour), "old", 100, 100)
	report(now.Add(-7*time.Hour), "old", 200, 150)
	report(now.Add(-time.Hour), "old", 300, 250)
	report(now.Add(-time.Hour), "old", 300, 250)    // retry is idempotent
	report(now.Add(-30*time.Minute), "new", 10, 10) // new process starts at zero
	report(now.Add(-20*time.Minute), "new", 30, 20)
	view := usageViews(s, now)[0]
	if got := view.Windows["5h"]; got.Upload != 250 || got.Download != 500 {
		t.Fatalf("5h: %+v", got)
	}
	if got := view.Windows["1d"]; got.Upload != 600 || got.Download != 1200 {
		t.Fatalf("1d: %+v", got)
	}
	if len(view.Links) != 2 || view.Links[0].Windows["5h"].Upload != 130 || view.Links[1].Windows["5h"].Upload != 120 {
		t.Fatalf("per Link: %+v", view.Links)
	}
	if err := sampleGrantUsage(&s, "new", "g", []model.GrantLinkUsage{{LinkID: "other", UploadBytes: 1}}, now); err == nil {
		t.Fatal("accepted unrelated Link")
	}
	removeGrant(&s, "g")
	removeLink(&s, "l1")
	if got := usageViews(s, now)[0].Windows["1d"].Upload; got != 600 {
		t.Fatalf("deleting authorization erased history: %d", got)
	}
}

func TestUsageIgnoresOlderCountersWithoutMovingBaseline(t *testing.T) {
	for _, retired := range []bool{false, true} {
		for _, older := range []model.GrantLinkUsage{
			{LinkID: "link", UploadBytes: 50, DownloadBytes: 100},
			{LinkID: "link", UploadBytes: 90, DownloadBytes: 230},
		} {
			s, state := testServer(t, securityState)
			now := time.Now().UTC()
			s.now = func() time.Time { return now }
			report := func(instance string, up, down uint64) {
				t.Helper()
				payload, _ := json.Marshal(StatusReport{Status: model.NodeStatus{InstanceID: instance}, Grants: []model.GrantStatus{{GrantID: "grant", Links: []model.GrantLinkUsage{{LinkID: "link", UploadBytes: up, DownloadBytes: down}}}}})
				r := httptest.NewRequest(http.MethodPost, "/api/v1/status", bytes.NewReader(payload))
				r.Header.Set("Authorization", "Bearer gateway-secret")
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				if w.Code != 204 {
					t.Fatalf("status report: %d %s", w.Code, w.Body.String())
				}
			}
			check := func(up, down uint64) {
				t.Helper()
				for _, user := range usageViews(state.Snapshot(), now) {
					if user.UserID == "u" {
						if got := user.Windows["5h"]; got.Upload != up || got.Download != down {
							t.Fatalf("retired=%v older=%+v usage=%+v want=%d/%d", retired, older, got, up, down)
						}
						return
					}
				}
				t.Fatal("user usage missing")
			}
			report("process-1", 100, 200)
			if retired {
				if err := state.Update(func(st *model.State) error { removeGrant(st, "grant"); return nil }); err != nil {
					t.Fatal(err)
				}
			}
			report("process-1", older.UploadBytes, older.DownloadBytes)
			check(100, 200)
			report("process-1", 150, 300)
			report("process-1", 150, 300)
			check(150, 300)
			report("process-2", 10, 20)
			report("process-2", 10, 20)
			check(160, 320)
			report("process-2", 5, 10)
			report("process-2", 20, 40)
			check(170, 340)
		}
	}
}

func TestUsageLinksWithSameNameHaveStableOrder(t *testing.T) {
	st := model.NewState()
	st.Users["u"] = model.User{ID: "u"}
	st.Grants["grant"] = model.Grant{ID: "grant", UserID: "u", AttachmentID: "route"}
	for _, id := range []string{"z", "a"} {
		st.Links[id] = model.Link{ID: id, Name: "相同名称", AttachmentID: "route"}
	}
	for range 30 {
		links := usageViews(st, time.Now())[0].Links
		if len(links) != 2 || links[0].ID != "a" || links[1].ID != "z" {
			t.Fatalf("same-name links jump between refreshes: %+v", links)
		}
	}
}
