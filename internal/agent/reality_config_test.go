package agent

import (
	"testing"

	"github.com/Purestreams/xmesh/internal/controller"
	"github.com/Purestreams/xmesh/internal/model"
	"github.com/xtls/xray-core/core"
)

func TestRealityClientConfigStartsEmbeddedCore(t *testing.T) {
	link := controller.AgentLinkConfig{Link: model.Link{URL: "reality://127.0.0.1:18443/tunnel", RealityUUID: "00000000-0000-4000-8000-000000000001", RealityShortID: "0123456789abcdef"}, RealityPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", RealityName: "example.com"}
	b, err := buildRealityClientConfig(link)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := core.StartInstance("json", b)
	if err != nil {
		t.Fatalf("embedded Xray rejected REALITY config: %v\n%s", err, b)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
}
