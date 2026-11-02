package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/model"
)

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
