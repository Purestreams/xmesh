// Package egress defines the default restrictions on client proxy traffic.
package egress

import (
	"strconv"
	"strings"
)

var blockedTCPPorts = [...]int{25, 465, 587, 2525}

// BlockedTCPPort reports whether a destination TCP port is blocked by default.
// This policy does not apply to UDP or node management/tunnel connections.
func BlockedTCPPort(port int) bool {
	for _, blocked := range blockedTCPPorts {
		if port == blocked {
			return true
		}
	}
	return false
}

// BlockedTCPPorts returns the destination port list for Xray routing rules.
func BlockedTCPPorts() string {
	ports := make([]string, len(blockedTCPPorts))
	for i, port := range blockedTCPPorts {
		ports[i] = strconv.Itoa(port)
	}
	return strings.Join(ports, ",")
}
