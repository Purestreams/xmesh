package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/model"
)

func TestLoginRejectsCrossOriginAndAmbiguousCredentials(t *testing.T) {
	credentials := url.Values{"username": {"admin"}, "password": {"a-strong-test-password"}}
	for _, test := range []struct {
		name, origin, site, query, body string
		status                          int
	}{
		{name: "cross-site origin", origin: "https://attacker.example", body: credentials.Encode(), status: 403},
		{name: "sibling origin", origin: "https://untrusted.panel.example", site: "same-site", body: credentials.Encode(), status: 403},
		{name: "cross-site fetch", site: "cross-site", body: credentials.Encode(), status: 403},
		{name: "opaque origin", origin: "null", body: credentials.Encode(), status: 403},
		{name: "query credentials", query: credentials.Encode(), status: 400},
		{name: "duplicate username", body: credentials.Encode() + "&username=admin", status: 400},
		{name: "duplicate password", body: credentials.Encode() + "&password=wrong", status: 400},
		{name: "oversized body", body: credentials.Encode() + "&padding=" + strings.Repeat("x", 9<<10), status: 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, state := testServer(t, nil)
			before := state.Snapshot()
			r := httptest.NewRequest(http.MethodPost, "https://panel.example/login?"+test.query, strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if test.origin != "" {
				r.Header.Set("Origin", test.origin)
			}
			if test.site != "" {
				r.Header.Set("Sec-Fetch-Site", test.site)
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != test.status || len(w.Result().Cookies()) != 0 {
				t.Fatalf("login accepted unsafe input: status=%d cookies=%d", w.Code, len(w.Result().Cookies()))
			}
			if !reflect.DeepEqual(before, state.Snapshot()) {
				t.Fatal("rejected login changed persisted state")
			}
		})
	}
}

func TestLoginConcurrentAttemptsCannotBypassRateLimit(t *testing.T) {
	s, _ := testServer(t, nil)
	handler := s.Handler()
	const attempts = 16
	start := make(chan struct{})
	statuses := make(chan int, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=wrong"))
			r.RemoteAddr = "192.0.2.1:" + strconv.Itoa(1000+i)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			// Untrusted forwarded addresses must not provide fresh quotas.
			r.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i))
			w := httptest.NewRecorder()
			<-start
			handler.ServeHTTP(w, r)
			statuses <- w.Code
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	accepted := 0
	for status := range statuses {
		switch status {
		case http.StatusOK:
			accepted++
		case http.StatusTooManyRequests:
		default:
			t.Fatalf("unexpected login status %d", status)
		}
	}
	if accepted < 1 || accepted > 5 {
		t.Fatalf("concurrent requests bypassed five-attempt limit: accepted=%d", accepted)
	}
}

func TestLoginGlobalBudgetAndSuccessfulSession(t *testing.T) {
	for _, test := range []struct {
		name, origin, site string
	}{
		{name: "browser", origin: "https://panel.example", site: "same-origin"},
		{name: "non-browser"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _ := testServer(t, nil)
			handler := s.Handler()
			login := func(ip, password string) *httptest.ResponseRecorder {
				t.Helper()
				values := url.Values{"username": {"admin"}, "password": {password}}
				r := httptest.NewRequest(http.MethodPost, "https://panel.example/login", strings.NewReader(values.Encode()))
				r.RemoteAddr = ip + ":12345"
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if test.origin != "" {
					r.Header.Set("Origin", test.origin)
					r.Header.Set("Sec-Fetch-Site", test.site)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			for i := range 20 {
				if w := login("192.0.2."+strconv.Itoa(i+1), "wrong"); w.Code != http.StatusOK {
					t.Fatalf("login blocked before global budget exhausted: %d", w.Code)
				}
			}
			if w := login("192.0.2.100", "a-strong-test-password"); w.Code != http.StatusTooManyRequests || len(w.Result().Cookies()) != 0 || w.Header().Get("Retry-After") == "" {
				t.Fatal("rotating source IP bypassed global login budget", w.Code)
			}
			now := s.now()
			s.now = func() time.Time { return now.Add(time.Minute) }
			w := login("192.0.2.100", "a-strong-test-password")
			cookies := w.Result().Cookies()
			if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" || w.Header().Get("Cache-Control") != "no-store" || len(cookies) != 1 {
				t.Fatalf("normal login failed after refill: %d %s", w.Code, w.Body.String())
			}
			cookie := cookies[0]
			if cookie.Name != sessionCookie || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
				t.Fatalf("insecure session cookie: %+v", cookie)
			}
			r := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
			r.AddCookie(cookie)
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatal("new session not authorized", w.Code)
			}
			values := url.Values{"csrf": {auth.Derive(s.cfg.sessionKey(), "csrf", cookie.Value)}, "name": {"security regression user"}}
			r = httptest.NewRequest(http.MethodPost, "https://panel.example/admin/users", strings.NewReader(values.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "https://panel.example")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.AddCookie(cookie)
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusSeeOther || w.Header().Get("Location") == "/login" {
				t.Fatalf("normal authenticated form failed: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestNodeAndUpdaterAPIsKeepCredentialsIsolated(t *testing.T) {
	s, state := testServer(t, securityState)
	if err := state.Update(func(v *model.State) error {
		v.Updaters["g"] = model.Updater{NodeID: "g", Role: model.RoleGateway, CredentialHash: auth.SecretHash("updater-secret")}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := state.Snapshot()
	admin := auth.SignSession(s.cfg.adminSessionKey(), "admin", s.now().Add(time.Hour))
	handler := s.Handler()
	for _, route := range []struct {
		method, path, body string
		updater            bool
	}{
		{http.MethodGet, "/api/v1/config", "", false},
		{http.MethodGet, "/api/v1/self/status", "", false},
		{http.MethodPost, "/api/v1/status", `{}`, false},
		{http.MethodPost, "/api/v1/updater/heartbeat", `{}`, true},
		{http.MethodGet, "/api/v1/updater/task", "", true},
		{http.MethodPost, "/api/v1/updater/manual", `{}`, true},
		{http.MethodPost, "/api/v1/updater/task/task/report", `{}`, true},
	} {
		t.Run(route.path, func(t *testing.T) {
			wrongCredentials := []string{"", "invalid", "old-secret", "updater-secret"}
			if route.updater {
				wrongCredentials = []string{"", "invalid", "old-secret", "gateway-secret", "agent-secret"}
			}
			for _, credential := range wrongCredentials {
				r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
				r.AddCookie(&http.Cookie{Name: sessionCookie, Value: admin})
				if credential != "" {
					r.Header.Set("Authorization", "Bearer "+credential)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("API accepted unrelated credentials %q: %d", credential, w.Code)
				}
			}
		})
	}
	for _, route := range []struct{ path, body string }{
		{"/api/v1/enroll", `{"token":"invalid","role":"gateway","node_id":"g"}`},
		{"/api/v1/updater/pair", `{"token":"invalid","role":"gateway","node_id":"g"}`},
	} {
		r := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(route.body))
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: admin})
		r.Header.Set("Authorization", "Bearer gateway-secret")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s bypassed one-time token check: %d", route.path, w.Code)
		}
	}
	if !reflect.DeepEqual(before, state.Snapshot()) {
		t.Fatal("unauthorized API request changed persisted state")
	}
}

// Discover the registered management routes so future endpoints are included
// automatically. Exercise the actual handler and check that denials never write.
func TestEveryManagementRouteRejectsInvalidSessionsAndCSRF(t *testing.T) {
	s, state := testServer(t, securityState)
	cookie := auth.SignSession(s.cfg.adminSessionKey(), "admin", s.now().Add(time.Hour))
	otherSession := auth.SignSession(s.cfg.adminSessionKey(), "admin", s.now().Add(time.Hour))
	revoked := auth.SignSession(s.cfg.adminSessionKey(), "admin", s.now().Add(time.Hour))
	if err := state.Update(func(v *model.State) error {
		v.RevokedAdminSessions = map[string]time.Time{auth.SecretHash(revoked): s.now().Add(time.Hour)}
		v.Updaters["g"] = model.Updater{NodeID: "g", Role: model.RoleGateway, CredentialHash: auth.SecretHash("updater-secret")}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var routes []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		pattern, ok := call.Args[0].(*ast.BasicLit)
		if !ok || pattern.Kind != token.STRING {
			return true
		}
		route, err := strconv.Unquote(pattern.Value)
		if err != nil {
			t.Fatal(err)
		}
		_, path, _ := strings.Cut(route, " ")
		if path == "/" || path == "/logout" || strings.HasPrefix(path, "/admin/") {
			routes = append(routes, route)
		}
		return true
	})
	if len(routes) < 40 {
		t.Fatalf("management route discovery failed: %d", len(routes))
	}
	t.Logf("checking %d management routes", len(routes))
	before := state.Snapshot()
	handler := s.Handler()
	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			method, path, _ := strings.Cut(route, " ")
			path = strings.NewReplacer("{id}", "u", "{role}", "gateway").Replace(path)
			for _, test := range []struct {
				name, session, bearer, csrf, origin string
				query                               bool
				status                              int
			}{
				{name: "anonymous", status: 303},
				{name: "forged cookie", session: cookie + "x", status: 303},
				{name: "expired cookie", session: auth.SignSession(s.cfg.adminSessionKey(), "admin", s.now()), status: 303},
				{name: "wrong principal", session: auth.SignSession(s.cfg.adminSessionKey(), "other", s.now().Add(time.Hour)), status: 303},
				{name: "revoked cookie", session: revoked, status: 303},
				{name: "node credential", bearer: "gateway-secret", status: 303},
				{name: "updater credential", bearer: "updater-secret", status: 303},
				{name: "missing csrf", session: cookie, status: 403},
				{name: "other session csrf", session: cookie, csrf: auth.Derive(s.cfg.sessionKey(), "csrf", otherSession), status: 403},
				{name: "query csrf", session: cookie, csrf: auth.Derive(s.cfg.sessionKey(), "csrf", cookie), query: true, status: 403},
				{name: "cross-origin csrf", session: cookie, csrf: auth.Derive(s.cfg.sessionKey(), "csrf", cookie), origin: "https://attacker.example", status: 403},
			} {
				if method != http.MethodPost && test.status == 403 {
					continue
				}
				t.Run(test.name, func(t *testing.T) {
					values := url.Values{"csrf": {test.csrf}}
					target, body := path, values.Encode()
					if test.query {
						target, body = path+"?"+body, ""
					}
					r := httptest.NewRequest(method, target, strings.NewReader(body))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					if test.session != "" {
						r.AddCookie(&http.Cookie{Name: sessionCookie, Value: test.session})
					}
					if test.bearer != "" {
						r.Header.Set("Authorization", "Bearer "+test.bearer)
					}
					if test.origin != "" {
						r.Header.Set("Origin", test.origin)
					}
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, r)
					if w.Code != test.status || test.status == 303 && w.Header().Get("Location") != "/login" {
						t.Fatalf("security boundary failed: %d %s", w.Code, w.Body.String())
					}
				})
			}
		})
	}
	if !reflect.DeepEqual(before, state.Snapshot()) {
		t.Fatal("rejected management request mutated persisted state")
	}
}
