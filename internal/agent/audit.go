package agent

import (
	"context"
	"net/netip"
	"strings"
	"time"

	"xmesh/internal/audit"
	"xmesh/internal/identity"
	"xmesh/internal/protocol"
)

type auditDropKey struct {
	grant      string
	generation uint64
}
type auditTrace struct {
	runtime *Runtime
	base    audit.Event
}

func (r *Runtime) newAuditTrace(linkID string, request protocol.Message) *auditTrace {
	if len(request.Host) == 0 || len(request.Host) > 255 || request.Port < 1 || request.Port > 65535 {
		return nil
	}
	r.mu.RLock()
	policy, enabled := r.config.AuditGrants[request.GrantID]
	allowed := enabled && r.grantAllowedLocked(linkID, request.GrantID)
	r.mu.RUnlock()
	if !allowed {
		return nil
	}
	id := request.RequestID
	if id == "" || len(id) > 128 {
		id, _ = identity.Token(12)
	}
	return &auditTrace{runtime: r, base: audit.Event{GrantID: request.GrantID, Generation: policy.Generation, ConnectionID: id, LinkID: linkID, Host: request.Host, Port: request.Port}}
}

func (t *auditTrace) emit(e audit.Event) {
	if t == nil {
		return
	}
	r := t.runtime
	r.mu.RLock()
	policy, enabled := r.config.AuditGrants[t.base.GrantID]
	allowed := enabled && policy.Generation == t.base.Generation && r.grantAllowedLocked(t.base.LinkID, t.base.GrantID)
	r.mu.RUnlock()
	if !allowed {
		return
	}
	e.At = time.Now().UTC()
	e.GrantID = t.base.GrantID
	e.Generation = t.base.Generation
	e.ConnectionID = t.base.ConnectionID
	e.LinkID = t.base.LinkID
	e.Host = t.base.Host
	e.Port = t.base.Port
	if len(e.Reason) > 512 {
		e.Reason = e.Reason[:512]
	}
	select {
	case r.auditQueue <- e:
	default:
		r.recordAuditDrop(e)
	}
}

func (t *auditTrace) observeDNS(ips []netip.Addr, err error, duration time.Duration) {
	if t == nil {
		return
	}
	e := audit.Event{Kind: "dns", Result: "resolved", DurationMS: duration.Milliseconds()}
	if err != nil {
		e.Result = "failed"
		e.Reason = err.Error()
	}
	for _, ip := range ips {
		if len(e.IPs) >= 32 {
			break
		}
		e.IPs = append(e.IPs, ip.Unmap().String())
	}
	t.emit(e)
}

func (r *Runtime) recordAuditDrop(e audit.Event) {
	r.auditMu.Lock()
	defer r.auditMu.Unlock()
	key := auditDropKey{e.GrantID, e.Generation}
	if len(r.auditDrops) < audit.QueueSize || r.auditDrops[key] != 0 {
		r.auditDrops[key]++
	}
}

func (r *Runtime) auditLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.flushAudit(ctx)
		}
	}
}

func (r *Runtime) flushAudit(ctx context.Context) {
	batch := audit.Batch{}
	for len(batch.Events) < audit.MaxBatch {
		select {
		case e := <-r.auditQueue:
			batch.Events = append(batch.Events, e)
		default:
			goto drained
		}
	}
drained:
	r.mu.RLock()
	events := batch.Events[:0]
	for _, e := range batch.Events {
		p, enabled := r.config.AuditGrants[e.GrantID]
		if enabled && p.Generation == e.Generation && r.grantAllowedLocked(e.LinkID, e.GrantID) {
			events = append(events, e)
		}
	}
	batch.Events = events
	r.auditMu.Lock()
	for key, count := range r.auditDrops {
		p, enabled := r.config.AuditGrants[key.grant]
		if !enabled || p.Generation != key.generation {
			delete(r.auditDrops, key)
			continue
		}
		if len(batch.Dropped) >= audit.MaxBatch {
			break
		}
		batch.Dropped = append(batch.Dropped, audit.Drop{GrantID: key.grant, Generation: key.generation, Count: count})
		delete(r.auditDrops, key)
	}
	r.auditMu.Unlock()
	r.mu.RUnlock()
	if len(batch.Events) == 0 && len(batch.Dropped) == 0 {
		return
	}
	if err := r.client.ReportAudit(ctx, batch); err != nil {
		for _, e := range batch.Events {
			r.recordAuditDrop(e)
		}
		// Keep loss counts for a later successful report, not event payloads.
		r.auditMu.Lock()
		for _, d := range batch.Dropped {
			key := auditDropKey{d.GrantID, d.Generation}
			if len(r.auditDrops) < audit.QueueSize || r.auditDrops[key] != 0 {
				r.auditDrops[key] += d.Count
			}
		}
		r.auditMu.Unlock()
		r.logger.Warn("audit report failed", "error", strings.TrimSpace(err.Error()))
	}
}
