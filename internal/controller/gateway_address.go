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
	if gateway.PublicHost == host {
		return
	}
	oldURL := "reality://" + net.JoinHostPort(gateway.PublicHost, "8443") + "/tunnel"
	newURL := ""
	if host != "" {
		newURL = "reality://" + net.JoinHostPort(host, "8443") + "/tunnel"
	}
	for id, link := range state.Links {
		attachment := state.Attachments[link.AttachmentID]
		if attachment.GatewayID == gateway.ID && (link.AutoURL || (!link.URLOverride && link.URL == oldURL)) {
			link.URL, link.AutoURL = newURL, true
			state.Links[id] = link
			bumpAgent(state, attachment.AgentID)
		}
	}
	gateway.PublicHost = host
	gateway.HY2Certificate, gateway.HY2PrivateKey, gateway.HY2PinSHA256 = "", "", ""
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
	ip, valid := model.ParsePublicIP(report.IP)
	sources := map[string]bool{}
	for _, source := range report.Sources {
		if !slices.ContainsFunc(model.PublicIPSources(), func(allowed model.PublicIPSource) bool { return source == allowed.Name }) {
			http.Error(w, "unknown public IP source", http.StatusBadRequest)
			return
		}
		sources[source] = true
	}
	if !valid || len(sources) < 2 {
		http.Error(w, "a public IP confirmed by at least two independent sources is required", http.StatusBadRequest)
		return
	}
	names := make([]string, 0, len(sources))
	for source := range sources {
		names = append(names, source)
	}
	sort.Strings(names)
	err := s.store.Update(func(state *model.State) error {
		gateway, exists := state.Gateways[nodeID]
		if !exists {
			return fmt.Errorf("Gateway no longer exists")
		}
		gateway.DetectedPublicIP, gateway.PublicIPSources = ip.String(), strings.Join(names, ", ")
		gateway.PublicIPCheckedAt = s.now().UTC()
		gateway.ObservedPublicIP = s.observedPublicIP(r)
		if gateway.PublicHostAuto && gateway.PublicHost != ip.String() {
			changeGatewayHost(state, &gateway, ip.String())
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
