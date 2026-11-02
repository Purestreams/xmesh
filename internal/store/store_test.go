package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/model"
)

func TestPendingUpdateDoesNotBlockCommittedReads(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		done <- s.Update(func(state *model.State) error {
			state.Users["pending"] = model.User{ID: "pending"}
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	read := make(chan bool, 1)
	go func() {
		empty := false
		s.View(func(state *model.State) {
			empty = len(state.Users) == 0
		})
		read <- empty && len(s.Snapshot().Users) == 0
	}()
	select {
	case committed := <-read:
		if !committed {
			t.Fatal("read exposed an uncommitted update")
		}
	case <-time.After(time.Second):
		t.Fatal("a pending write blocked reads of the committed state")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Users["pending"].ID != "pending" {
		t.Fatal("successful update was not published")
	}
}

func TestConcurrentUpdatesPreserveAllCommits(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var writers sync.WaitGroup
	for _, id := range []string{"a", "b", "c", "d"} {
		writers.Go(func() {
			if err := s.Update(func(state *model.State) error {
				state.Users[id] = model.User{ID: id}
				return nil
			}); err != nil {
				t.Error(err)
			}
		})
	}
	writers.Wait()
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if state := reopened.Snapshot(); len(state.Users) != 4 || state.Revision != 4 {
		t.Fatalf("lost a concurrent commit: %+v", state)
	}
}

func TestUpdateIsAtomicOnCallbackError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(state *model.State) error {
		state.Users["kept"] = model.User{ID: "kept"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("no")
	if err := s.Update(func(state *model.State) error {
		delete(state.Users, "kept")
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("got %v, want %v", err, wantErr)
	}
	if _, ok := s.Snapshot().Users["kept"]; !ok {
		t.Fatal("failed update mutated live state")
	}
	if err := s.Update(func(state *model.State) error {
		state.Users["second"] = model.User{ID: "second"}
		return nil
	}); err != nil {
		t.Fatalf("replace existing state: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Snapshot().Users["kept"]; !ok {
		t.Fatal("persisted state lost committed user")
	}
	if _, ok := reopened.Snapshot().Users["second"]; !ok {
		t.Fatal("persisted state lost replacement update")
	}
}

func TestSnapshotAndFailedUpdateDoNotShareNestedState(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(state *model.State) error {
		state.Agents["a"] = model.Agent{ID: "a", AllowedCIDRs: []string{"127.0.0.0/8"}}
		state.Upstreams["u"] = model.VMessUpstream{ID: "u", Candidates: []model.UpstreamCandidate{{Name: "original"}}}
		state.RetiredGrants["g"] = model.RetiredGrant{LinkNames: map[string]string{"l": "original"}}
		state.NodeStatus["n"] = model.NodeStatus{FailureCounters: map[string]uint64{"failure": 1}}
		state.GrantStatus["g"] = model.GrantStatus{Links: []model.GrantLinkUsage{{LinkID: "l", UploadBytes: 1}}}
		state.UsageHistory = map[string][]model.UsageBucket{}
		state.UsageHistory["u/l"] = []model.UsageBucket{{UploadBytes: 1}}
		state.LinkHistory = map[string][]model.LinkSample{}
		state.LinkHistory["l"] = []model.LinkSample{{UploadBytes: 1}}
		state.UpgradeTasks["t"] = model.UpgradeTask{BaselineLinks: []string{"l"}}
		state.UpgradeBatches["b"] = model.UpgradeBatch{TaskIDs: []string{"t"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mutate := func(state *model.State) {
		agent := state.Agents["a"]
		agent.AllowedCIDRs[0] = "changed"
		upstream := state.Upstreams["u"]
		upstream.Candidates[0].Name = "changed"
		state.RetiredGrants["g"].LinkNames["l"] = "changed"
		state.NodeStatus["n"].FailureCounters["failure"] = 2
		grant := state.GrantStatus["g"]
		grant.Links[0].UploadBytes = 2
		state.UsageHistory["u/l"][0].UploadBytes = 2
		state.LinkHistory["l"][0].UploadBytes = 2
		task := state.UpgradeTasks["t"]
		task.BaselineLinks[0] = "changed"
		batch := state.UpgradeBatches["b"]
		batch.TaskIDs[0] = "changed"
	}
	snapshot := s.Snapshot()
	mutate(&snapshot)
	if err := s.Update(func(state *model.State) error {
		mutate(state)
		return errors.New("rollback")
	}); err == nil {
		t.Fatal("expected callback failure")
	}
	state := s.Snapshot()
	if state.Agents["a"].AllowedCIDRs[0] != "127.0.0.0/8" || state.Upstreams["u"].Candidates[0].Name != "original" || state.RetiredGrants["g"].LinkNames["l"] != "original" || state.NodeStatus["n"].FailureCounters["failure"] != 1 || state.GrantStatus["g"].Links[0].UploadBytes != 1 || state.UsageHistory["u/l"][0].UploadBytes != 1 || state.LinkHistory["l"][0].UploadBytes != 1 || state.UpgradeTasks["t"].BaselineLinks[0] != "l" || state.UpgradeBatches["b"].TaskIDs[0] != "t" {
		t.Fatal("nested state changed outside a successful update")
	}
}
