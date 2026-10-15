package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPublicRequestLimitsBeforeAuthentication(t *testing.T) {
	for _, tc := range []struct {
		path  string
		burst int
	}{
		{"/api/v1/enroll", 10}, {"/api/v1/updater/pair", 10}, {"/api/v1/config", 200},
		{"/api/v1/status", 200}, {"/subscription/invalid", 20},
	} {
		t.Run(tc.path, func(t *testing.T) {
			now := time.Now()
			s := &Server{now: func() time.Time { return now }}
			calls := 0
			h := s.limitPublicRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(401) }))
			request := func(ip string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("GET", tc.path, nil)
				r.RemoteAddr = ip + ":1234"
				r.Header.Set("X-Forwarded-For", "192.0.2."+time.Now().Format("05"))
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			for i := 0; i < tc.burst; i++ {
				if w := request("127.0.0.1"); w.Code != 401 {
					t.Fatalf("limited valid burst at %d: %d", i, w.Code)
				}
			}
			if w := request("127.0.0.1"); w.Code != 429 || w.Header().Get("Retry-After") == "" || calls != tc.burst {
				t.Fatal("invalid requests reached authentication after exhausting burst")
			}
			if w := request("127.0.0.2"); w.Code != 401 {
				t.Fatal("other TCP peer was blocked")
			}
			now = now.Add(time.Second)
			if w := request("127.0.0.1"); w.Code != 401 {
				t.Fatal("rate limit did not refill")
			}
		})
	}
}

func TestStatusTextBounds(t *testing.T) {
	var report StatusReport
	report.Status.LastError = string(make([]byte, 1025))
	if validateStatusText(report) == nil {
		t.Fatal("oversized error accepted")
	}
	report.Status.LastError = "spoof\u202emessage"
	if validateStatusText(report) == nil {
		t.Fatal("bidi control accepted")
	}
	report.Status.LastError = "TLS error: 证书无效\nplease retry"
	if err := validateStatusText(report); err != nil {
		t.Fatal(err)
	}
}
