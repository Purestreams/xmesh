package controller

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/auth"
)

func TestAdminSessionRecoveryRenewsCSRFWithoutAcceptingOldTokens(t *testing.T) {
	server, store := testServer(t, securityState)
	request := func(method, path, session string, values url.Values) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if session != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w
	}
	oldSession := auth.SignSession(server.cfg.adminSessionKey(), "admin", server.now().Add(time.Hour))
	newSession := auth.SignSession(server.cfg.adminSessionKey(), "admin", server.now().Add(time.Hour))
	oldToken := auth.Derive(server.cfg.sessionKey(), "csrf", oldSession)
	newToken := auth.Derive(server.cfg.sessionKey(), "csrf", newSession)
	for _, session := range []string{oldSession, newSession} {
		w := request(http.MethodGet, "/admin/dashboard", session, nil)
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" ||
			w.Header().Get("X-XMesh-CSRF-Token") != auth.Derive(server.cfg.sessionKey(), "csrf", session) {
			t.Fatal("authenticated refresh did not provide its current session token")
		}
	}
	for _, session := range []string{"", "invalid-session"} {
		w := request(http.MethodGet, "/admin/dashboard", session, nil)
		if w.Code != http.StatusSeeOther || w.Header().Get("X-XMesh-CSRF-Token") != "" {
			t.Fatal("unauthenticated request received a session token")
		}
	}
	values := url.Values{"csrf": {oldToken}, "name": {"Recovered profile"}, "notes": {"Draft preserved"}}
	w := request(http.MethodPost, "/admin/users/u/edit", newSession, values)
	if w.Code != http.StatusForbidden || w.Header().Get("X-XMesh-CSRF-Token") != newToken || store.Snapshot().Users["u"].Name == "Recovered profile" {
		t.Fatal("old CSRF token was accepted or the new session token was not returned")
	}
	values.Set("csrf", newToken)
	w = request(http.MethodPost, "/admin/users/u/edit", newSession, values)
	if w.Code != http.StatusSeeOther || store.Snapshot().Users["u"].Name != "Recovered profile" || store.Snapshot().Users["u"].Notes != "Draft preserved" {
		t.Fatal("retry with the recovered session could not save the draft")
	}
}
