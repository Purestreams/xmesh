package controller

import (
	"net/http"
	"strings"

	"github.com/Purestreams/xmesh/internal/auth"
)

// Refresh access tables and subscription links without replacing drafts.
func (s *Server) grantFragment(w http.ResponseWriter, r *http.Request) {
	state := s.panelState()
	cookie, _ := r.Cookie(sessionCookie)
	data := panelData{State: state, Now: s.now(), CSRF: auth.Derive(s.cfg.sessionKey(), "csrf", cookie.Value),
		UserList: sortedUsers(state), GrantList: sortedGrants(state), GrantSummary: summarizeGrants(state),
		PublicURL: strings.TrimSuffix(s.cfg.PublicURL, "/"), SubscriptionCounts: subscriptionCounts(state, s.now())}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	for _, name := range []string{"grants", "subscription-links", "user-table"} {
		if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
			s.logger.Error("render access fragment", "template", name, "error", err)
			return
		}
	}
}

const grantPanel = `{{template "grants" .}}`

const grantTemplate = `{{define "grants"}}
<section id="grants" data-page="users" data-revision="{{.Revision}}">
<h2 data-i18n>用户线路授权</h2>
<p class="muted" data-i18n>按用户管理已开通的线路。停用只撤销所选授权，Gateway 应用配置后停止该用户的访问；其他用户仍可使用。</p>
<div class="grant-filters selection-tools">
<label><span data-i18n>用户</span><select id="grant-user"><option value="" data-i18n>所有用户</option>{{range .UserList}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></label>
<label><span data-i18n>线路名称</span><input id="grant-search" type="search" placeholder="搜索用户、Gateway、出口或 Link 名称" data-i18n-placeholder></label>
<label><span data-i18n>授权状态</span><select id="grant-state"><option value="" data-i18n>全部</option><option value="true" data-i18n>启用</option><option value="false" data-i18n>停用</option></select></label>
<button type="button" data-grant-select data-i18n>全选筛选结果</button><button type="button" data-grant-clear data-i18n>清除选择</button>
</div>
<form id="grant-bulk" method="post" action="/admin/grants/state" class="grant-bulk">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<label><span data-i18n>批量操作</span><select name="enabled" required><option value="false" data-i18n>停用所选授权</option><option value="true" data-i18n>启用所选授权</option></select></label>
<button type="submit" data-i18n>应用到所选授权</button><span id="grant-selection-summary" role="status" data-i18n>请选择要管理的授权</span>
</form>
<table id="grant-table"><thead><tr><th data-i18n>选择 / 详情</th><th data-i18n>用户</th><th data-i18n>线路</th><th data-i18n>状态</th><th data-i18n>流量 / 连接</th><th data-i18n>操作</th></tr></thead><tbody>
{{range .GrantList}}{{$st := index $.GrantSummary .ID}}{{$route := index $.Attachments .AttachmentID}}{{$gw := index $.Gateways $route.GatewayID}}{{$hy2st := index $.NodeStatus $gw.ID}}{{$user := index $.Users .UserID}}{{$access := grantAccess $.State . $.Now}}
<tr data-confirmation="{{$user.Name}} / {{routeName $.State $route}}" data-grant-id="{{.ID}}" data-user-id="{{.UserID}}" data-enabled="{{.Enabled}}">
<td><label><input type="checkbox" name="grant_id" value="{{.ID}}" form="grant-bulk"><span class="sr-only">{{$user.Name}} / {{routeName $.State $route}}</span></label><details><summary data-i18n>技术详情</summary><small>ID <code>{{.ID}}</code><br>UUID {{.VMessUUID}}<br><span data-i18n>线路组合 ID</span> <code>{{.AttachmentID}}</code></small></details></td>
<td class="grant-user-name">{{$user.Name}}</td>
<td class="grant-route-name"><strong>{{routeName $.State $route}}</strong><small>{{$gw.Name}} / {{if $route.UpstreamID}}{{(index $.State.Upstreams $route.UpstreamID).Name}}{{else}}{{(index $.State.Agents $route.AgentID).Name}}{{end}}</small>{{range routeLinks $.State .AttachmentID}}<small>Link: {{.}}</small>{{end}}{{if $route.UpstreamID}}<small data-i18n>外部出口</small>{{end}}
{{if grantHY2Available $.State . $.Now}}<br><a href="/subscription/{{$user.SubscriptionToken}}?format=hysteria2&amp;grant={{.ID}}" data-i18n>下载 HY2 严格验证客户端包</a>{{if and $gw.IPv6IngressHost $hy2st.IPv6IngressReady (gatewayIPv6Supported $hy2st.BinaryVersion)}}<br><a href="/subscription/{{$user.SubscriptionToken}}?format=hysteria2&amp;grant={{.ID}}&amp;family=6" data-i18n>下载 IPv6 HY2 严格验证客户端包</a>{{end}}{{end}}</td>
<td>{{if .Enabled}}<span data-i18n>启用</span>{{else}}<span data-i18n>停用</span>{{end}}{{if not $user.Enabled}}<small data-i18n>用户已停用</small>{{end}}<small data-i18n>{{$access.Reason}}</small><a href="#{{$access.Target}}" data-i18n>查看下一步</a></td>
<td>↑ {{$st.UploadBytes}} ↓ {{$st.DownloadBytes}}<br>TCP {{$st.TCPConnections}}, UDP {{$st.UDPAssociations}}</td>
<td><form class="inline grant-state-form" method="post" action="/admin/grants/{{.ID}}/state"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="enabled" value="{{if .Enabled}}false{{else}}true{{end}}"><button class="secondary">{{if .Enabled}}<span data-i18n>停用此授权</span>{{else}}<span data-i18n>启用此授权</span>{{end}}</button></form><details><summary data-i18n>删除</summary><form method="post" action="/admin/grants/{{.ID}}/delete"><input type="hidden" name="csrf" value="{{$.CSRF}}"><label><span data-i18n>输入用户与线路名称以确认删除</span><input name="confirm_name" placeholder="{{$user.Name}} / {{routeName $.State $route}}" required autocomplete="off"></label><button class="danger" data-i18n>删除授权</button></form></details></td>
</tr>{{else}}<tr><td colspan="6" data-i18n>暂无授权</td></tr>{{end}}
</tbody></table>
<p id="grant-empty" hidden data-i18n>没有匹配的授权，请调整筛选条件。</p>
<p class="next-steps"><a href="#subscribe" data-subscribe-selected-user data-i18n>为用户开通更多线路</a></p>
</section>
{{end}}`

const subscriptionLinksTemplate = `{{define "subscription-links"}}
<div id="subscription-links" data-revision="{{.Revision}}">
{{range .UserList}}<div class="subscription-row" data-user-id="{{.ID}}" data-user-name="{{.Name}}" data-archived="{{.Archived}}"><a href="#grants" data-manage-user="{{.ID}}" data-i18n>管理线路</a><span class="publication">{{.Name}}（<span data-i18n>{{index $.SubscriptionCounts .ID}}</span>）</span><button type="button" data-copy="{{$.PublicURL}}/subscription/{{.SubscriptionToken}}" data-i18n>复制订阅链接</button><details><summary data-i18n>查看完整链接</summary><input aria-label="{{.Name}} 的订阅链接" data-i18n-aria-label value="{{$.PublicURL}}/subscription/{{.SubscriptionToken}}" readonly></details></div>{{end}}
</div>
{{end}}`
