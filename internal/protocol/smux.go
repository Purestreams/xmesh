package protocol

import (
	"time"

	"github.com/xtaci/smux"
)

const (
	SMuxMaxFrameSize      = 32 << 10
	SMuxMaxStreamBuffer   = 8 << 20
	SMuxMaxReceiveBuffer  = 32 << 20
	SMuxKeepAliveInterval = 10 * time.Second
	SMuxKeepAliveTimeout  = 60 * time.Second
)

// NewSMuxConfig returns the symmetric tunnel configuration used by both peers.
// The stream window is sized for a 50 Mbps flow at 400 ms RTT while leaving
// enough session-level receive budget for concurrent streams and control data.
func NewSMuxConfig() (*smux.Config, error) {
	config := smux.DefaultConfig()
	config.Version = SMuxVersion
	config.MaxFrameSize = SMuxMaxFrameSize
	config.MaxStreamBuffer = SMuxMaxStreamBuffer
	config.MaxReceiveBuffer = SMuxMaxReceiveBuffer
	config.KeepAliveInterval = SMuxKeepAliveInterval
	config.KeepAliveTimeout = SMuxKeepAliveTimeout
	if err := smux.VerifyConfig(config); err != nil {
		return nil, err
	}
	return config, nil
}
