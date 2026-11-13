package controller

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Purestreams/xmesh/internal/model"
)

type accessView struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	Target string `json:"target"`
}

func routeIssue(state model.State, route model.Attachment, at ...time.Time) accessView {
	gateway := state.Gateways[route.GatewayID]
	switch {
	case !route.Enabled:
		return accessView{"disabled", "线路已停用", "assignments"}
	case !gateway.Enabled:
		return accessView{"disabled", "入口已停用", "gateways"}
	case gateway.PublicHost == "":
		return accessView{"pending", "等待入口确认公网地址", "gateways"}
	case !gateway.VMessEnabled() && !gateway.HY2Enabled():
		return accessView{"disabled", "入口的接入协议均已关闭", "gateways"}
	}
	if route.UpstreamID != "" {
		if upstream, exists := state.Upstreams[route.UpstreamID]; exists && !upstream.Enabled {
			return accessView{"disabled", "外部出口已停用", "upstreams"}
		}
		if !upstreamAvailable(&state, route) {
			return accessView{"pending", "外部出口尚未选择节点", "upstreams"}
		}
	} else {
		if !agentEnabled(&state, state.Agents[route.AgentID]) {
			return accessView{"disabled", "出口已停用", "agents"}
		}
		linked := false
		for _, link := range state.Links {
			if link.AttachmentID == route.ID && link.Enabled {
				linked = true
				break
			}
		}
		if !linked {
			return accessView{"disabled", "没有启用的传输连接", "links"}
		}
	}
	status := state.NodeStatus[gateway.ID]
	if !status.Online {
		return accessView{"offline", "入口离线，检查服务与网络", "readiness"}
	}
	if status.ApplyError != "" || status.XrayError != "" {
		return accessView{"pending", "入口应用配置失败，查看节点错误", "gateways"}
	}
	if status.AppliedVersion < gateway.DesiredVersion || !status.Ready || !status.XrayReady {
		return accessView{"pending", "等待入口应用配置并启动接入服务", "readiness"}
	}
	if route.UpstreamID != "" {
		if !status.ExternalUpstreams {
			return accessView{"pending", "入口版本不支持外部出口，请升级节点", "node-upgrades"}
		}
		probe := currentUpstreamProbe(&state, route)
		now := time.Now()
		if len(at) > 0 {
			now = at[0]
		}
		if probe == nil || (!probe.CompletedAt.IsZero() && now.Sub(probe.CompletedAt) > upstreamProbeFreshness(status)) {
			return accessView{"configured", "外部出口已配置，等待入口测试连通性", "routes"}
		}
		if probe.CompletedAt.IsZero() {
			return accessView{"testing", "入口正在测试外部出口连通性", "routes"}
		}
		if !probe.Successful() {
			return accessView{"probe_failed", "外部出口连通性测试失败，查看测试结果", "routes"}
		}
	}
	if route.AgentID != "" {
		agent := state.Agents[route.AgentID]
		status := state.NodeStatus[agent.ID]
		if !status.Online {
			return accessView{"offline", "出口离线，检查服务与网络", "readiness"}
		}
		if status.ApplyError != "" || status.AppliedVersion < agent.DesiredVersion || !status.Ready {
			return accessView{"pending", "等待出口应用配置并就绪", "readiness"}
		}
		ready := false
		for _, link := range state.Links {
			g, a := state.LinkStatus[gateway.ID+"/"+link.ID], state.LinkStatus[agent.ID+"/"+link.ID]
			if link.AttachmentID == route.ID && link.Enabled && g.Online && g.Ready && a.Online && a.Ready {
				ready = true
				break
			}
		}
		if !ready {
			return accessView{"pending", "传输连接尚未就绪，检查两端连接错误", "links"}
		}
	}
	return accessView{"ready", "线路运行就绪", "routes"}
}

func grantAccess(state model.State, grant model.Grant, at ...time.Time) accessView {
	user := state.Users[grant.UserID]
	route := state.Attachments[grant.AttachmentID]
	if !grant.Enabled || !user.Enabled || user.Archived {
		gateway := state.Gateways[route.GatewayID]
		status := state.NodeStatus[gateway.ID]
		version := grant.AccessVersion
		if version == 0 {
			version = gateway.DesiredVersion
		}
		if status.AppliedVersion < version || status.ApplyError != "" || status.XrayError != "" {
			return accessView{"pending", "停用已提交，等待入口确认配置", "readiness"}
		}
		return accessView{"disabled", "停用配置已获入口确认", "grants"}
	}
	if issue := routeIssue(state, route, at...); issue.State != "ready" {
		return issue
	}
	if !grant.Published {
		return accessView{"pending", "等待入口确认授权后发布订阅", "readiness"}
	}
	now := time.Now()
	if len(at) > 0 {
		now = at[0]
	}
	if !grantImportable(state, grant, now) {
		return accessView{"pending", "等待接入协议、证书与用户凭据就绪", "readiness"}
	}
	return accessView{"ready", "已发布，可导入客户端", "subscribe"}
}

func stampUserAccess(state *model.State, userID string) {
	for id, grant := range state.Grants {
		if grant.UserID == userID {
			grant.AccessVersion = state.Gateways[state.Attachments[grant.AttachmentID].GatewayID].DesiredVersion
			state.Grants[id] = grant
		}
	}
}

func validDisplayName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= 120 && !strings.ContainsAny(name, "\r\n\x00")
}

func (s *Server) editUser(w http.ResponseWriter, r *http.Request) {
	name, notes := strings.TrimSpace(r.FormValue("name")), strings.TrimSpace(r.FormValue("notes"))
	if !validDisplayName(name) || utf8.RuneCountInString(notes) > 2000 {
		http.Error(w, "invalid name or notes", 400)
		return
	}
	err := s.store.Update(func(state *model.State) error {
		user, ok := state.Users[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("user not found")
		}
		user.Name, user.Notes = name, notes
		state.Users[user.ID] = user
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/#users", 303)
}

func (s *Server) archiveUser(w http.ResponseWriter, r *http.Request) {
	value := r.FormValue("archived")
	if value != "true" && value != "false" {
		http.Error(w, "invalid archive state", 400)
		return
	}
	err := s.store.Update(func(state *model.State) error {
		user, ok := state.Users[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("user not found")
		}
		if user.Archived == (value == "true") {
			return nil
		}
		user.Archived = value == "true"
		user.Enabled = false
		state.Users[user.ID] = user
		gateways := map[string]bool{}
		for id, grant := range state.Grants {
			if grant.UserID == user.ID {
				grant.Published = false
				state.Grants[id] = grant
				gateways[state.Attachments[grant.AttachmentID].GatewayID] = true
			}
		}
		for id := range gateways {
			bumpGateway(state, id)
		}
		stampUserAccess(state, user.ID)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/#users", 303)
}

func (s *Server) editRouteName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name != "" && !validDisplayName(name) {
		http.Error(w, "invalid route name", 400)
		return
	}
	err := s.store.Update(func(state *model.State) error {
		route, ok := state.Attachments[r.PathValue("id")]
		if !ok {
			return fmt.Errorf("assignment not found")
		}
		route.Name = name
		state.Attachments[route.ID] = route
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/#routes", 303)
}
