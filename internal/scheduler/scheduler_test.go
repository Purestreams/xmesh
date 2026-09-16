package scheduler

import (
	"net"
	"testing"

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
