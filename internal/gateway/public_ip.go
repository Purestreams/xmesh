package gateway

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Purestreams/xmesh/internal/model"
)

// Use the physical host's direct egress, independently of environment proxies,
// managed Agent routes and external upstreams. Query each address family alone
// so a dual-stack host does not mistake IPv4/IPv6 answers for disagreement.
func discoverPublicIP(ctx context.Context) (model.PublicIPReport, error) {
	for _, network := range []string{"tcp4", "tcp6"} {
		dialer := &net.Dialer{Timeout: 4 * time.Second}
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _ string, address string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, address)
			},
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   4 * time.Second,
			ResponseHeaderTimeout: 4 * time.Second,
		}
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		report, err := queryPublicIP(ctx, client, network)
		transport.CloseIdleConnections()
		if err == nil {
			return report, nil
		}
		if ctx.Err() != nil {
			return model.PublicIPReport{}, ctx.Err()
		}
	}
	return model.PublicIPReport{}, fmt.Errorf("public IP sources did not reach a two-source consensus")
}

func queryPublicIP(ctx context.Context, client *http.Client, network string) (model.PublicIPReport, error) {
	type answer struct{ source, ip string }
	sources := model.PublicIPSources()
	answers := make(chan answer, len(sources))
	for _, source := range sources {
		go func() {
			result := answer{source: source.Name}
			defer func() { answers <- result }()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
			if err != nil {
				return
			}
			request.Header.Set("Accept", "text/plain")
			request.Header.Set("User-Agent", "xmesh-public-ip/1")
			response, err := client.Do(request)
			if err != nil {
				return
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return
			}
			body, err := io.ReadAll(io.LimitReader(response.Body, 129))
			if err != nil || len(body) > 128 {
				return
			}
			ip, ok := model.ParsePublicIP(strings.TrimSpace(string(body)))
			if ok && ((network == "tcp4") == ip.Is4()) {
				result.ip = ip.String()
			}
		}()
	}
	votes := map[string][]string{}
	for range sources {
		select {
		case <-ctx.Done():
			return model.PublicIPReport{}, ctx.Err()
		case answer := <-answers:
			if answer.ip != "" {
				votes[answer.ip] = append(votes[answer.ip], answer.source)
			}
		}
	}
	for ip, sources := range votes {
		if len(sources) >= 2 {
			return model.PublicIPReport{IP: ip, Sources: sources}, nil
		}
	}
	return model.PublicIPReport{}, fmt.Errorf("public IP sources disagree or are unavailable")
}

func (r *Runtime) publicIPLoop(ctx context.Context) {
	discover := r.publicIPDiscover
	if discover == nil {
		discover = discoverPublicIP
	}
	for ctx.Err() == nil {
		queryCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		report, err := discover(queryCtx)
		cancel()
		if err == nil {
			err = r.client.AnnouncePublicIP(ctx, report)
		}
		delay := 15 * time.Minute
		if err != nil {
			delay = time.Minute
			if ctx.Err() == nil {
				r.logger.Warn("public IP discovery/announcement; retaining last confirmed address", "error", err)
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
