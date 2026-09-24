package controller

import "xmesh/internal/model"

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
<section id="upstreams" data-page="network"><h2>外部出口</h2><p class="muted">Gateway 接收用户连接后将流量发送到所选外部节点。粘贴单个 vmess://、vless:// 链接或订阅 URL；订阅包含多个节点时需明确选择。</p>
<form class="stack" method="post" action="/admin/upstreams"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>名称<input name="name" required></label><label>VMess / VLESS 链接或订阅 URL<input name="source" required autocomplete="off"></label><button>添加外部出口</button></form>
{{range .UpstreamList}}{{$selected := .SelectedKey}}<details><summary>{{.Name}} · {{if .Enabled}}启用{{else}}停用{{end}} · {{if .Endpoint.UUID}}{{.Endpoint.Name}}{{else}}待选择节点{{end}}</summary><p>ID <code>{{.ID}}</code>；来源 {{if .SubscriptionURL}}订阅{{else}}手动链接{{end}}；最近刷新 {{if .LastRefresh.IsZero}}从未刷新{{else}}<time datetime="{{.LastRefresh.Format "2006-01-02T15:04:05Z07:00"}}">{{.LastRefresh.Format "2006-01-02 15:04:05 MST"}}</time>{{end}}； <span class="error">{{.LastError}}</span></p>{{if .SubscriptionURL}}<form method="post" action="/admin/upstreams/{{.ID}}/select"><input type="hidden" name="csrf" value="{{$.CSRF}}"><label>已选节点<select name="selected_key" required>{{range .Candidates}}<option value="{{.Key}}" {{if eq .Key $selected}}selected{{end}}>{{.Name}}</option>{{end}}</select></label><button>选择节点</button></form><form method="post" action="/admin/upstreams/{{.ID}}/refresh"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button>刷新订阅</button></form>{{end}}<form method="post" action="/admin/upstreams/{{.ID}}/toggle"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button>{{if .Enabled}}停用{{else}}启用{{end}}</button></form><form method="post" action="/admin/upstreams/{{.ID}}/delete"><input type="hidden" name="csrf" value="{{$.CSRF}}"><label>输入出口名称以确认删除<input name="confirm_name" required autocomplete="off"></label><button class="danger">删除出口及关联线路</button></form></details>{{else}}<p>暂无外部出口。</p>{{end}}
{{range .UpstreamList}}<details><summary>编辑 {{.Name}}</summary><form class="stack" method="post" action="/admin/upstreams/{{.ID}}/edit"><input type="hidden" name="csrf" value="{{$.CSRF}}"><label>名称<input name="name" value="{{.Name}}" required></label><label>替换 VMess / VLESS 链接或订阅 URL<input name="source" placeholder="留空以保留当前来源" autocomplete="off"></label><button>保存外部出口</button></form></details>{{end}}
<h3>连接 Gateway</h3><form class="stack" method="post" action="/admin/upstream-attachments"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Gateway<select name="gateway_id" required>{{range .GatewayList}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></label><label>外部出口<select name="upstream_id" required>{{range .UpstreamList}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></label><button>创建线路</button></form>
{{range .ExternalAttachmentList}}{{$exit := index $.Upstreams .UpstreamID}}<div>{{(index $.Gateways .GatewayID).Name}} / {{$exit.Name}} · <code>{{.ID}}</code> · {{if .Enabled}}启用{{else}}停用{{end}} <form class="inline" method="post" action="/admin/attachments/{{.ID}}/toggle"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button>{{if .Enabled}}停用{{else}}启用{{end}}</button></form><details><summary>删除线路</summary><form method="post" action="/admin/attachments/{{.ID}}/delete"><input type="hidden" name="csrf" value="{{$.CSRF}}"><label>输入线路 ID 以确认删除<input name="confirm_id" required></label><button class="danger">删除线路</button></form></details></div>{{end}}
<p class="next-steps">线路创建后，前往 <a href="#subscribe">用户与订阅</a> 统一授权。</p></section>
`
