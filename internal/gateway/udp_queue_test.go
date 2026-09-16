package gateway

import (
	"context"
	"testing"

	"xmesh/internal/protocol"
)

func TestUDPQueueBoundsBytesAndReturnsCapacity(t *testing.T) {
	queue := newUDPQueue(100)
	first := protocol.Datagram{Payload: make([]byte, 80)}
	second := protocol.Datagram{Payload: make([]byte, 30)}
	if !queue.offer(first) || queue.offer(second) {
		t.Fatal("queue did not enforce byte limit")
	}
	if got := queue.bytes.Load(); got != 80 {
		t.Fatalf("queued bytes=%d", got)
	}
	if _, ok := queue.take(context.Background()); !ok || !queue.offer(second) {
		t.Fatal("dequeue did not return byte capacity")
	}
	if got := queue.bytes.Load(); got != 30 {
		t.Fatalf("queued bytes=%d", got)
	}
}

func TestUDPQueueBoundsPacketCount(t *testing.T) {
	queue := newUDPQueue(100)
	for range 64 {
		if !queue.offer(protocol.Datagram{}) {
			t.Fatal("queue rejected a packet before reaching capacity")
		}
	}
	if queue.offer(protocol.Datagram{}) {
		t.Fatal("queue accepted packet beyond channel capacity")
	}
}
