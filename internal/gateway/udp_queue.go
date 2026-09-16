package gateway

import (
	"context"
	"sync/atomic"

	"xmesh/internal/protocol"
)

// udpQueue bounds both packet count and payload bytes; a large datagram cannot
// silently turn a small channel into an unbounded application-level backlog.
type udpQueue struct {
	items chan protocol.Datagram
	bytes atomic.Int64
	limit int64
}

func newUDPQueue(limit int) *udpQueue {
	if limit <= 0 {
		limit = 256 << 10
	}
	return &udpQueue{items: make(chan protocol.Datagram, 64), limit: int64(limit)}
}

func (q *udpQueue) offer(datagram protocol.Datagram) bool {
	size := int64(len(datagram.Payload))
	for {
		used := q.bytes.Load()
		if size > q.limit-used {
			return false
		}
		if q.bytes.CompareAndSwap(used, used+size) {
			break
		}
	}
	select {
	case q.items <- datagram:
		return true
	default:
		q.bytes.Add(-size)
		return false
	}
}

func (q *udpQueue) take(ctx context.Context) (protocol.Datagram, bool) {
	select {
	case <-ctx.Done():
		return protocol.Datagram{}, false
	case datagram := <-q.items:
		q.bytes.Add(-int64(len(datagram.Payload)))
		return datagram, true
	}
}
