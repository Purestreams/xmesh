package model

import "net/netip"

type PublicIPSource struct{ Name, URL string }

func PublicIPSources() []PublicIPSource {
	return []PublicIPSource{{"ip.sb", "https://api.ip.sb/ip"}, {"ipify", "https://api64.ipify.org"}, {"ifconfig.co", "https://ifconfig.co/ip"}}
}

type PublicIPReport struct {
	IP      string   `json:"ip"`
	Sources []string `json:"sources"`
}

func ParsePublicIP(value string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(value)
	ip = ip.Unmap()
	return ip, err == nil && ip.Zone() == "" && ip.IsGlobalUnicast() && !ip.IsPrivate() &&
		!ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) &&
		!netip.MustParsePrefix("0.0.0.0/8").Contains(ip) && !netip.MustParsePrefix("240.0.0.0/4").Contains(ip)
}
