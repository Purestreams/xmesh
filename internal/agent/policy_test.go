package agent

import (
	"context"
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
