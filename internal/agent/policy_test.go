package agent

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"xmesh/internal/model"
)

func TestPolicyDenyOverridesAllow(t *testing.T) {
	p, err := compilePolicy(model.Agent{AllowedCIDRs: []string{"0.0.0.0/0"}, DeniedCIDRs: []string{"10.0.0.0/8"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.resolve(context.Background(), "10.1.2.3", 443); err == nil {
		t.Fatal("denied address accepted")
	}
	addresses, err := p.resolve(context.Background(), "192.0.2.1", 443)
	if err != nil || len(addresses) != 1 {
		t.Fatalf("allowed address failed: %v", err)
	}
}

func TestDefaultSMTPBlockIsTCPOnlyAndOverridesAllowList(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // A regression must fail promptly without attempting a real dial.
	for _, ports := range [][]int{nil, {25, 465, 587, 2525, 80, 443}} {
		p, err := compilePolicy(model.Agent{AllowedCIDRs: []string{"0.0.0.0/0", "::/0"}, AllowedPorts: ports})
		if err != nil {
			t.Fatal(err)
		}
		for _, port := range []int{25, 465, 587, 2525} {
			for _, host := range []string{"192.0.2.1", "2001:db8::1", "smtp.invalid"} {
				t.Run(fmt.Sprintf("allow=%v/%s/%d", ports, host, port), func(t *testing.T) {
					conn, err := p.dialTCP(ctx, host, port)
					if conn != nil {
						defer conn.Close()
					}
					if conn != nil || err == nil || !strings.Contains(err.Error(), "denied by default policy") {
						t.Fatalf("SMTP target was not rejected before DNS/dial: conn=%v err=%v", conn, err)
					}
				})
			}
			for _, host := range []string{"192.0.2.1", "2001:db8::1"} {
				if p.permitsTCP(&net.TCPAddr{IP: net.ParseIP(host), Port: port}) {
					t.Fatalf("TCP policy recheck accepted %s:%d", host, port)
				}
				if _, err := p.udpAddress(context.Background(), host, port); err != nil {
					t.Fatalf("TCP block affected UDP %s:%d: %v", host, port, err)
				}
			}
		}
		for _, port := range []int{80, 443} {
			if !p.permitsTCP(&net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: port}) {
				t.Fatalf("normal TCP port %d blocked", port)
			}
		}
	}
}
