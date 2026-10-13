package audit

import (
	"strings"
	"time"
)

const (
	DefaultLimitMB = 20
	MaxBatch       = 256
	QueueSize      = 2048
)

type Policy struct {
	Generation uint64 `json:"generation"`
}

// Event is connection metadata, never traffic payload or credentials.
// User and exit identities are filled by the Controller from trusted grants.
type Event struct {
	At            time.Time `json:"at"`
	GrantID       string    `json:"grant_id"`
	Generation    uint64    `json:"generation"`
	ConnectionID  string    `json:"connection_id"`
	Kind          string    `json:"kind"`
	Result        string    `json:"result"`
	Host          string    `json:"host"`
	IP            string    `json:"ip,omitempty"`
	IPs           []string  `json:"ips,omitempty"`
	Port          int       `json:"port"`
	Reason        string    `json:"reason,omitempty"`
	DurationMS    int64     `json:"duration_ms,omitempty"`
	ConnectMS     int64     `json:"connect_ms,omitempty"`
	UploadBytes   uint64    `json:"upload_bytes,omitempty"`
	DownloadBytes uint64    `json:"download_bytes,omitempty"`
	LinkID        string    `json:"link_id"`
	UserID        string    `json:"user_id,omitempty"`
	UserName      string    `json:"user_name,omitempty"`
	AgentID       string    `json:"agent_id,omitempty"`
	GatewayID     string    `json:"gateway_id,omitempty"`
	ExitName      string    `json:"exit_name,omitempty"`
}

type Drop struct {
	GrantID    string `json:"grant_id"`
	Generation uint64 `json:"generation"`
	Count      uint64 `json:"count"`
}

type Batch struct {
	Events  []Event `json:"events"`
	Dropped []Drop  `json:"dropped,omitempty"`
}

type Summary struct {
	WindowSeconds int       `json:"window_seconds"`
	Attempts      uint64    `json:"attempts"`
	Failures      uint64    `json:"failures"`
	Targets       int       `json:"targets"`
	Ports         int       `json:"ports"`
	Dropped       uint64    `json:"dropped"`
	LastReceived  time.Time `json:"last_received,omitempty"`
	Alerts        []string  `json:"alerts"`
}

type bucket struct {
	second             int64
	attempts, failures uint64
	targets            map[string]bool
	ports              map[int]bool
}

type window struct {
	buckets      [60]bucket
	dropped      uint64
	lastReceived time.Time
}

func (w *window) add(e Event, received time.Time) {
	w.lastReceived = received
	if e.Kind != "tcp" {
		return
	}
	second := received.Unix()
	b := &w.buckets[second%60]
	if b.second != second {
		*b = bucket{second: second, targets: map[string]bool{}, ports: map[int]bool{}}
	}
	if e.Result == "attempt" {
		b.attempts++
		if len(b.targets) < 128 {
			b.targets[strings.TrimSuffix(strings.ToLower(e.Host), ".")] = true
		}
		if len(b.ports) < 128 {
			b.ports[e.Port] = true
		}
	}
	if e.Result == "failed" || e.Result == "denied" {
		b.failures++
	}
}

func (w *window) summary(now time.Time) Summary {
	s := Summary{WindowSeconds: 60, Dropped: w.dropped, LastReceived: w.lastReceived, Alerts: []string{}}
	targets, ports := map[string]bool{}, map[int]bool{}
	for _, b := range w.buckets {
		if b.second <= now.Unix()-60 || b.second > now.Unix() {
			continue
		}
		s.Attempts += b.attempts
		s.Failures += b.failures
		for host := range b.targets {
			if len(targets) < 128 {
				targets[host] = true
			}
		}
		for port := range b.ports {
			if len(ports) < 128 {
				ports[port] = true
			}
		}
	}
	s.Targets, s.Ports = len(targets), len(ports)
	if s.Attempts >= 120 {
		s.Alerts = append(s.Alerts, "connections")
	}
	if s.Failures >= 30 {
		s.Alerts = append(s.Alerts, "failures")
	}
	if s.Targets >= 30 {
		s.Alerts = append(s.Alerts, "targets")
	}
	if s.Ports >= 20 {
		s.Alerts = append(s.Alerts, "ports")
	}
	return s
}
