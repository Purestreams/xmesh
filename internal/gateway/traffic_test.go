package gateway

import (
	"testing"

	"xmesh/internal/runtimecfg"
)

func TestTrafficSnapshotAggregatesGrantAndLinkOnce(t *testing.T) {
	r := New(runtimecfg.Config{}, nil)
	for _, grantID := range []string{"first", "second"} {
		grant := r.grantCounters(grantID)
		link := r.grantLinkCounters(grantID, "shared")
		grant.upload.Add(100)
		grant.download.Add(200)
		link.upload.Add(100)
		link.download.Add(200)
	}
	grants, links, upload, download := r.trafficSnapshot()
	if len(grants) != 2 || len(links) != 1 || upload != 200 || download != 400 {
		t.Fatalf("totals: grants=%d links=%d up=%d down=%d", len(grants), len(links), upload, download)
	}
	if got := links["shared"]; got.UploadBytes != 200 || got.DownloadBytes != 400 {
		t.Fatalf("Link usage: %+v", got)
	}
	for _, grant := range grants {
		if len(grant.Links) != 1 || grant.Links[0].UploadBytes != 100 {
			t.Fatalf("grant usage: %+v", grant)
		}
	}
}
