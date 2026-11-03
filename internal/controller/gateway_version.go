package controller

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/Purestreams/xmesh/internal/model"
)

const gatewayAutoIPMinimum = "v0.4.6"
const gatewayIPv6Minimum = "v0.4.7"

// Accept stable release versions only. Unknown, development and prerelease
// builds must not be assumed to implement a released feature.
var gatewayReleaseVersion = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func gatewayVersionAtLeast(version string, minimum [3]uint64) bool {
	parts := gatewayReleaseVersion.FindStringSubmatch(version)
	if parts == nil {
		return false
	}
	var actual [3]uint64
	for i := range actual {
		value, err := strconv.ParseUint(parts[i+1], 10, 64)
		if err != nil {
			return false
		}
		actual[i] = value
	}
	for i, value := range actual {
		if value != minimum[i] {
			return value > minimum[i]
		}
	}
	return true
}

func gatewayAutoIPSupported(version string) bool {
	return gatewayVersionAtLeast(version, [3]uint64{0, 4, 6})
}

func gatewayIPv6Supported(version string) bool {
	return gatewayVersionAtLeast(version, [3]uint64{0, 4, 7})
}

func validateGatewayAddressActivation(previous, next model.Gateway, status model.NodeStatus) error {
	// Uninstalled Gateways may be preconfigured; announcements and subscription
	// publication still require a supported version after enrollment.
	if previous.CredentialHash == "" {
		return nil
	}
	if next.PublicHostAuto && !previous.PublicHostAuto && !gatewayAutoIPSupported(status.BinaryVersion) {
		return fmt.Errorf("public IP discovery requires Gateway %s or newer; reported version: %q", gatewayAutoIPMinimum, status.BinaryVersion)
	}
	if next.IPv6IngressEnabled && !previous.IPv6IngressEnabled && !gatewayIPv6Supported(status.BinaryVersion) {
		return fmt.Errorf("IPv6 ingress requires Gateway %s or newer; reported version: %q", gatewayIPv6Minimum, status.BinaryVersion)
	}
	return nil
}
