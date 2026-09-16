package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"

	"xmesh/internal/model"
)

type accessPolicy struct {
	allow []netip.Prefix
	deny  []netip.Prefix
	ports map[int]bool
}

func compilePolicy(agent model.Agent) (accessPolicy, error) {
	var policy accessPolicy
	policy.ports = map[int]bool{}
	for _, value := range agent.AllowedCIDRs {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return policy, fmt.Errorf("invalid allowed CIDR %q: %w", value, err)
		}
		policy.allow = append(policy.allow, prefix)
	}
	for _, value := range agent.DeniedCIDRs {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return policy, fmt.Errorf("invalid denied CIDR %q: %w", value, err)
		}
		policy.deny = append(policy.deny, prefix)
	}
	for _, port := range agent.AllowedPorts {
		if port < 1 || port > 65535 {
			return policy, fmt.Errorf("invalid allowed port %d", port)
		}
		policy.ports[port] = true
	}
	if len(policy.allow) == 0 {
		return policy, errors.New("access policy has no allowed CIDRs")
	}
	return policy, nil
}

func (p accessPolicy) resolve(ctx context.Context, host string, port int) ([]netip.Addr, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid target port")
	}
	if len(p.ports) > 0 && !p.ports[port] {
		return nil, errors.New("target port is not allowed")
	}
	var candidates []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		candidates = []netip.Addr{ip.Unmap()}
	} else {
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve target: %w", err)
		}
		for _, ip := range ips {
			candidates = append(candidates, ip.Unmap())
		}
	}
	var allowed []netip.Addr
	for _, ip := range candidates {
		if !contains(p.allow, ip) || contains(p.deny, ip) {
			continue
		}
		allowed = append(allowed, ip)
	}
	if len(allowed) == 0 {
		return nil, errors.New("target denied by access policy")
	}
	return allowed, nil
}

func contains(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func (p accessPolicy) dialTCP(ctx context.Context, host string, port int) (net.Conn, error) {
	addresses, err := p.resolve(ctx, host, port)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{}
	var last error
	for _, address := range addresses {
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.String(), strconv.Itoa(port)))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

func (p accessPolicy) udpAddress(ctx context.Context, host string, port int) (*net.UDPAddr, error) {
	addresses, err := p.resolve(ctx, host, port)
	if err != nil {
		return nil, err
	}
	return net.UDPAddrFromAddrPort(netip.AddrPortFrom(addresses[0], uint16(port))), nil
}
