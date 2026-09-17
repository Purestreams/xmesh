package scheduler

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/smux"
)

var ErrNoPath = errors.New("no healthy path available")

type Session struct {
	ID                 string
	AgentID            string
	LinkID             string
	Priority           int
	Weight             int
	MaxStreams         int
	SMux               *smux.Session
	Generation         uint64
	Ready              bool
	LastOK             time.Time
	LastError          string
	RTTMillis          float64
	ProbeTimeouts      uint64
	ActiveStreams      int
	WriteBlockedMillis uint64
	WriteStalls        uint64
	active             int
	load               *sessionLoad
}

type sessionLoad struct {
	writeNanos atomic.Uint64
	stalls     atomic.Uint64
	lastStall  atomic.Int64
}

// ObserveWrite records actual WSS stream write time. A recent write longer
// than 20 ms biases only new streams away from this session for five seconds.
func (s *Session) ObserveWrite(duration time.Duration) {
	if s.load == nil {
		return
	}
	if duration >= 20*time.Millisecond {
		s.load.writeNanos.Add(uint64(duration.Nanoseconds()))
		s.load.stalls.Add(1)
		s.load.lastStall.Store(time.Now().UnixNano())
	}
}

type Pool struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func New() *Pool { return &Pool{sessions: map[string]*Session{}} }

func (p *Pool) Add(session *Session) *Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	if session.Weight <= 0 {
		session.Weight = 1
	}
	if session.MaxStreams <= 0 {
		session.MaxStreams = 1
	}
	if session.load == nil {
		session.load = &sessionLoad{}
	}
	previous := p.sessions[session.ID]
	p.sessions[session.ID] = session
	return previous
}

func (p *Pool) Remove(id string, expected *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessions[id] == expected {
		delete(p.sessions, id)
	}
}

func (p *Pool) SetReady(id string, ready bool, err string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if session := p.sessions[id]; session != nil {
		session.Ready, session.LastError = ready, err
		if ready {
			session.LastOK = time.Now()
		}
	}
}

func (p *Pool) UpdateProbe(id string, rtt time.Duration, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	session := p.sessions[id]
	if session == nil {
		return
	}
	if err != nil {
		session.ProbeTimeouts++
		session.LastError = err.Error()
		if session.ProbeTimeouts >= 3 {
			session.Ready = false
		}
		return
	}
	measurement := float64(rtt.Microseconds()) / 1000
	if session.RTTMillis == 0 {
		session.RTTMillis = measurement
	} else {
		session.RTTMillis = session.RTTMillis*0.8 + measurement*0.2
	}
	session.ProbeTimeouts = 0
	session.Ready = true
	session.LastError = ""
	session.LastOK = time.Now()
}

type Lease struct {
	Session *Session
	pool    *Pool
	once    sync.Once
}

func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() { l.pool.mu.Lock(); l.Session.active--; l.pool.mu.Unlock() })
}

func (p *Pool) Acquire(agentID string) (*Lease, error) {
	return p.AcquireLinks(agentID, nil)
}

// AcquireLinks restricts selection to the Links authorized for a grant route.
// A nil set preserves the unconstrained behavior used by health checks.
func (p *Pool) AcquireLinks(agentID string, allowed map[string]bool) (*Lease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	candidates := make([]*Session, 0)
	priority := int(^uint(0) >> 1)
	for _, session := range p.sessions {
		if session.AgentID != agentID || (allowed != nil && !allowed[session.LinkID]) || !session.Ready || session.SMux == nil || session.SMux.IsClosed() || session.active >= session.MaxStreams {
			continue
		}
		if session.Priority < priority {
			priority = session.Priority
			candidates = candidates[:0]
		}
		if session.Priority == priority {
			candidates = append(candidates, session)
		}
	}
	if len(candidates) == 0 {
		return nil, ErrNoPath
	}
	now := time.Now()
	sort.Slice(candidates, func(i, j int) bool {
		left := loadScore(candidates[i], now)
		right := loadScore(candidates[j], now)
		if left == right {
			return candidates[i].ID < candidates[j].ID
		}
		return left < right
	})
	chosen := candidates[0]
	chosen.active++
	return &Lease{Session: chosen, pool: p}, nil
}

func loadScore(session *Session, now time.Time) float64 {
	penalty := 0
	if session.load != nil {
		last := session.load.lastStall.Load()
		if last > 0 && now.Sub(time.Unix(0, last)) < 5*time.Second {
			penalty = 1
		}
	}
	return float64(session.active+1+penalty) / float64(session.Weight)
}

func (p *Pool) Snapshot() []Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]Session, 0, len(p.sessions))
	for _, session := range p.sessions {
		copy := *session
		copy.active = session.active
		copy.ActiveStreams = session.active
		if session.load != nil {
			copy.WriteBlockedMillis = session.load.writeNanos.Load() / uint64(time.Millisecond)
			copy.WriteStalls = session.load.stalls.Load()
		}
		result = append(result, copy)
	}
	return result
}
