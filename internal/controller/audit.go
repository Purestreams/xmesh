package controller

import (
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xmesh/internal/audit"
	"xmesh/internal/auth"
	"xmesh/internal/model"
)

//go:embed audit.html
var auditHTML string

//go:embed audit_panel.html
var auditPanelSection string

var auditTemplate = template.Must(template.New("user-audit").Funcs(template.FuncMap{"auditResult": func(value string) string {
	labels := map[string]string{"attempt": "尝试连接", "connected": "连接成功", "closed": "连接关闭", "failed": "失败", "denied": "策略拒绝", "resolved": "解析成功"}
	if label, ok := labels[value]; ok {
		return label
	}
	return value
}}).Parse(auditHTML))

func (s *Server) bumpUserAudit(state *model.State, id string) {
	seen := map[string]bool{}
	for _, grant := range state.Grants {
		if grant.UserID != id {
			continue
		}
		route := state.Attachments[grant.AttachmentID]
		if route.AgentID != "" && !seen[route.AgentID] {
			bumpAgent(state, route.AgentID)
			seen[route.AgentID] = true
		}
	}
}

func (s *Server) saveUserAudit(w http.ResponseWriter, r *http.Request) {
	enabled := r.FormValue("enabled") == "true"
	limit, err := parsePositive(r.FormValue("limit_mb"), audit.DefaultLimitMB)
	if err != nil || limit > 1024 {
		http.Error(w, "audit limit must be 1–1024 MB", 400)
		return
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	id := r.PathValue("id")
	err = s.store.Update(func(state *model.State) error {
		user, ok := state.Users[id]
		if !ok {
			return errors.New("user not found")
		}
		if user.AuditEnabled != enabled {
			user.AuditGeneration++
		}
		user.AuditEnabled, user.AuditLimitMB = enabled, limit
		state.Users[id] = user
		s.bumpUserAudit(state, id)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err = s.audits.Enforce(id, audit.LimitBytes(limit)); err != nil {
		http.Error(w, "settings saved but audit quota could not be applied", 500)
		return
	}
	http.Redirect(w, r, "/admin/users/"+id+"/audit", http.StatusSeeOther)
}

func (s *Server) clearUserAudit(w http.ResponseWriter, r *http.Request) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	id := r.PathValue("id")
	// Invalidate queued events first: a slow node must not resurrect cleared logs.
	err := s.store.Update(func(state *model.State) error {
		user, ok := state.Users[id]
		if !ok {
			return errors.New("user not found")
		}
		user.AuditGeneration++
		state.Users[id] = user
		s.bumpUserAudit(state, id)
		return nil
	})
	if err == nil {
		err = s.audits.Clear(id)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/admin/users/"+id+"/audit", http.StatusSeeOther)
}

func auditFilter(r *http.Request) (audit.Filter, error) {
	filter := audit.Filter{Host: strings.TrimSpace(r.URL.Query().Get("host")), Result: r.URL.Query().Get("result"), Limit: 200}
	if len(filter.Host) > 255 {
		return filter, errors.New("filter too long")
	}
	if value := r.URL.Query().Get("port"); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return filter, errors.New("invalid port")
		}
		filter.Port = port
	}
	switch filter.Result {
	case "", "attempt", "connected", "failed", "denied", "closed", "resolved":
	default:
		return filter, errors.New("invalid result")
	}
	return filter, nil
}

func (s *Server) auditView(w http.ResponseWriter, r *http.Request) (model.User, audit.View, bool) {
	user, ok := s.store.Snapshot().Users[r.PathValue("id")]
	if !ok {
		http.NotFound(w, r)
		return user, audit.View{}, false
	}
	filter, err := auditFilter(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return user, audit.View{}, false
	}
	view, err := s.audits.Read(user.ID, audit.LimitBytes(user.AuditLimitMB), filter, s.now())
	if err != nil {
		http.Error(w, "cannot read audit logs", 500)
		return user, view, false
	}
	return user, view, true
}

func (s *Server) userAuditData(w http.ResponseWriter, r *http.Request) {
	user, view, ok := s.auditView(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, struct {
		UserID  string     `json:"user_id"`
		Enabled bool       `json:"enabled"`
		View    audit.View `json:"view"`
	}{user.ID, user.AuditEnabled, view})
}

func (s *Server) userAuditPage(w http.ResponseWriter, r *http.Request) {
	user, view, ok := s.auditView(w, r)
	if !ok {
		return
	}
	state := s.store.Snapshot()
	external := false
	for _, grant := range state.Grants {
		if grant.UserID == user.ID && grant.Enabled && state.Attachments[grant.AttachmentID].UpstreamID != "" {
			external = true
		}
	}
	cookie, _ := r.Cookie(sessionCookie)
	data := struct {
		User               model.User
		View               audit.View
		CSRF               string
		CSS                template.CSS
		External           bool
		Host, Port, Result string
		LimitMB            int
	}{user, view, auth.Derive(s.cfg.sessionKey(), "csrf", cookie.Value), template.CSS(panelCSS), external, r.URL.Query().Get("host"), r.URL.Query().Get("port"), r.URL.Query().Get("result"), user.AuditLimitMB}
	if data.LimitMB <= 0 {
		data.LimitMB = audit.DefaultLimitMB
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := auditTemplate.Execute(w, data); err != nil {
		s.logger.Error("render audit page", "error", err)
	}
}

func (s *Server) downloadUserAudit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.store.Snapshot().Users[id]; !ok {
		http.NotFound(w, r)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"audit-%s.jsonl.gz\"", id))
	if err := s.audits.Export(id, w); err != nil {
		s.logger.Warn("export audit logs", "user", id, "error", err)
	}
}

func validAuditEvent(e audit.Event) bool {
	if e.At.IsZero() || len(e.Host) == 0 || len(e.Host) > 255 || e.Port < 1 || e.Port > 65535 || len(e.ConnectionID) == 0 || len(e.ConnectionID) > 128 || len(e.Reason) > 512 || len(e.IPs) > 32 || e.DurationMS < 0 || e.ConnectMS < 0 {
		return false
	}
	if e.IP != "" && net.ParseIP(e.IP) == nil {
		return false
	}
	for _, ip := range e.IPs {
		if net.ParseIP(ip) == nil {
			return false
		}
	}
	if e.Kind == "dns" {
		return e.Result == "resolved" || e.Result == "failed"
	}
	if e.Kind != "tcp" {
		return false
	}
	switch e.Result {
	case "attempt", "connected", "closed", "failed", "denied":
		return true
	}
	return false
}

func auditLabel(value string) string {
	if len(value) > 256 {
		return strings.ToValidUTF8(value[:256], "")
	}
	return value
}

// User identity is derived from the authenticated node's persisted Grant, never
// from an event's supplied user/exit metadata. Validate the whole batch first.
func (s *Server) receiveAudit(w http.ResponseWriter, r *http.Request) {
	role, nodeID, ok := s.authenticateNode(r)
	if !ok || role != model.RoleAgent {
		http.Error(w, "unauthorized audit reporter", 403)
		return
	}
	var batch audit.Batch
	if !decodeJSON(w, r, &batch) {
		return
	}
	if len(batch.Events) > audit.MaxBatch || len(batch.Dropped) > audit.MaxBatch {
		http.Error(w, "audit batch too large", 400)
		return
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	state := s.store.Snapshot()
	if !state.Agents[nodeID].Enabled {
		http.Error(w, "agent disabled", 403)
		return
	}
	resolve := func(grantID string, generation uint64) (model.User, model.Attachment, bool, bool) {
		grant, known := state.Grants[grantID]
		route := state.Attachments[grant.AttachmentID]
		user := state.Users[grant.UserID]
		if !known || route.AgentID != nodeID {
			return user, route, false, false
		}
		active := grant.Enabled && routeAvailable(&state, route) && user.Enabled && user.AuditEnabled && user.AuditGeneration == generation
		return user, route, true, active
	}
	grouped := map[string][]audit.Event{}
	drops := map[string]uint64{}
	users := map[string]model.User{}
	for _, e := range batch.Events {
		if !validAuditEvent(e) {
			http.Error(w, "invalid audit event", 400)
			return
		}
		user, route, owned, active := resolve(e.GrantID, e.Generation)
		link := state.Links[e.LinkID]
		if !owned || link.AttachmentID != route.ID || link.ID == "" || (e.UserID != "" && e.UserID != user.ID) {
			http.Error(w, "audit event outside node grants", 403)
			return
		}
		if !active {
			continue
		}
		e.UserID, e.UserName, e.AgentID, e.GatewayID, e.ExitName = user.ID, auditLabel(user.Name), nodeID, route.GatewayID, auditLabel(state.Agents[nodeID].Name)
		grouped[user.ID] = append(grouped[user.ID], e)
		users[user.ID] = user
	}
	for _, d := range batch.Dropped {
		user, _, owned, active := resolve(d.GrantID, d.Generation)
		if !owned {
			http.Error(w, "audit drop outside node grants", 403)
			return
		}
		if active {
			drops[user.ID] += d.Count
			users[user.ID] = user
		}
	}
	for id, user := range users {
		if err := s.audits.Append(id, audit.LimitBytes(user.AuditLimitMB), grouped[id], drops[id], s.now()); err != nil {
			s.logger.Error("write audit log", "user", id, "error", err)
			http.Error(w, "audit storage unavailable", 503)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
