package controller

import (
	"testing"
	"time"

	"xmesh/internal/model"
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
