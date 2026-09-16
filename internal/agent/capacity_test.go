package agent

import (
	"log/slog"
	"testing"

	"xmesh/internal/runtimecfg"
)

func TestAgentStreamSlotsRejectWithoutBlocking(t *testing.T) {
	runtime := New(runtimecfg.Config{MaxActiveStreams: 1}, slog.Default())
	if !runtime.acquireStreamSlot() {
		t.Fatal("first stream rejected")
	}
	if runtime.acquireStreamSlot() {
		t.Fatal("stream accepted past global limit")
	}
	runtime.releaseStreamSlot()
	if !runtime.acquireStreamSlot() {
		t.Fatal("released capacity was not reusable")
	}
	runtime.releaseStreamSlot()
}
