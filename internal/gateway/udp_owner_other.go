//go:build !linux && !windows

package gateway

import "net"

// Platforms without socket ownership support require an explicit UDP endpoint.
func udpControlOwner(net.Conn) (uint32, bool)  { return 0, false }
func udpSourceOwned(*net.UDPAddr, uint32) bool { return false }
