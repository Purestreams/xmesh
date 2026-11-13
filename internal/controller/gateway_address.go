package controller

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/Purestreams/xmesh/internal/model"
)

func normalizeGatewayPublicHost(value string) (string, error) {
	host := strings.TrimSpace(value)
	if host == "" {
		return "", nil
	}
	// Accept bracketed IPv6 input, but persist a bare address so JoinHostPort
	// cannot produce a malformed URL with doubled brackets.
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
		if ip, err := netip.ParseAddr(host); err == nil && ip.Is6() && ip.Zone() == "" {
			return ip.Unmap().String(), nil
		}
		return "", fmt.Errorf("invalid bracketed IPv6 address")
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Zone() == "" {
		return ip.Unmap().String(), nil
	}
	if strings.ContainsAny(host, "[]:/\\?#@% \t\r\n") {
		return "", fmt.Errorf("public host must be a hostname or IP address without a scheme or port")
	}
	return host, nil
}

// Older clients infer automatic mode from an empty host. Explicit controls
// let an administrator retain a manual value while ignoring it in auto mode.
func gatewayAddressForm(r *http.Request, gateway *model.Gateway) (string, error) {
	manual := strings.TrimSpace(r.FormValue("public_host"))
	auto := manual == ""
	if r.Form.Has("address_controls") {
		auto = r.FormValue("public_host_auto") == "1"
		gateway.IPv6IngressEnabled = r.FormValue("ipv6_ingress_enabled") == "1"
	}
	if !auto {
		var err error
		manual, err = normalizeGatewayPublicHost(manual)
		if err != nil {
			return "", err
		}
		if manual == "" {
			return "", fmt.Errorf("manual public host is required when automatic discovery is disabled")
		}
	}
	gateway.ManualPublicHost, gateway.PublicHostAuto = manual, auto
	if auto {
		if gateway.DetectedPublicIP != "" {
			return gateway.DetectedPublicIP, nil
		}
		return gateway.PublicHost, nil
	}
	return manual, nil
}

func confirmedPublicIP(value string, sources []string) (string, string, error) {
	ip, valid := model.ParsePublicIP(value)
	names := map[string]bool{}
	for _, source := range sources {
		if !slices.ContainsFunc(model.PublicIPSources(), func(allowed model.PublicIPSource) bool { return source == allowed.Name }) {
			return "", "", fmt.Errorf("unknown public IP source")
		}
		names[source] = true
	}
	if !valid || len(names) < 2 {
		return "", "", fmt.Errorf("a public IP confirmed by at least two independent sources is required")
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	return ip.String(), strings.Join(ordered, ", "), nil
}

func (s *Server) observedPublicIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	trusted := s.cfg.TrustedProxyCIDRs
	if trusted == nil {
		trusted = []string{"127.0.0.0/8", "::1/128"}
	}
	isTrusted := func(ip netip.Addr) bool {
		for _, cidr := range trusted {
			if prefix, err := netip.ParsePrefix(cidr); err == nil && prefix.Contains(ip.Unmap()) {
				return true
			}
		}
		return false
	}
	if isTrusted(peer) {
		// Walk from the nearest hop. Never accept an untrusted client's own
		// forwarding headers, or skip a malformed hop to reach a forged value.
		chain := r.Header.Get("X-Forwarded-For")
		if chain == "" {
			chain = r.Header.Get("X-Real-IP")
		}
		if len(chain) > 4096 {
			return ""
		}
		hops := strings.Split(chain, ",")
		for i := len(hops) - 1; i >= 0 && isTrusted(peer); i-- {
			peer, err = netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				return ""
			}
		}
	}
	if ip, ok := model.ParsePublicIP(peer.String()); ok {
		return ip.String()
	}
	return ""
}

// PublicHost remains the effective address for older nodes and subscriptions.
// The new flag preserves all existing addresses as explicit manual overrides.
func changeGatewayHost(state *model.State, gateway *model.Gateway, host string) {
	changeGatewayEndpoint(state, gateway, host, gateway.RealityPort)
}

func changeGatewayEndpoint(state *model.State, gateway *model.Gateway, host string, realityPort int) {
	if gateway.PublicHost == host && gateway.RealityPort == realityPort {
		return
	}
	oldURL := gatewayRealityURL(gateway.PublicHost, gateway.RealityLinkPort())
	hostChanged := gateway.PublicHost != host
	if hostChanged {
		gateway.PublicHost = host
		gateway.HY2Certificate, gateway.HY2PrivateKey, gateway.HY2PinSHA256 = "", "", ""
	}
	gateway.RealityPort = realityPort
	newURL := gatewayRealityURL(host, gateway.RealityLinkPort())
	if oldURL == newURL {
		return
	}
	agents := map[string]bool{}
	for id, link := range state.Links {
		attachment := state.Attachments[link.AttachmentID]
		if attachment.GatewayID == gateway.ID && (link.AutoURL || (!link.URLOverride && oldURL != "" && link.URL == oldURL)) {
			link.URL, link.AutoURL = newURL, true
			state.Links[id] = link
			agents[attachment.AgentID] = true
		}
	}
	if hostChanged || gateway.PreviousRealityLinkPort == 0 {
		for id := range agents {
			bumpAgent(state, id)
		}
	}
}

func unpublishGatewayGrants(state *model.State, gatewayID string) {
	for id, grant := range state.Grants {
		if state.Attachments[grant.AttachmentID].GatewayID == gatewayID {
			grant.Published = false
			state.Grants[id] = grant
		}
	}
}

func (s *Server) gatewayAddress(w http.ResponseWriter, r *http.Request) {
	role, nodeID, ok := s.authenticateNodeIdentity(r, true)
	if !ok || role != model.RoleGateway {
		http.Error(w, "Gateway authentication required", http.StatusUnauthorized)
		return
	}
	var report model.PublicIPReport
	if !decodeJSON(w, r, &report) {
		return
	}
	ip, sources, err := confirmedPublicIP(report.IP, report.Sources)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ipv6, ipv6Sources := "", ""
	if parsed, _ := model.ParsePublicIP(ip); parsed.Is6() {
		ipv6, ipv6Sources = ip, sources
	}
	if report.IPv6 != "" || len(report.IPv6Sources) != 0 {
		ipv6, ipv6Sources, err = confirmedPublicIP(report.IPv6, report.IPv6Sources)
		parsed, _ := model.ParsePublicIP(ipv6)
		if err != nil || !parsed.Is6() || (strings.Contains(ip, ":") && ip != ipv6) {
			http.Error(w, "invalid IPv6 consensus", http.StatusBadRequest)
			return
		}
	}
	err = s.store.Update(func(state *model.State) error {
		gateway, exists := state.Gateways[nodeID]
		if !exists {
			return fmt.Errorf("Gateway no longer exists")
		}
		version := state.NodeStatus[nodeID].BinaryVersion
		if !gatewayAutoIPSupported(version) {
			return fmt.Errorf("public IP discovery requires Gateway %s or newer; reported version: %q", gatewayAutoIPMinimum, version)
		}
		if (report.IPv6 != "" || len(report.IPv6Sources) != 0) && !gatewayIPv6Supported(version) {
			return fmt.Errorf("dual-stack discovery requires Gateway %s or newer; reported version: %q", gatewayIPv6Minimum, version)
		}
		previousIPv6 := gateway.IPv6IngressHost()
		primary := ip
		oldIP, oldValid := model.ParsePublicIP(gateway.DetectedPublicIP)
		// A transient IPv4 probe failure must not replace existing IPv4 nodes
		// with IPv6 nodes when the administrator requested both families.
		if gateway.IPv6IngressEnabled && strings.Contains(ip, ":") && oldValid && oldIP.Is4() {
			primary = gateway.DetectedPublicIP
		} else {
			gateway.DetectedPublicIP, gateway.PublicIPSources = ip, sources
			gateway.PublicIPCheckedAt = s.now().UTC()
		}
		if ipv6 != "" {
			gateway.DetectedPublicIPv6, gateway.PublicIPv6Sources = ipv6, ipv6Sources
			gateway.PublicIPv6CheckedAt = s.now().UTC()
		}
		gateway.ObservedPublicIP = s.observedPublicIP(r)
		changed := previousIPv6 != gateway.IPv6IngressHost()
		if gateway.PublicHostAuto && gateway.PublicHost != primary {
			changeGatewayHost(state, &gateway, primary)
			changed = true
		}
		if changed {
			if err := configureHY2(state, &gateway, gateway.HY2Port, s.now()); err != nil {
				return err
			}
			gateway.DesiredVersion++
			unpublishGatewayGrants(state, nodeID)
		}
		state.Gateways[nodeID] = gateway
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
