package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
)

func TestLoginRateLimitExpires(t *testing.T) {
	server, _ := testServer(t, nil)
	now := server.now()
	for range 5 {
		if !server.beginLoginAttempt("192.0.2.1") {
			t.Fatal("login limited before five attempts")
		}
		server.finishLoginAttempt("192.0.2.1", false)
	}
	if server.beginLoginAttempt("192.0.2.1") || !server.beginLoginAttempt("192.0.2.2") {
		t.Fatal("rate limit not scoped to source IP")
	}
	server.finishLoginAttempt("192.0.2.2", false)
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=wrong"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.RemoteAddr = "192.0.2.1:12345"
	recorder := httptest.NewRecorder()
	server.login(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("login endpoint did not enforce limit: %d", recorder.Code)
	}
	server.now = func() time.Time { return now.Add(16 * time.Minute) }
	if !server.beginLoginAttempt("192.0.2.1") {
		t.Fatal("rate limit did not expire")
	}
	server.finishLoginAttempt("192.0.2.1", false)
}

func TestPanelRendersManagementAndDeleteImpact(t *testing.T) {
	server, _ := testServer(t, func(s *model.State) error {
		s.Gateways["g"] = model.Gateway{ID: "g", Name: "Gateway", PublicHost: "edge.example", VMessPort: 8080, VMessPath: "/proxy"}
		s.Agents["a"] = model.Agent{ID: "a", Name: "Agent", AllowedCIDRs: []string{"0.0.0.0/0"}}
		s.Attachments["n"] = model.Attachment{ID: "n", GatewayID: "g", AgentID: "a"}
		s.Links["l"] = model.Link{ID: "l", Name: "Link", AttachmentID: "n", URL: "reality://edge.example:8443/tunnel"}
		s.Grants["r"] = model.Grant{ID: "r", AttachmentID: "n"}
		return nil
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.SignSession(server.cfg.adminSessionKey(), "admin", server.now().Add(time.Hour))})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("panel: %d %s", recorder.Code, recorder.Body.String())
	}
	text := strings.NewReplacer("<span data-i18n>", "", "</span>", "").Replace(recorder.Body.String())
	for _, want := range []string{"删除影响：1 个节点组合、1 条 Link、1 项授权", "删除节点组合", "删除 Link", "删除授权", "刷新状态", "单独添加 Gateway", "单独添加 Agent", "线路总览"} {
		if !strings.Contains(text, want) {
			t.Fatalf("panel missing %q", want)
		}
	}
}

func TestGrantPanelShowsUserRouteAndCustomLinkNames(t *testing.T) {
	server, _ := testServer(t, func(s *model.State) error {
		s.Users["user-id"] = model.User{ID: "user-id", Name: "Alice", Enabled: true}
		s.Gateways["gateway-id"] = model.Gateway{ID: "gateway-id", Name: "香港入口"}
		s.Agents["agent-id"] = model.Agent{ID: "agent-id", Name: "家庭出口"}
		s.Upstreams["upstream-id"] = model.VMessUpstream{ID: "upstream-id", Name: "日本出口"}
		s.Attachments["agent-route-id"] = model.Attachment{ID: "agent-route-id", GatewayID: "gateway-id", AgentID: "agent-id"}
		s.Attachments["external-route-id"] = model.Attachment{ID: "external-route-id", GatewayID: "gateway-id", UpstreamID: "upstream-id"}
		s.Links["link-id"] = model.Link{ID: "link-id", Name: "我的线路 <home>", AttachmentID: "agent-route-id"}
		s.Grants["agent-grant-id"] = model.Grant{ID: "agent-grant-id", UserID: "user-id", AttachmentID: "agent-route-id", Enabled: true}
		s.Grants["external-grant-id"] = model.Grant{ID: "external-grant-id", UserID: "user-id", AttachmentID: "external-route-id"}
		return nil
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.SignSession(server.cfg.adminSessionKey(), "admin", server.now().Add(time.Hour))})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	body := response.Body.String()
	start := strings.Index(body, `<section id="grants"`)
	grants := body[start : start+strings.Index(body[start:], "</section>")]
	for _, want := range []string{"Alice", "香港入口 / 家庭出口", "我的线路 &lt;home&gt;", "香港入口 / 日本出口", `form="grant-bulk"`, `action="/admin/grants/agent-grant-id/state"`} {
		if !strings.Contains(grants, want) {
			t.Fatalf("grant panel missing %q", want)
		}
	}
	if strings.Contains(grants, "查看授权记录与管理操作") || strings.Contains(grants, `<home>`) {
		t.Fatal("management is still collapsed or custom names are not escaped")
	}
}

func TestGrantFragmentRequiresAdminAndMatchesHY2Authorization(t *testing.T) {
	server, state := testServer(t, func(s *model.State) error {
		s.Users["u"] = model.User{ID: "u", Name: "User", Enabled: true, SubscriptionToken: "private-subscription"}
		s.Gateways["g"] = model.Gateway{ID: "g", Name: "Gateway", PublicHost: "edge.example", Enabled: true, HY2Port: 443}
		s.Agents["a"] = model.Agent{ID: "a", Name: "Agent", Enabled: true}
		s.Attachments["r"] = model.Attachment{ID: "r", GatewayID: "g", AgentID: "a", Enabled: true}
		s.Links["l"] = model.Link{ID: "l", AttachmentID: "r", Enabled: true}
		s.Grants["grant"] = model.Grant{ID: "grant", UserID: "u", AttachmentID: "r", Enabled: true, Published: true, HY2Auth: "secret"}
		s.NodeStatus["g"] = model.NodeStatus{HY2Ready: true}
		return nil
	})
	anonymous := httptest.NewRecorder()
	server.Handler().ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/admin/grants", nil))
	if anonymous.Code != http.StatusSeeOther || strings.Contains(anonymous.Body.String(), "private-subscription") {
		t.Fatal("grant fragment exposed private management data without authentication")
	}
	baseline := state.Snapshot()
	for _, test := range []struct {
		name   string
		mutate func(*model.State)
	}{
		{"available", func(*model.State) {}},
		{"disabled grant", func(s *model.State) { g := s.Grants["grant"]; g.Enabled = false; s.Grants["grant"] = g }},
		{"unpublished grant", func(s *model.State) { g := s.Grants["grant"]; g.Published = false; s.Grants["grant"] = g }},
		{"disabled user", func(s *model.State) { u := s.Users["u"]; u.Enabled = false; s.Users["u"] = u }},
		{"disabled route", func(s *model.State) { r := s.Attachments["r"]; r.Enabled = false; s.Attachments["r"] = r }},
		{"disabled HY2", func(s *model.State) { g := s.Gateways["g"]; g.HY2Disabled = true; s.Gateways["g"] = g }},
		{"unready HY2", func(s *model.State) { n := s.NodeStatus["g"]; n.HY2Ready = false; s.NodeStatus["g"] = n }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := state.Update(func(s *model.State) error {
				// Copy the fixture maps to avoid carrying mutations between cases.
				s.Users["u"], s.Gateways["g"], s.Attachments["r"] = baseline.Users["u"], baseline.Gateways["g"], baseline.Attachments["r"]
				s.Grants["grant"], s.NodeStatus["g"] = baseline.Grants["grant"], baseline.NodeStatus["g"]
				test.mutate(s)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/admin/grants", nil)
			request.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.SignSession(server.cfg.adminSessionKey(), "admin", server.now().Add(time.Hour))})
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			available := test.name == "available"
			if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "?format=hysteria2") != available {
				t.Fatalf("fragment disagrees with access state: %d %s", response.Code, response.Body.String())
			}
			if dashboardRequest(t, server).Grants[0].HY2Available != available {
				t.Fatal("live dashboard disagrees with downloadable access")
			}
			if test.name == "disabled user" && !strings.Contains(response.Body.String(), "用户已停用") {
				t.Fatal("disabled user is misleadingly shown as awaiting publication")
			}
		})
	}
}

func TestControllerWarnsOnlyForHTTPPublicURL(t *testing.T) {
	server, _ := testServer(t, nil)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.SignSession(server.cfg.adminSessionKey(), "admin", server.now().Add(time.Hour))})
	for _, test := range []struct {
		publicURL string
		warning   bool
	}{{"http://panel.example", true}, {"https://panel.example", false}} {
		server.cfg.PublicURL = test.publicURL
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "id=\"transport-warning\"") != test.warning {
			t.Fatalf("public URL %q warning=%t, response=%d", test.publicURL, test.warning, response.Code)
		}
		login := httptest.NewRecorder()
		server.Handler().ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/login", nil))
		if login.Code != http.StatusOK || strings.Contains(login.Body.String(), "class=\"warning\"") != test.warning {
			t.Fatalf("public URL %q login warning=%t, response=%d", test.publicURL, test.warning, login.Code)
		}
	}
}
