package audit

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testEvent(host string, port int, result string) Event {
	return Event{At: time.Now().UTC(), UserID: "u", GrantID: "grant", ConnectionID: "connection", LinkID: "link", Kind: "tcp", Host: host, Port: port, Result: result}
}

func TestPerUserStorageRotationQuotaExportAndClear(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	now := time.Now().UTC()
	// More than one segment, using small test quotas so eviction is exercised.
	for i := 0; i < 2200; i++ {
		e := testEvent(fmt.Sprintf("host-%04d.example", i), 443, "closed")
		e.Reason = strings.Repeat(fmt.Sprintf("%04d-", i), 95)
		if err := s.Append("u", 1_000_000, []Event{e}, 0, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Append("other", 1_000_000, []Event{testEvent("other.example", 80, "attempt")}, 0, now); err != nil {
		t.Fatal(err)
	}
	view, err := s.Read("u", 1_000_000, Filter{Limit: 200}, now)
	if err != nil {
		t.Fatal(err)
	}
	if view.Archives == 0 || view.StorageBytes > 1_000_000 || len(view.Events) != 200 {
		t.Fatalf("rotation/quota: %+v", view)
	}
	for _, e := range view.Events {
		if e.Host == "other.example" {
			t.Fatal("cross-user log leak")
		}
	}
	filtered, err := s.Read("u", 1_000_000, Filter{Host: "host-2199", Result: "closed", Port: 443}, now)
	if err != nil || len(filtered.Events) != 1 {
		t.Fatalf("filter: %v %+v", err, filtered.Events)
	}
	var archive bytes.Buffer
	if err = s.Export("u", &archive); err != nil {
		t.Fatal(err)
	}
	zipped, err := gzip.NewReader(&archive)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(zipped)
	zipped.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("host-2199.example")) || bytes.Contains(data, []byte("other.example")) {
		t.Fatal("incorrect export")
	}
	if err = s.Enforce("u", 400_000); err != nil {
		t.Fatal(err)
	}
	view, err = s.Read("u", 400_000, Filter{}, now)
	if err != nil || view.StorageBytes > 400_000 {
		t.Fatalf("reduced quota: %v %d", err, view.StorageBytes)
	}
	if err = s.Clear("u"); err != nil {
		t.Fatal(err)
	}
	view, err = s.Read("u", 400_000, Filter{}, now)
	if err != nil || len(view.Events) != 0 || view.StorageBytes != 0 {
		t.Fatal("clear failed", err)
	}
	other, err := s.Read("other", 1_000_000, Filter{}, now)
	if err != nil || len(other.Events) != 1 {
		t.Fatal("clear affected another user", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "u", "current.jsonl")); !os.IsNotExist(err) {
		t.Fatal("current log survived clear")
	}
}

func TestStatisticsAggregateAcrossGrantsAndExpire(t *testing.T) {
	s := NewStore(t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	events := []Event{}
	for i := 0; i < 120; i++ {
		e := testEvent(fmt.Sprintf("host-%d.example", i%30), 1000+i%20, "attempt")
		e.GrantID = fmt.Sprintf("grant-%d", i%2)
		events = append(events, e)
		if i < 30 {
			failed := e
			failed.Result = "failed"
			events = append(events, failed)
		}
		closed := e
		closed.Result = "closed"
		events = append(events, closed)
	}
	if err := s.Append("u", LimitBytes(0), events, 7, now); err != nil {
		t.Fatal(err)
	}
	view, err := s.Read("u", LimitBytes(0), Filter{}, now)
	if err != nil {
		t.Fatal(err)
	}
	st := view.Summary
	if st.Attempts != 120 || st.Failures != 30 || st.Targets != 30 || st.Ports != 20 || st.Dropped != 7 || len(st.Alerts) != 4 {
		t.Fatalf("incorrect statistics: %+v", st)
	}
	view, err = s.Read("u", LimitBytes(0), Filter{}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if view.Summary.Attempts != 0 || len(view.Summary.Alerts) != 0 || view.Summary.Dropped != 7 {
		t.Fatalf("window did not expire: %+v", view.Summary)
	}
	// Log files contain events, never mutable statistics/Controller state.
	data, err := os.ReadFile(filepath.Join(s.dir, "u", "current.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var e Event
	if err = json.Unmarshal(bytes.Split(data, []byte("\n"))[0], &e); err != nil {
		t.Fatal(err)
	}
}

func TestAuditStorageRejectsTraversal(t *testing.T) {
	s := NewStore(t.TempDir())
	for _, user := range []string{"../other", "..", "a/b", "a\\b", "", "x\r\ny"} {
		if err := s.Append(user, LimitBytes(0), []Event{testEvent("x", 443, "attempt")}, 0, time.Now()); err == nil {
			t.Fatalf("accepted %q", user)
		}
	}
}
