package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/model"
)

func TestDiscoverPublicIPChecksBothFamilies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		v4, v6 bool
	}{
		{"dual-stack", true, true}, {"ipv4-only", true, false}, {"ipv6-only", false, true}, {"unavailable", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var queried []string
			report, err := discoverPublicIPs(context.Background(), func(_ context.Context, network string) (model.PublicIPReport, error) {
				queried = append(queried, network)
				if network == "tcp4" && tc.v4 {
					return model.PublicIPReport{IP: "8.8.8.8", Sources: []string{"ip.sb", "ipify"}}, nil
				}
				if network == "tcp6" && tc.v6 {
					return model.PublicIPReport{IP: "2606:4700:4700::1111", Sources: []string{"ip.sb", "ifconfig.co"}}, nil
				}
				return model.PublicIPReport{}, errors.New("unavailable")
			})
			if len(queried) != 2 || (err == nil) != (tc.v4 || tc.v6) {
				t.Fatalf("queries=%v report=%+v err=%v", queried, report, err)
			}
			if tc.v4 && report.IP != "8.8.8.8" {
				t.Fatal("IPv6 replaced IPv4", report)
			}
			if tc.v6 && (report.IPv6 != "2606:4700:4700::1111" || len(report.IPv6Sources) != 2) {
				t.Fatal("IPv6 consensus lost", report)
			}
		})
	}
}

func TestPublicIPRequiresIndependentSourceConsensus(t *testing.T) {
	for _, tc := range []struct {
		name, network, want string
		answers             [3]string
	}{
		{"all-agree", "tcp4", "8.8.8.8", [3]string{"8.8.8.8", "8.8.8.8\n", "8.8.8.8"}},
		{"majority", "tcp4", "8.8.8.8", [3]string{"8.8.8.8", "1.1.1.1", "8.8.8.8"}},
		{"one-unavailable", "tcp4", "8.8.8.8", [3]string{"8.8.8.8", "<html>blocked</html>", "8.8.8.8"}},
		{"disagree", "tcp4", "", [3]string{"8.8.8.8", "1.1.1.1", "9.9.9.9"}},
		{"one-vote", "tcp4", "", [3]string{"8.8.8.8", "127.0.0.1", "10.0.0.1"}},
		{"carrier-nat", "tcp4", "", [3]string{"100.64.0.1", "100.64.0.1", "100.64.0.1"}},
		{"ipv6", "tcp6", "2606:4700:4700::1111", [3]string{"2606:4700:4700::1111", "2606:4700:4700::1111", "error"}},
		{"wrong-family", "tcp4", "", [3]string{"2606:4700:4700::1111", "2606:4700:4700::1111", "error"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bodies := map[string]string{}
			for i, source := range model.PublicIPSources() {
				bodies[source.URL] = tc.answers[i]
			}
			client := &http.Client{Transport: probeRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "" || r.URL.Scheme != "https" {
					t.Error("public IP request leaked credentials or used plaintext")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(bodies[r.URL.String()]))}, nil
			})}
			report, err := queryPublicIP(context.Background(), client, tc.network)
			if report.IP != tc.want || (err != nil) != (tc.want == "") || (err == nil && len(report.Sources) < 2) {
				t.Fatalf("report=%+v err=%v want=%s", report, err, tc.want)
			}
		})
	}
}

func TestPublicIPCancellationDoesNotWaitForSources(t *testing.T) {
	client := &http.Client{Transport: probeRoundTripper(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := queryPublicIP(ctx, client, "tcp4"); err == nil || time.Since(started) > time.Second {
		t.Fatalf("cancellation not respected: %v", err)
	}
}
