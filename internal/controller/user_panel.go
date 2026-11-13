package controller

const userTableTemplate = `{{define "user-table"}}
<table id="user-table"><thead><tr><th data-i18n>用户</th><th data-i18n>状态</th><th data-i18n>操作</th></tr></thead><tbody>
{{range .UserList}}<tr data-user-id="{{.ID}}" data-enabled="{{.Enabled}}" data-archived="{{.Archived}}">
<td><strong>{{.Name}}</strong>{{if .Notes}}<small>{{.Notes}}</small>{{end}}<details><summary data-i18n>技术详情</summary><code>{{.ID}}</code></details></td>
<td>{{if .Archived}}<span data-i18n>已归档</span>{{else if .Enabled}}<span data-i18n>启用</span>{{else}}<span data-i18n>停用</span>{{end}}</td>
<td><a href="#grants" data-manage-user="{{.ID}}" data-i18n>管理线路</a><br><form class="inline user-state-form" method="post" action="/admin/users/{{.ID}}/state"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="enabled" value="{{if .Enabled}}false{{else}}true{{end}}"><button class="secondary" {{if .Archived}}disabled{{end}}>{{if .Enabled}}<span data-i18n="action.disable">停用</span>{{else}}<span data-i18n="action.enable">启用</span>{{end}}</button></form>
<details class="user-profile"><summary data-i18n>资料与归档</summary><form method="post" action="/admin/users/{{.ID}}/edit"><input type="hidden" name="csrf" value="{{$.CSRF}}"><label><span data-i18n>名称</span><input name="name" value="{{.Name}}" maxlength="120" required></label><label><span data-i18n>备注</span><textarea name="notes" maxlength="2000">{{.Notes}}</textarea></label><button data-i18n>保存用户资料</button></form><p data-i18n>归档会停用用户并保留线路。恢复归档后，需手动启用用户。</p><form method="post" action="/admin/users/{{.ID}}/archive"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="archived" value="{{if .Archived}}false{{else}}true{{end}}"><button class="secondary">{{if .Archived}}<span data-i18n>恢复归档</span>{{else}}<span data-i18n>归档用户</span>{{end}}</button></form></details>
<details><summary data-i18n>重置订阅与连接凭据</summary><p data-i18n>旧链接立即失效，旧代理凭据会在 Gateway 应用配置后撤销。待新订阅发布后，请让客户端重新导入。</p><p data-i18n>此操作影响该用户的所有线路。重置后复制新链接，在客户端更新或重新导入订阅。</p><form method="post" action="/admin/users/{{.ID}}/reset-subscription"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button class="danger" data-i18n>确认重置</button></form></details></td>
</tr>{{else}}<tr><td colspan="3" data-i18n>暂无用户</td></tr>{{end}}
</tbody></table>
{{end}}`
