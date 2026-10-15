package relay

import (
	"io"
	"net"
	"testing"
	"time"
)

type partialErrorConn struct{ net.Conn }

func (c partialErrorConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b[:min(2, len(b))])
	if err != nil {
		return n, err
	}
	return n, io.ErrUnexpectedEOF
}

func TestPartialWriteIsCountedBeforeError(t *testing.T) {
	client, left := net.Pipe()
	right, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan struct{})
	count := 0
	go func() {
		Bidirectional(left, partialErrorConn{right}, func(n int) { count += n }, nil, Options{})
		close(done)
	}()
	go func() { _, _ = client.Write([]byte("hello")) }()
	if _, err := io.ReadFull(server, make([]byte, 2)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if count != 2 {
			t.Fatalf("partial write counted %d bytes, want 2", count)
		}
	case <-time.After(time.Second):
		t.Fatal("write error did not close relay")
	}
}

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
