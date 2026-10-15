package nodeclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Purestreams/xmesh/internal/controller"
	"github.com/Purestreams/xmesh/internal/model"
)

func TestErrorResponseDrainsBoundedBody(t *testing.T) {
	for _, size := range []int{8192, 1 << 20} {
		body := bytes.NewBuffer(bytes.Repeat([]byte("x"), size))
		err := responseError(&http.Response{StatusCode: 500, Status: "500 Internal Server Error", Body: io.NopCloser(body)})
		if err == nil {
			t.Fatal("error response was accepted")
		}
		if size == 8192 && body.Len() != 0 {
			t.Fatal("small response not drained for keep-alive")
		}
		if size > 64<<10 && body.Len() == 0 {
			t.Fatal("unbounded body drain")
		}
	}
}

func TestReportBoundsDiagnosticsWithoutLosingStatus(t *testing.T) {
	long := "Xray error: \u001b\u202e" + strings.Repeat("证", 500)
	var received controller.StatusReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	status := model.NodeStatus{NodeID: "g", Online: true, LastError: long, ApplyError: long, XrayError: long}
	links := []model.LinkStatus{{LastError: long}}
	grants := []model.GrantStatus{{LastError: long}}
	if err := New(server.URL, "credential").Report(context.Background(), status, links, grants); err != nil {
		t.Fatal(err)
	}
	if received.Status.NodeID != "g" || !received.Status.Online {
		t.Fatal("diagnostic truncation lost node state")
	}
	for _, text := range []string{received.Status.LastError, received.Status.ApplyError, received.Status.XrayError, received.Links[0].LastError, received.Grants[0].LastError} {
		if len(text) > 1024 || !utf8.ValidString(text) || !strings.HasPrefix(text, "Xray error: 证") || strings.ContainsAny(text, "\u001b\u202e") {
			t.Fatalf("invalid diagnostic: bytes=%d", len(text))
		}
	}
	if status.LastError != long || links[0].LastError != long || grants[0].LastError != long {
		t.Fatal("report modified caller's diagnostics")
	}
}
