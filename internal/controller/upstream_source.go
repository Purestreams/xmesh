package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	xraytls "github.com/xtls/xray-core/transport/internet/tls"

	"xmesh/internal/model"
)

const maxSubscriptionBytes = 1 << 20

var vmessUUIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var subscriptionSchemePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*$`)
var realityShortIDPattern = regexp.MustCompile(`(?i)^(?:[0-9a-f]{2}){0,8}$`)

type vmessImport struct {
	Version     string `json:"v"`
	Name        string `json:"ps"`
	Address     string `json:"add"`
	Port        any    `json:"port"`
	UUID        string `json:"id"`
	Cipher      string `json:"scy"`
	AlterID     any    `json:"aid"`
	Network     string `json:"net"`
	Path        string `json:"path"`
	Host        string `json:"host"`
	TLS         string `json:"tls"`
	ServerName  string `json:"sni"`
	HeaderType  string `json:"type"`
	ALPN        string `json:"alpn"`
	Fingerprint string `json:"fp"`
	Insecure    any    `json:"insecure"`
	VCN         string `json:"vcn"`
	PCS         string `json:"pcs"`
}

func importNumber(value any) (int, error) {
	switch v := value.(type) {
	case float64:
		if v != float64(int(v)) {
			return 0, errors.New("invalid number")
		}
		return int(v), nil
	case string:
		return strconv.Atoi(v)
	case nil:
		return 0, nil
	default:
		return 0, errors.New("invalid number")
	}
}

func parseVMessURI(value string) (model.VMessEndpoint, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "vmess://") {
		return model.VMessEndpoint{}, errors.New("expected vmess:// link")
	}
	payload := strings.TrimPrefix(value, "vmess://")
	if i := strings.IndexAny(payload, "?#"); i >= 0 {
		payload = payload[:i]
	}
	var decoded []byte
	var err error
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err = encoding.DecodeString(payload)
		if err == nil {
			break
		}
	}
	if err != nil {
		return model.VMessEndpoint{}, errors.New("invalid VMess base64")
	}
	var item vmessImport
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&item); err != nil {
		return model.VMessEndpoint{}, errors.New("invalid VMess JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return model.VMessEndpoint{}, errors.New("invalid VMess JSON")
	}
	if item.Version != "" && item.Version != "2" {
		return model.VMessEndpoint{}, errors.New("unsupported VMess link version")
	}
	if item.HeaderType != "" && item.HeaderType != "none" || item.ALPN != "" || item.Fingerprint != "" || item.VCN != "" || item.PCS != "" {
		return model.VMessEndpoint{}, errors.New("unsupported VMess header or TLS option")
	}
	switch value := item.Insecure.(type) {
	case nil:
	case string:
		if value != "" && value != "0" {
			return model.VMessEndpoint{}, errors.New("VMess insecure TLS is unsupported")
		}
	case float64:
		if value != 0 {
			return model.VMessEndpoint{}, errors.New("VMess insecure TLS is unsupported")
		}
	case bool:
		if value {
			return model.VMessEndpoint{}, errors.New("VMess insecure TLS is unsupported")
		}
	default:
		return model.VMessEndpoint{}, errors.New("invalid VMess insecure TLS option")
	}
	port, err := importNumber(item.Port)
	if err != nil || port < 1 || port > 65535 {
		return model.VMessEndpoint{}, errors.New("invalid VMess port")
	}
	aid, err := importNumber(item.AlterID)
	if err != nil || aid != 0 {
		return model.VMessEndpoint{}, errors.New("only VMess alterId 0 is supported")
	}
	if !vmessUUIDPattern.MatchString(item.UUID) {
		return model.VMessEndpoint{}, errors.New("invalid VMess UUID")
	}
	address := strings.TrimSpace(item.Address)
	if address == "" || strings.ContainsAny(address, "/\\?#@ \t\r\n") || strings.Contains(address, ":") && net.ParseIP(address) == nil {
		return model.VMessEndpoint{}, errors.New("invalid VMess address")
	}
	network := strings.ToLower(strings.TrimSpace(item.Network))
	if network == "" {
		network = "tcp"
	}
	if network != "tcp" && network != "ws" {
		return model.VMessEndpoint{}, errors.New("only VMess TCP and WS are supported")
	}
	tls := strings.ToLower(strings.TrimSpace(item.TLS))
	if tls != "" && tls != "none" && tls != "tls" {
		return model.VMessEndpoint{}, errors.New("unsupported VMess TLS mode")
	}
	cipher := strings.ToLower(strings.TrimSpace(item.Cipher))
	if cipher == "" {
		cipher = "auto"
	}
	if cipher != "auto" && cipher != "aes-128-gcm" && cipher != "chacha20-poly1305" {
		return model.VMessEndpoint{}, errors.New("unsupported VMess cipher")
	}
	path := strings.TrimSpace(item.Path)
	if network == "ws" {
		if path == "" {
			path = "/"
		}
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n") {
			return model.VMessEndpoint{}, errors.New("invalid VMess WS path")
		}
	} else {
		path = ""
	}
	host, serverName := strings.TrimSpace(item.Host), strings.TrimSpace(item.ServerName)
	if strings.ContainsAny(host+serverName, "\r\n") {
		return model.VMessEndpoint{}, errors.New("invalid VMess host")
	}
	if tls == "tls" && serverName == "" && host != "" && !strings.ContainsAny(host, ":, 	") && net.ParseIP(host) == nil {
		serverName = host
	}
	return model.VMessEndpoint{Name: strings.TrimSpace(item.Name), Protocol: "vmess", Address: address, Port: port, UUID: item.UUID, Cipher: cipher, Network: network, Path: path, Host: host, TLS: tls == "tls", ServerName: serverName}, nil
}

func normalizeExternalLink(value string) string {
	value = strings.TrimSpace(value)
	// Some clients copy Markdown-escaped links instead of the underlying URL.
	for _, pair := range [][2]string{{`\://`, `://`}, {`\@`, `@`}, {`\.`, `.`}} {
		value = strings.ReplaceAll(value, pair[0], pair[1])
	}
	return value
}

func parseExternalURI(value string) (model.VMessEndpoint, error) {
	value = normalizeExternalLink(value)
	if strings.HasPrefix(value, "vmess://") {
		return parseVMessURI(value)
	}
	if !strings.HasPrefix(value, "vless://") {
		return model.VMessEndpoint{}, errors.New("expected vmess:// or vless:// link")
	}
	u, err := url.Parse(value)
	if err != nil || u.User == nil || u.Hostname() == "" || u.Port() == "" {
		return model.VMessEndpoint{}, errors.New("invalid VLESS URL")
	}
	if _, hasPassword := u.User.Password(); hasPassword || !vmessUUIDPattern.MatchString(u.User.Username()) || u.Path != "" {
		return model.VMessEndpoint{}, errors.New("invalid VLESS identity or path")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return model.VMessEndpoint{}, errors.New("invalid VLESS port")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return model.VMessEndpoint{}, errors.New("invalid VLESS query")
	}
	allowed := map[string]bool{"encryption": true, "flow": true, "security": true, "sni": true, "fp": true, "pbk": true, "sid": true, "spx": true, "type": true, "headerType": true}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return model.VMessEndpoint{}, fmt.Errorf("unsupported VLESS option %q", key)
		}
	}
	if q.Get("encryption") != "none" || q.Get("flow") != "xtls-rprx-vision" || q.Get("security") != "reality" || q.Get("type") != "tcp" || q.Get("headerType") != "none" {
		return model.VMessEndpoint{}, errors.New("only VLESS TCP + REALITY + Vision is supported")
	}
	serverName, publicKey, fingerprint := q.Get("sni"), q.Get("pbk"), q.Get("fp")
	keyBytes, keyErr := base64.RawURLEncoding.DecodeString(publicKey)
	if serverName == "" || strings.ContainsAny(serverName, "/\\?#@: \t\r\n") || keyErr != nil || len(keyBytes) != 32 || fingerprint == "" || strings.ContainsAny(fingerprint, " \t\r\n") || fingerprint == "unsafe" || fingerprint == "hellogolang" || xraytls.GetFingerprint(fingerprint) == nil {
		return model.VMessEndpoint{}, errors.New("invalid REALITY server name, public key or fingerprint")
	}
	shortID, spiderX := q.Get("sid"), q.Get("spx")
	if !realityShortIDPattern.MatchString(shortID) {
		return model.VMessEndpoint{}, errors.New("invalid REALITY short ID")
	}
	if spiderX == "" {
		spiderX = "/"
	}
	if !strings.HasPrefix(spiderX, "/") || strings.ContainsAny(spiderX, "\r\n") {
		return model.VMessEndpoint{}, errors.New("invalid REALITY spider path")
	}
	return model.VMessEndpoint{Name: strings.TrimSpace(u.Fragment), Protocol: "vless", Address: u.Hostname(), Port: port, UUID: u.User.Username(), Network: "raw", ServerName: serverName, Flow: "xtls-rprx-vision", PublicKey: publicKey, ShortID: shortID, Fingerprint: fingerprint, SpiderX: spiderX}, nil
}

func parseVMessSubscription(body []byte) ([]model.VMessEndpoint, error) {
	content := normalizeExternalLink(string(body))
	if content == "" {
		return []model.VMessEndpoint{}, nil
	}
	if !strings.Contains(content, "://") {
		var decoded []byte
		var err error
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			decoded, err = encoding.DecodeString(strings.Join(strings.Fields(content), ""))
			if err == nil {
				break
			}
		}
		if err != nil {
			return nil, errors.New("invalid external subscription encoding")
		}
		if !utf8.Valid(decoded) {
			return nil, errors.New("invalid external subscription text")
		}
		content = strings.TrimSpace(string(decoded))
		if content != "" && !strings.Contains(content, "://") {
			return nil, errors.New("invalid external subscription text")
		}
	}
	var result []model.VMessEndpoint
	supportedLines := 0
	for _, line := range strings.Fields(content) {
		scheme, target, ok := strings.Cut(line, "://")
		if !ok || !subscriptionSchemePattern.MatchString(scheme) || target == "" || strings.ContainsAny(line, "<>\"'") || strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https") {
			return nil, errors.New("invalid external subscription text")
		}
		if !strings.EqualFold(scheme, "vmess") && !strings.EqualFold(scheme, "vless") {
			continue
		}
		supportedLines++
		endpoint, err := parseExternalURI(line)
		if err != nil {
			continue
		}
		duplicate := false
		for _, existing := range result {
			if sameVMessConnection(existing, endpoint) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, endpoint)
		}
	}
	if supportedLines > 0 && len(result) == 0 {
		return nil, errors.New("subscription has no valid VMess or VLESS node")
	}
	return result, nil
}

func upstreamKey(endpoint model.VMessEndpoint) string {
	protocol := endpoint.Protocol
	if protocol == "" {
		protocol = "vmess"
	}
	identity := strings.Join([]string{
		protocol,
		strings.ToLower(endpoint.UUID),
		strings.ToLower(endpoint.Address),
		strconv.Itoa(endpoint.Port),
		endpoint.Network,
		endpoint.Path,
		strings.ToLower(endpoint.Host),
		strconv.FormatBool(endpoint.TLS),
		strings.ToLower(endpoint.ServerName),
		endpoint.Cipher,
		endpoint.Flow,
		endpoint.PublicKey,
		endpoint.ShortID,
		endpoint.Fingerprint,
		endpoint.SpiderX,
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return "v3-" + hex.EncodeToString(sum[:12])
}

func previousUpstreamKey(endpoint model.VMessEndpoint) string {
	identity := strings.Join([]string{strings.ToLower(endpoint.UUID), strings.ToLower(endpoint.Address), strconv.Itoa(endpoint.Port), endpoint.Network, endpoint.Path, strings.ToLower(endpoint.Host), strconv.FormatBool(endpoint.TLS), strings.ToLower(endpoint.ServerName)}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return "v2-" + hex.EncodeToString(sum[:12])
}

func legacyUpstreamKey(endpoint model.VMessEndpoint) string {
	name := endpoint.Name
	if name == "" {
		name = net.JoinHostPort(endpoint.Address, strconv.Itoa(endpoint.Port))
	}
	sum := sha256.Sum256([]byte(name + "\x00" + strings.ToLower(endpoint.UUID)))
	return hex.EncodeToString(sum[:12])
}

func sameVMessConnection(a, b model.VMessEndpoint) bool {
	a.Name, b.Name = "", ""
	if a.Protocol == "" {
		a.Protocol = "vmess"
	}
	if b.Protocol == "" {
		b.Protocol = "vmess"
	}
	return a == b
}

func subscriptionCandidates(nodes []model.VMessEndpoint) []model.UpstreamCandidate {
	result := make([]model.UpstreamCandidate, 0, len(nodes))
	seen := map[string]bool{}
	for _, node := range nodes {
		key := upstreamKey(node)
		if seen[key] {
			continue
		}
		seen[key] = true
		name := node.Name
		if name == "" {
			name = net.JoinHostPort(node.Address, strconv.Itoa(node.Port))
		}
		result = append(result, model.UpstreamCandidate{Key: key, Name: name})
	}
	return result
}

func selectedEndpoint(nodes []model.VMessEndpoint, key string) (model.VMessEndpoint, error) {
	var selected model.VMessEndpoint
	count := 0
	for _, node := range nodes {
		if upstreamKey(node) == key || strings.HasPrefix(key, "v2-") && previousUpstreamKey(node) == key || !strings.HasPrefix(key, "v3-") && !strings.HasPrefix(key, "v2-") && legacyUpstreamKey(node) == key {
			if count > 0 && sameVMessConnection(selected, node) {
				continue
			}
			selected = node
			count++
		}
	}
	if count != 1 {
		return model.VMessEndpoint{}, fmt.Errorf("selected external node has %d matches", count)
	}
	return selected, nil
}

func publicSubscriptionIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "2001:db8::/32"} {
		prefix := netip.MustParsePrefix(cidr)
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func fetchVMessSubscription(ctx context.Context, rawURL string, allowPrivate bool) ([]model.VMessEndpoint, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("subscription URL must be absolute HTTP or HTTPS")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.New("invalid subscription URL")
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		var dialErr error
		for _, ip := range ips {
			if !allowPrivate && !publicSubscriptionIP(ip) {
				continue
			}
			conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErr = err
		}
		if dialErr != nil {
			return nil, dialErr
		}
		return nil, errors.New("subscription address is not public")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Scheme != u.Scheme || req.URL.User != nil || req.URL.Fragment != "" {
			return errors.New("subscription redirect rejected")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("subscription request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription HTTP status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxSubscriptionBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read subscription: %w", err)
	}
	if len(data) > maxSubscriptionBytes {
		return nil, errors.New("subscription exceeds 1 MiB")
	}
	return parseVMessSubscription(data)
}
