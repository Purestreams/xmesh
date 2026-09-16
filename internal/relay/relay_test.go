package relay

import (
	"net"
	"testing"
	"time"
)

func TestStalledWriteEndsBothDirections(t *testing.T) {
	client, left := net.Pipe()
	right, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan struct{})
	go func() {
		Bidirectional(left, right, nil, nil, Options{WriteStallTimeout: 50 * time.Millisecond})
		close(done)
	}()
	go func() { _, _ = client.Write([]byte("blocked")) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stalled write did not release the relay")
	}
}

func TestIdleConnectionIsNotAWriteStall(t *testing.T) {
	client, left := net.Pipe()
	right, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan struct{})
	go func() {
		Bidirectional(left, right, nil, nil, Options{WriteStallTimeout: 30 * time.Millisecond})
		close(done)
	}()
	time.Sleep(60 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("idle relay stopped without a pending write")
	default:
	}
	go func() { _, _ = client.Write([]byte("x")) }()
	buffer := make([]byte, 1)
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	if n, err := server.Read(buffer); err != nil || n != 1 || buffer[0] != 'x' {
		t.Fatalf("idle relay did not resume: n=%d err=%v", n, err)
	}
	_ = client.Close()
	_ = server.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay did not close")
	}
}
