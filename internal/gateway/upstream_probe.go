package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"github.com/Purestreams/xmesh/internal/controller"
	"github.com/Purestreams/xmesh/internal/model"
)

type upstreamProbeKey struct {
	revision uint64
	request  string
	epoch    uint64
}

type upstreamProbeJob struct {
	key    upstreamProbeKey
	cancel context.CancelFunc
}

func upstreamProbeAuth(secret, attachment string) (string, string) {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("upstream-probe/" + attachment))
	return "probe-" + attachment, hex.EncodeToString(mac.Sum(nil))
}

// The dedicated authenticated loopback inbound routes each probe through the
// same outbound as its users. Unknown accounts cannot fall back to direct.
func addUpstreamProbeInbound(payload []byte, config controller.GatewayConfig, address, secret string) ([]byte, error) {
	if len(config.Upstreams) == 0 || address == "" {
		return payload, nil
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || secret == "" {
		return nil, errors.New("upstream probe requires an authenticated 127.0.0.1 listener")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid upstream probe port")
	}
	var rendered xrayConfig
	if err := json.Unmarshal(payload, &rendered); err != nil {
		return nil, err
	}
	inbound := xrayInbound{Listen: host, Port: port, Protocol: "socks", Tag: "xmesh-upstream-probe", Settings: xrayInboundSettings{Auth: "password"}}
	upstreams := append([]controller.GatewayUpstreamConfig(nil), config.Upstreams...)
	sort.Slice(upstreams, func(i, j int) bool { return upstreams[i].AttachmentID < upstreams[j].AttachmentID })
	rules := []xrayRule{}
	for _, upstream := range upstreams {
		user, pass := upstreamProbeAuth(secret, upstream.AttachmentID)
		if len(user) > 255 {
			return nil, errors.New("upstream probe account is too long")
		}
		inbound.Settings.Accounts = append(inbound.Settings.Accounts, xraySOCKSUser{User: user, Pass: pass})
		rules = append(rules, xrayRule{Type: "field", InboundTag: []string{inbound.Tag}, User: []string{user}, OutboundTag: "upstream-" + upstream.AttachmentID})
	}
	rendered.Inbounds = append(rendered.Inbounds, inbound)
	rendered.Routing.Rules = append(rules, rendered.Routing.Rules...)
	return json.Marshal(rendered)
}

func (r *Runtime) upstreamProbeLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer func() {
		r.probeMu.Lock()
		defer r.probeMu.Unlock()
		for _, job := range r.probeJobs {
			job.cancel()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reconcileUpstreamProbes(ctx)
		}
	}
}

func (r *Runtime) reconcileUpstreamProbes(ctx context.Context) {
	r.mu.RLock()
	config, epoch := r.config, r.authorizationEpoch
	ready := config.Gateway.Enabled && time.Now().Before(r.authorizationUntil) &&
		r.xrayReady.Load() && r.xrayAppliedRevision.Load() == config.Revision
	r.mu.RUnlock()
	wanted := map[string]upstreamProbeKey{}
	if ready {
		for _, upstream := range config.Upstreams {
			wanted[upstream.AttachmentID] = upstreamProbeKey{config.Revision, upstream.ProbeRequestID, epoch}
		}
	}
	r.probeMu.Lock()
	defer r.probeMu.Unlock()
	if r.probeResults == nil {
		r.probeResults = map[string]model.UpstreamProbeStatus{}
	}
	if r.probeJobs == nil {
		r.probeJobs = map[string]upstreamProbeJob{}
	}
	for id, job := range r.probeJobs {
		key, exists := wanted[id]
		if !exists || key != job.key {
			job.cancel()
		}
	}
	for id := range r.probeResults {
		if _, exists := wanted[id]; !exists {
			delete(r.probeResults, id)
		}
	}
	for _, upstream := range config.Upstreams {
		id := upstream.AttachmentID
		key, exists := wanted[id]
		if !exists {
			continue
		}
		if _, running := r.probeJobs[id]; running {
			continue
		}
		if result, ok := r.probeResults[id]; ok && result.Revision == key.revision &&
			result.RequestID == key.request && !result.CompletedAt.IsZero() &&
			time.Since(result.CompletedAt) < r.local.Gateway.UpstreamProbeInterval.Value(time.Minute) {
			continue
		}
		if len(r.probeJobs) >= 4 {
			break
		}
		jobCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		r.probeJobs[id] = upstreamProbeJob{key: key, cancel: cancel}
		status := model.UpstreamProbeStatus{AttachmentID: id, Revision: key.revision, RequestID: key.request, StartedAt: time.Now().UTC(), Targets: []model.UpstreamProbeTarget{}}
		r.probeResults[id] = status
		go r.executeUpstreamProbe(jobCtx, status, key)
	}
}

func (r *Runtime) executeUpstreamProbe(ctx context.Context, status model.UpstreamProbeStatus, key upstreamProbeKey) {
	client, transport, err := upstreamProbeClient(r.local.Gateway.UpstreamProbeListen, r.local.Credential, status.AttachmentID)
	if transport != nil {
		defer transport.CloseIdleConnections()
	}
	status.Targets = probeUpstreamHTTP(ctx, client, err)
	status.CompletedAt = time.Now().UTC()
	r.probeMu.Lock()
	defer r.probeMu.Unlock()
	if job, exists := r.probeJobs[status.AttachmentID]; exists && job.key == key {
		delete(r.probeJobs, status.AttachmentID)
		if ctx.Err() == nil {
			r.probeResults[status.AttachmentID] = status
		} else {
			delete(r.probeResults, status.AttachmentID)
		}
		job.cancel()
	}
}

func upstreamProbeClient(address, secret, attachmentID string) (*http.Client, *http.Transport, error) {
	user, pass := upstreamProbeAuth(secret, attachmentID)
	dialer, err := proxy.SOCKS5("tcp", address, &proxy.Auth{User: user, Password: pass}, &net.Dialer{Timeout: 10 * time.Second})
	if err != nil {
		return nil, nil, err
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, nil, errors.New("SOCKS dialer does not support cancellation")
	}
	transport := &http.Transport{DialContext: contextDialer.DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second, DisableKeepAlives: true}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: probeRedirect}, transport, nil
}

func (r *Runtime) upstreamProbeSnapshot(config controller.GatewayConfig) []model.UpstreamProbeStatus {
	r.probeMu.Lock()
	defer r.probeMu.Unlock()
	results := []model.UpstreamProbeStatus{}
	for _, upstream := range config.Upstreams {
		if result, ok := r.probeResults[upstream.AttachmentID]; ok &&
			result.Revision == config.Revision && result.RequestID == upstream.ProbeRequestID {
			results = append(results, result)
		}
	}
	return results
}

func probeRedirect(req *http.Request, via []*http.Request) error {
	host := strings.ToLower(req.URL.Hostname())
	firstHost := strings.ToLower(via[0].URL.Hostname())
	domain := "microsoft.com"
	if firstHost == "api.ip.sb" {
		domain = "ip.sb"
	}
	if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil ||
		(req.URL.Port() != "" && req.URL.Port() != "443") ||
		(host != domain && !strings.HasSuffix(host, "."+domain)) {
		return errors.New("test endpoint redirected outside its HTTPS domain")
	}
	return nil
}

func probeUpstreamHTTP(ctx context.Context, client *http.Client, setupErr error) []model.UpstreamProbeTarget {
	targets := []model.UpstreamProbeTarget{{URL: model.UpstreamIPProbeURL}, {URL: model.UpstreamMicrosoftProbeURL}}
	for i := range targets {
		target := &targets[i]
		start := time.Now()
		if setupErr != nil {
			target.Error = setupErr.Error()
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
		if err == nil {
			req.Header.Set("User-Agent", "XMesh-Upstream-Probe/1.0")
			var resp *http.Response
			resp, err = client.Do(req)
			if err == nil {
				target.HTTPStatus = resp.StatusCode
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
				_ = resp.Body.Close()
				switch {
				case resp.StatusCode < 200 || resp.StatusCode >= 400:
					err = fmt.Errorf("HTTP %d", resp.StatusCode)
				case readErr != nil:
					err = readErr
				case i == 0:
					ip, parseErr := netip.ParseAddr(strings.TrimSpace(string(body)))
					if parseErr != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
						err = errors.New("ip.sb did not return a public IP address")
					} else {
						target.ExitIP = ip.String()
					}
				}
			} else if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
		}
		target.DurationMillis = time.Since(start).Milliseconds()
		target.Success = err == nil
		if err != nil {
			target.Error = err.Error()
			if len(target.Error) > 512 {
				target.Error = target.Error[:512]
			}
		}
	}
	return targets
}
