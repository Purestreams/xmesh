package protocol

import "testing"

func TestNewSMuxConfigHighLatencyWindow(t *testing.T) {
	config, err := NewSMuxConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Version != 2 {
		t.Fatalf("version=%d", config.Version)
	}
	if config.MaxFrameSize != 32<<10 {
		t.Fatalf("frame=%d", config.MaxFrameSize)
	}
	if config.MaxStreamBuffer != 8<<20 {
		t.Fatalf("stream buffer=%d", config.MaxStreamBuffer)
	}
	if config.MaxReceiveBuffer != 32<<20 {
		t.Fatalf("receive buffer=%d", config.MaxReceiveBuffer)
	}
	if config.MaxStreamBuffer/2 < 2_500_000 {
		t.Fatalf("half stream window %d is below the 400 ms / 50 Mbps BDP", config.MaxStreamBuffer/2)
	}
}
