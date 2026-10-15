package controller

import "github.com/Purestreams/xmesh/internal/model"

func splitPanelAttachments(state model.State) (agent, external []model.Attachment) {
	for _, attachment := range sortedAttachments(state) {
		if attachment.UpstreamID != "" {
			external = append(external, attachment)
		} else {
			agent = append(agent, attachment)
		}
	}
	return
}

const upstreamPanelSection = `
<section id="upstreams" data-page="network"><h2 data-i18n>外部出口</h2><p class="muted" data-i18n>Gateway 接收用户连接后将流量发送到所选外部节点。粘贴单个 vmess://、vless:// 链接或订阅 URL；订阅包含多个节点时需明确选择。</p>
<form class="stack" method="post" action="/admin/upstreams"><input type="hidden" name="csrf" value="{{.CSRF}}"><label><span data-i18n>名称</span><input name="name" required></label><label><span data-i18n>VMess / VLESS 链接或订阅 URL</span><input name="source" required autocomplete="off"></label><button data-i18n>添加外部出口</button></form>
{{range .UpstreamList}}{{$selected := .SelectedKey}}<details><summary>{{.Name}} · {{if .Enabled}}<span data-i18n>启用</span>{{else}}<span data-i18n>停用</span>{{end}} · {{if .Endpoint.UUID}}{{.Endpoint.Name}}{{else}}<span data-i18n>待选择节点</span>{{end}}</summary><p>ID <code>{{.ID}}</code><span data-i18n>；来源</span> {{if .SubscriptionURL}}<span data-i18n>订阅</span>{{else}}<span data-i18n>手动链接</span>{{end}}<span data-i18n>；最近刷新</span> {{if .LastRefresh.IsZero}}<span data-i18n>从未刷新</span>{{else}}<time datetime="{{.LastRefresh.Format "2006-01-02T15:04:05Z07:00"}}">{{.LastRefresh.Format "2006-01-02 15:04:05 MST"}}</time>{{end}}； <span class="error">{{.LastError}}</span></p>{{if .SubscriptionURL}}<form method="post" action="/admin/upstreams/{{.ID}}/select"><input type="hidden" name="csrf" value="{{$.CSRF}}"><label><span data-i18n>已选节点</span><select name="selected_key" required>{{range .Candidates}}<option value="{{.Key}}" {{if eq .Key $selected}}selected{{end}}>{{.Name}}</option>{{end}}</select></label><button data-i18n>选择节点</button></form><form method="post" action="/admin/upstreams/{{.ID}}/refresh"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button data-i18n>刷新订阅</button></form>{{end}}<form method="post" action="/admin/upstreams/{{.ID}}/toggle"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button>{{if .Enabled}}<span data-i18n="action.disable">停用</span>{{else}}<span data-i18n="action.enable">启用</span>{{end}}</button></form><form method="post" action="/admin/upstreams/{{.ID}}/delete"><input type="hidden" name="csrf" value="{{$.CSRF}}"><label><span data-i18n>输入出口名称以确认删除</span><input name="confirm_name" required autocomplete="off"></label><button class="danger" data-i18n>删除出口及关联线路</button></form></details>{{else}}<p data-i18n>暂无外部出口。</p>{{end}}
{{range .UpstreamList}}<details><summary><span data-i18n>编辑</span> {{.Name}}</summary><form class="stack" method="post" action="/admin/upstreams/{{.ID}}/edit"><input type="hidden" name="csrf" value="{{$.CSRF}}"><label><span data-i18n>名称</span><input name="name" value="{{.Name}}" required></label><label><span data-i18n>替换 VMess / VLESS 链接或订阅 URL</span><input name="source" placeholder="留空以保留当前来源" data-i18n-placeholder="留空以保留当前来源" autocomplete="off"></label><button data-i18n>保存外部出口</button></form></details>{{end}}
<h3 data-i18n>连接 Gateway</h3><form class="stack" method="post" action="/admin/upstream-attachments"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Gateway<select name="gateway_id" required>{{range .GatewayList}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></label><label><span data-i18n>外部出口</span><select name="upstream_id" required>{{range .UpstreamList}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></label><button data-i18n>创建线路</button></form>
{{range .ExternalAttachmentList}}{{$exit := index $.Upstreams .UpstreamID}}<div>{{(index $.Gateways .GatewayID).Name}} / {{$exit.Name}} · <code>{{.ID}}</code> · {{if .Enabled}}<span data-i18n>启用</span>{{else}}<span data-i18n>停用</span>{{end}} <form class="inline" method="post" action="/admin/attachments/{{.ID}}/toggle"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button>{{if .Enabled}}<span data-i18n="action.disable">停用</span>{{else}}<span data-i18n="action.enable">启用</span>{{end}}</button></form><details><summary data-i18n>删除线路</summary><form method="post" action="/admin/attachments/{{.ID}}/delete"><input type="hidden" name="csrf" value="{{$.CSRF}}"><label><span data-i18n>输入线路 ID 以确认删除</span><input name="confirm_id" required></label><button class="danger" data-i18n>删除线路</button></form></details></div>{{end}}
{{if .ExternalAttachmentList}}<h3 data-i18n>出口连通性测试</h3>{{end}}
{{range .ExternalAttachmentList}}<article class="route-card"><strong>{{(index $.Gateways .GatewayID).Name}} / {{(index $.Upstreams .UpstreamID).Name}}</strong><div data-upstream-probe="{{.ID}}"><p class="muted" data-i18n>配置应用后将自动测试 ip.sb 和 microsoft.com；也可手动测试。</p></div><form class="inline" method="post" action="/admin/attachments/{{.ID}}/probe"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button data-upstream-probe-button="{{.ID}}" data-i18n>测试连接</button></form></article>{{end}}
<p class="next-steps"><span data-i18n>线路创建后，前往</span> <a href="#subscribe" data-i18n>用户与订阅</a> <span data-i18n>统一授权。</span></p></section>
`
