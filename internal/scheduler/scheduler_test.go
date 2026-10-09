package scheduler

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/xtaci/smux"
)

func smuxPair(t *testing.T) (*smux.Session, func()) {
	t.Helper()
	left, right := net.Pipe()
	cfg := smux.DefaultConfig()
	cfg.Version = 2
	serverCh := make(chan *smux.Session, 1)
	go func() { session, _ := smux.Server(left, cfg); serverCh <- session }()
	client, err := smux.Client(right, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := <-serverCh
	return client, func() { client.Close(); server.Close(); left.Close(); right.Close() }
}

func TestAcquireAvoidsRecentlyBlockedSession(t *testing.T) {
	a, closeA := smuxPair(t)
	defer closeA()
	b, closeB := smuxPair(t)
	defer closeB()
	pool := New()
	first := &Session{ID: "a", AgentID: "agent", Weight: 1, MaxStreams: 4, SMux: a, Ready: true}
	second := &Session{ID: "b", AgentID: "agent", Weight: 1, MaxStreams: 4, SMux: b, Ready: true}
	pool.Add(first)
	pool.Add(second)
	first.ObserveWrite(25 * time.Millisecond)
	lease, err := pool.Acquire("agent")
	if err != nil {
		t.Fatal(err)
	}
	if lease.Session != second {
		t.Fatal("new stream was assigned to recently blocked session")
	}
	lease.Release()
	first.load.lastStall.Store(time.Now().Add(-6 * time.Second).UnixNano())
	lease, err = pool.Acquire("agent")
	if err != nil || lease.Session != first {
		t.Fatalf("expired write stall still affected routing: lease=%v err=%v", lease, err)
	}
	lease.Release()
	result := pool.Snapshot()
	for _, session := range result {
		if session.ID == "a" && (session.WriteStalls != 1 || session.WriteBlockedMillis != 25) {
			t.Fatalf("write metrics missing: %+v", session)
		}
	}
}

func TestAcquireUsesHighestPriorityHealthyGroup(t *testing.T) {
	primary, closePrimary := smuxPair(t)
	defer closePrimary()
	backup, closeBackup := smuxPair(t)
	defer closeBackup()
	pool := New()
	pool.Add(&Session{ID: "primary", AgentID: "a", Priority: 10, Weight: 1, MaxStreams: 1, SMux: primary, Ready: true})
	pool.Add(&Session{ID: "backup", AgentID: "a", Priority: 20, Weight: 1, MaxStreams: 2, SMux: backup, Ready: true})
	first, err := pool.Acquire("a")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if first.Session.ID != "primary" {
		t.Fatalf("selected %s", first.Session.ID)
	}
	second, err := pool.Acquire("a")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if second.Session.ID != "backup" {
		t.Fatalf("expected backup at primary capacity, got %s", second.Session.ID)
	}
}

func TestAcquireLinksKeepsGrantOnItsRoute(t *testing.T) {
	foreign, closeForeign := smuxPair(t)
	defer closeForeign()
	allowed, closeAllowed := smuxPair(t)
	defer closeAllowed()
	pool := New()
	pool.Add(&Session{ID: "foreign", AgentID: "agent", LinkID: "other-route", Priority: 1, Weight: 1, MaxStreams: 1, SMux: foreign, Ready: true})
	pool.Add(&Session{ID: "allowed", AgentID: "agent", LinkID: "grant-route", Priority: 20, Weight: 1, MaxStreams: 1, SMux: allowed, Ready: true})
	lease, err := pool.AcquireLinks("agent", map[string]bool{"grant-route": true})
	if err != nil || lease.Session.LinkID != "grant-route" {
		t.Fatalf("selected foreign route: lease=%v err=%v", lease, err)
	}
	lease.Release()
	if _, err := pool.AcquireLinks("agent", map[string]bool{}); !errors.Is(err, ErrNoPath) {
		t.Fatalf("empty grant route: %v", err)
	}
}

func TestAcquireUsesWeightsWithinPriorityGroup(t *testing.T) {
	a, closeA := smuxPair(t)
	defer closeA()
	b, closeB := smuxPair(t)
	defer closeB()
	pool := New()
	pool.Add(&Session{ID: "a", AgentID: "agent", Priority: 10, Weight: 3, MaxStreams: 10, SMux: a, Ready: true})
	pool.Add(&Session{ID: "b", AgentID: "agent", Priority: 10, Weight: 1, MaxStreams: 10, SMux: b, Ready: true})
	counts := map[string]int{}
	var leases []*Lease
	for range 4 {
		lease, err := pool.Acquire("agent")
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, lease)
		counts[lease.Session.ID]++
	}
	defer func() {
		for _, lease := range leases {
			lease.Release()
		}
	}()
	if counts["a"] != 3 || counts["b"] != 1 {
		t.Fatalf("unexpected weighted allocation %#v", counts)
	}
}

func TestReplacingSessionIgnoresStaleRemoval(t *testing.T) {
	oldSMux, closeOld := smuxPair(t)
	defer closeOld()
	newSMux, closeNew := smuxPair(t)
	defer closeNew()

	pool := New()
	oldSession := &Session{ID: "agent/link/slot-0", AgentID: "agent", MaxStreams: 1, SMux: oldSMux, Ready: true}
	newSession := &Session{ID: oldSession.ID, AgentID: "agent", MaxStreams: 1, SMux: newSMux, Ready: true}
	if replaced := pool.Add(oldSession); replaced != nil {
		t.Fatal("first session unexpectedly replaced another session")
	}
	if replaced := pool.Add(newSession); replaced != oldSession {
		t.Fatal("replacement did not return the stale session")
	}

	pool.Remove(oldSession.ID, oldSession)
	lease, err := pool.Acquire("agent")
	if err != nil {
		t.Fatalf("stale removal removed replacement: %v", err)
	}
	if lease.Session != newSession {
		t.Fatal("acquired stale session after replacement")
	}
	lease.Release()

	pool.Remove(newSession.ID, newSession)
	if _, err := pool.Acquire("agent"); !errors.Is(err, ErrNoPath) {
		t.Fatalf("current session removal returned %v", err)
	}
}

func TestUpdatePolicyKeepsLeasesAndChangesNewSelection(t *testing.T) {
	a, closeA := smuxPair(t)
	defer closeA()
	b, closeB := smuxPair(t)
	defer closeB()
	pool := New()
	first := &Session{ID: "a", LinkID: "link-a", AgentID: "agent", Priority: 10, Weight: 1, MaxStreams: 4, SMux: a, Ready: true}
	second := &Session{ID: "b", LinkID: "link-b", AgentID: "agent", Priority: 20, Weight: 1, MaxStreams: 4, SMux: b, Ready: true}
	pool.Add(first)
	pool.Add(second)
	existing, err := pool.Acquire("agent")
	if err != nil || existing.Session != first {
		t.Fatalf("initial selection: %v %v", existing, err)
	}
	defer existing.Release()
	first.ObserveWrite(25 * time.Millisecond)
	pool.UpdatePolicy("link-a", "agent", 30, 3, 1)
	if a.IsClosed() || existing.Session != first || first.active != 1 || first.load.stalls.Load() != 1 {
		t.Fatal("policy update replaced a session or reset live lease/load accounting")
	}
	if _, err := pool.AcquireLinks("agent", map[string]bool{"link-a": true}); !errors.Is(err, ErrNoPath) {
		t.Fatalf("lower stream limit was ignored: %v", err)
	}
	next, err := pool.Acquire("agent")
	if err != nil || next.Session != second {
		t.Fatalf("updated priority was ignored: %v %v", next, err)
	}
	next.Release()
	existing.Release()
	pool.UpdatePolicy("link-a", "agent", 5, 7, 2)
	next, err = pool.Acquire("agent")
	if err != nil || next.Session != first || first.Weight != 7 {
		t.Fatalf("updated policy was ignored: %v %v", next, err)
	}
	next.Release()
	if first.active != 0 || a.IsClosed() {
		t.Fatal("live lease release corrupted session state")
	}
}

func TestReplacingSessionIgnoresStaleProbe(t *testing.T) {
	oldSMux, closeOld := smuxPair(t)
	defer closeOld()
	newSMux, closeNew := smuxPair(t)
	defer closeNew()
	pool := New()
	old := &Session{ID: "agent/link/slot-0", AgentID: "agent", SMux: oldSMux, Ready: true}
	current := &Session{ID: old.ID, AgentID: "agent", SMux: newSMux, Ready: true}
	pool.Add(old)
	pool.Add(current)
	pool.UpdateProbe(old, time.Second, errors.New("old tunnel closed"))
	pool.UpdateProbe(old, time.Second, nil)
	state := pool.Snapshot()[0]
	if state.ProbeTimeouts != 0 || state.LastError != "" || state.RTTMillis != 0 || !state.Ready {
		t.Fatalf("stale probe changed the replacement: %+v", state)
	}
	pool.UpdateProbe(current, 25*time.Millisecond, nil)
	if state := pool.Snapshot()[0]; state.RTTMillis != 25 || !state.Ready {
		t.Fatalf("current session probe was not applied: %+v", state)
	}
}
