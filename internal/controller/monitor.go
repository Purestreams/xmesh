package controller

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"xmesh/internal/auth"
	"xmesh/internal/model"
)

//go:embed monitor.html
var monitorHTML string

//go:embed monitor.js
var monitorJS string

//go:embed monitor.css
var monitorCSS string

//go:embed monitor_admin.html
var monitorAdminHTML string

var monitorAdminTemplate = template.Must(template.New("monitor-settings").Parse(monitorAdminHTML))

const monitorCacheTTL = 10 * time.Second

const monitorPanelSection = `<section id="public-monitor" data-page="maintenance"><h2>公开监控</h2><p class="muted">配置访客可见的节点与链路别名，提供免登录的只读状态和延迟页面。</p><p><a href="/admin/monitor">设置公开范围与别名</a> · <a href="/monitor" target="_blank" rel="noopener noreferrer">打开 Monitor ↗</a></p></section>`

type monitorRuntime struct {
	mu       sync.Mutex
	revision uint64
	at       time.Time
	expires  time.Time
	json     []byte // Only the allowlisted public projection is cached.
	budget   float64
	budgetAt time.Time
}

// Public DTOs deliberately contain no model status/config objects. IDs are
// response-local references, never database, node, route or instance identities.
type monitorNode struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Role  string `json:"role"`
	State string `json:"state"`
}

type monitorSample struct {
	At        time.Time `json:"at"`
	RTTMillis *float64  `json:"rtt_ms"`
}

type monitorLink struct {
	Name      string          `json:"name"`
	Gateway   string          `json:"gateway"`
	Exit      string          `json:"exit"`
	State     string          `json:"state"`
	RTTMillis *float64        `json:"rtt_ms"`
	Measured  *time.Time      `json:"measured_at"`
	History   []monitorSample `json:"history"`
}

type monitorPayload struct {
	At    time.Time     `json:"at"`
	Nodes []monitorNode `json:"nodes"`
	Links []monitorLink `json:"links"`
}

func (s *Server) monitorPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	enabled := false
	s.store.View(func(state *model.State) { enabled = state.Monitor.Enabled })
	if !enabled {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(monitorHTML))
}

func monitorScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(monitorJS))
}

func monitorStyles(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(monitorCSS))
}

func (s *Server) monitorData(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	s.monitor.mu.Lock()
	defer s.monitor.mu.Unlock()
	now := s.now().UTC()
	var payload []byte
	enabled, limited, encodingFailed := false, false, false
	s.store.View(func(state *model.State) {
		enabled = state.Monitor.Enabled
		if !enabled {
			return
		}
		// Global token bucket: 100 reads/s, burst 200. It is bounded and does
		// not trust proxy headers or keep an unbounded map of visitor IPs.
		if s.monitor.budgetAt.IsZero() {
			s.monitor.budget = 200
		} else {
			s.monitor.budget = math.Min(200, s.monitor.budget+math.Max(0, now.Sub(s.monitor.budgetAt).Seconds())*100)
		}
		s.monitor.budgetAt = now
		if s.monitor.budget < 1 {
			limited = true
			return
		}
		s.monitor.budget--
		if s.monitor.json == nil || s.monitor.revision != state.Revision || !now.Before(s.monitor.expires) || now.Before(s.monitor.at) {
			// Rebuild on every state revision: hiding/renaming/deleting an
			// object takes effect immediately, even within the cache window.
			data := s.publicMonitor(state, now)
			encoded, err := json.Marshal(data)
			if err != nil {
				encodingFailed = true
				s.monitor.json = nil
				s.logger.Error("encode public monitor", "error", err)
				return
			}
			s.monitor.json = encoded
			s.monitor.revision, s.monitor.at = state.Revision, now
			s.monitor.expires = s.monitorCacheExpiry(state, now)
		}
		payload = s.monitor.json
	})
	if !enabled {
		http.NotFound(w, r)
		return
	}
	if limited {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	if encodingFailed {
		http.Error(w, "monitor temporarily unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

func validMonitorAlias(alias string) bool {
	if alias == "" || utf8.RuneCountInString(alias) > 48 {
		return false
	}
	for _, ch := range alias {
		// Deliberately excludes address/URL punctuation, controls and HTML.
		if !unicode.IsLetter(ch) && !unicode.IsNumber(ch) && !strings.ContainsRune(" -_()（）", ch) {
			return false
		}
	}
	return strings.TrimSpace(alias) != ""
}

func publicAliasKeys(aliases map[string]string) []string {
	return monitorAliasKeys(aliases, false)
}

func monitorAliasKeys(aliases map[string]string, optional bool) []string {
	keys := make([]string, 0, len(aliases))
	for key, alias := range aliases {
		if validMonitorAlias(alias) || (optional && alias == "") {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if aliases[keys[i]] == aliases[keys[j]] {
			return keys[i] < keys[j]
		}
		return aliases[keys[i]] < aliases[keys[j]]
	})
	return keys
}

func (s *Server) monitorFresh(at, now time.Time) bool {
	return !at.IsZero() && !at.After(now) && now.Sub(at) <= time.Duration(s.cfg.NodeOfflineAfterSeconds)*time.Second
}

// A cached healthy value must not survive its heartbeat/probe expiry, even if
// no new status report or persisted revision arrives during the 10s cache TTL.
func (s *Server) monitorCacheExpiry(state *model.State, now time.Time) time.Time {
	expires := now.Add(monitorCacheTTL)
	consider := func(at time.Time) {
		if s.monitorFresh(at, now) {
			deadline := at.Add(time.Duration(s.cfg.NodeOfflineAfterSeconds)*time.Second + time.Nanosecond)
			if deadline.Before(expires) {
				expires = deadline
			}
		}
	}
	for id := range state.Monitor.Nodes {
		consider(state.NodeStatus[id].LastSeen)
	}
	for id := range state.Monitor.Links {
		link := state.Links[id]
		route := state.Attachments[link.AttachmentID]
		for _, reporter := range []string{route.GatewayID, route.AgentID} {
			status := state.LinkStatus[reporter+"/"+id]
			consider(status.LastSeen)
			if reporter == route.GatewayID {
				consider(status.LastSuccess)
			}
		}
	}
	return expires
}

func (s *Server) monitorNodeState(enabled bool, desired uint64, status model.NodeStatus, gateway bool, now time.Time) string {
	switch {
	case !enabled:
		return "disabled"
	case status.LastSeen.IsZero():
		return "unknown"
	case !s.monitorFresh(status.LastSeen, now):
		return "stale"
	case !status.Online:
		return "offline"
	case !status.Ready || status.AppliedVersion < desired || status.ApplyError != "" || (gateway && (!status.XrayReady || status.XrayError != "")):
		return "pending"
	default:
		return "ready"
	}
}

func monitorRTT(value float64) *float64 {
	if value <= 0 || value > math.MaxFloat64/10 || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	value = math.Round(value*10) / 10
	if value == 0 {
		value = 0.1
	}
	return &value
}

func (s *Server) publicMonitor(state *model.State, now time.Time) monitorPayload {
	data := monitorPayload{At: now, Nodes: []monitorNode{}, Links: []monitorLink{}}
	refs, stages := map[string]string{}, map[string]string{}
	for _, id := range publicAliasKeys(state.Monitor.Nodes) {
		var role, stage string
		if gateway, ok := state.Gateways[id]; ok {
			role, stage = "gateway", s.monitorNodeState(gateway.Enabled, gateway.DesiredVersion, state.NodeStatus[id], true, now)
		} else if agent, ok := state.Agents[id]; ok {
			role, stage = "agent", s.monitorNodeState(agent.Enabled, agent.DesiredVersion, state.NodeStatus[id], false, now)
		} else if upstream, ok := state.Upstreams[id]; ok {
			role, stage = "external", "unprobed"
			if !upstream.Enabled {
				stage = "disabled"
			}
		} else {
			continue
		}
		refs[id], stages[id] = fmt.Sprintf("n%d", len(data.Nodes)+1), stage
		data.Nodes = append(data.Nodes, monitorNode{Key: refs[id], Name: state.Monitor.Nodes[id], Role: role, State: stage})
	}
	linkKeys := monitorAliasKeys(state.Monitor.Links, true)
	// Count only selected, existing links with visible endpoints. Derive default
	// labels from public aliases; never copy a management Link name or URL.
	pairCounts, pairIndexes := map[string]int{}, map[string]int{}
	for _, id := range linkKeys {
		route, exitID, exists := monitorLinkRoute(state, id)
		if exists && refs[route.GatewayID] != "" && refs[exitID] != "" {
			pairCounts[refs[route.GatewayID]+"/"+refs[exitID]]++
		}
	}
	for _, id := range linkKeys {
		link := state.Links[id]
		external := strings.HasPrefix(id, "external:")
		route, exitID, exists := monitorLinkRoute(state, id)
		if !exists || refs[route.GatewayID] == "" || refs[exitID] == "" {
			continue
		}
		pair := refs[route.GatewayID] + "/" + refs[exitID]
		pairIndexes[pair]++
		name := state.Monitor.Links[id]
		if name == "" {
			name = state.Monitor.Nodes[route.GatewayID] + " → " + state.Monitor.Nodes[exitID]
			if pairCounts[pair] > 1 {
				name += fmt.Sprintf(" / 链路 %d", pairIndexes[pair])
			}
		}
		out := monitorLink{Name: name, Gateway: refs[route.GatewayID], Exit: refs[exitID], State: "pending", History: []monitorSample{}}
		gs, as := state.LinkStatus[route.GatewayID+"/"+id], state.LinkStatus[exitID+"/"+id]
		gatewayState, exitState := stages[route.GatewayID], stages[exitID]
		switch {
		case !route.Enabled || (!external && !link.Enabled) || gatewayState == "disabled" || exitState == "disabled":
			out.State = "disabled"
		case gatewayState == "stale" || exitState == "stale":
			out.State = "stale"
		case gatewayState == "offline" || exitState == "offline":
			out.State = "offline"
		case gatewayState == "unknown" || exitState == "unknown":
			out.State = "unknown"
		case gatewayState != "ready" || (!external && exitState != "ready"):
			out.State = "pending"
		case external:
			out.State = "unprobed"
		case gs.LastSeen.IsZero() || as.LastSeen.IsZero():
			out.State = "unknown"
		case !s.monitorFresh(gs.LastSeen, now) || !s.monitorFresh(as.LastSeen, now):
			out.State = "stale"
		case !gs.Online || !as.Online:
			out.State = "offline"
		case !gs.Ready || !as.Ready:
			out.State = "pending"
		case gs.LastSuccess.IsZero():
			out.State = "unknown"
		case !s.monitorFresh(gs.LastSuccess, now):
			out.State = "stale"
		default:
			out.State = "ready"
			out.RTTMillis = monitorRTT(gs.RTTMillis)
			if out.RTTMillis != nil {
				at := gs.LastSuccess.UTC()
				out.Measured = &at
			}
		}
		if !external {
			for _, sample := range state.LinkHistory[id] {
				if sample.At.Before(now.Add(-historyRetention)) || sample.At.After(now) {
					continue
				}
				point := monitorSample{At: sample.At.UTC()}
				if sample.Ready {
					point.RTTMillis = monitorRTT(sample.RTTMillis)
				}
				out.History = append(out.History, point)
			}
			if len(out.History) > historyLimit {
				out.History = out.History[len(out.History)-historyLimit:]
			}
		}
		data.Links = append(data.Links, out)
	}
	return data
}

func monitorLinkRoute(state *model.State, id string) (model.Attachment, string, bool) {
	if strings.HasPrefix(id, "external:") {
		route, exists := state.Attachments[strings.TrimPrefix(id, "external:")]
		return route, route.UpstreamID, exists && route.UpstreamID != ""
	}
	link, exists := state.Links[id]
	route, routeExists := state.Attachments[link.AttachmentID]
	return route, route.AgentID, exists && routeExists && route.UpstreamID == ""
}

type monitorSettingRow struct {
	ID, Name, Role, Alias string
	Visible               bool
}

type monitorSettingsView struct {
	CSRF    string
	Enabled bool
	Saved   bool
	Nodes   []monitorSettingRow
	Links   []monitorSettingRow
}

func monitorSettingRows(state model.State) ([]monitorSettingRow, []monitorSettingRow) {
	nodes, links := []monitorSettingRow{}, []monitorSettingRow{}
	for _, node := range sortedGateways(state) {
		nodes = append(nodes, monitorSettingRow{ID: node.ID, Name: node.Name, Role: "Gateway", Alias: state.Monitor.Nodes[node.ID], Visible: state.Monitor.Nodes[node.ID] != ""})
	}
	for _, node := range sortedAgents(state) {
		nodes = append(nodes, monitorSettingRow{ID: node.ID, Name: node.Name, Role: "Agent", Alias: state.Monitor.Nodes[node.ID], Visible: state.Monitor.Nodes[node.ID] != ""})
	}
	for _, node := range sortedUpstreams(state) {
		nodes = append(nodes, monitorSettingRow{ID: node.ID, Name: node.Name, Role: "外部出口", Alias: state.Monitor.Nodes[node.ID], Visible: state.Monitor.Nodes[node.ID] != ""})
	}
	for _, link := range sortedLinks(state) {
		route, ok := state.Attachments[link.AttachmentID]
		if !ok || route.UpstreamID != "" {
			continue
		}
		name := state.Gateways[route.GatewayID].Name + " → " + state.Agents[route.AgentID].Name + " / " + link.Name
		_, visible := state.Monitor.Links[link.ID]
		links = append(links, monitorSettingRow{ID: link.ID, Name: name, Alias: state.Monitor.Links[link.ID], Visible: visible})
	}
	for _, route := range sortedAttachments(state) {
		if route.UpstreamID != "" {
			id := "external:" + route.ID
			name := state.Gateways[route.GatewayID].Name + " → " + state.Upstreams[route.UpstreamID].Name
			_, visible := state.Monitor.Links[id]
			links = append(links, monitorSettingRow{ID: id, Name: name, Alias: state.Monitor.Links[id], Visible: visible})
		}
	}
	return nodes, links
}

func (s *Server) monitorSettingsPage(w http.ResponseWriter, r *http.Request) {
	state := s.store.Snapshot()
	nodes, links := monitorSettingRows(state)
	cookie, _ := r.Cookie(sessionCookie)
	data := monitorSettingsView{CSRF: auth.Derive(s.cfg.sessionKey(), "csrf", cookie.Value), Enabled: state.Monitor.Enabled, Saved: r.URL.Query().Get("saved") == "1", Nodes: nodes, Links: links}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := monitorAdminTemplate.Execute(w, data); err != nil {
		s.logger.Error("render monitor settings", "error", err)
	}
}

func (s *Server) monitorForm(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid monitor form", http.StatusBadRequest)
			return
		}
		next(w, r)
	}
}

func (s *Server) saveMonitorSettings(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		nodes, links := monitorSettingRows(*state)
		next := model.MonitorSettings{Enabled: r.PostFormValue("enabled") == "on", Nodes: map[string]string{}, Links: map[string]string{}}
		for _, group := range []struct {
			prefix string
			rows   []monitorSettingRow
			values map[string]string
		}{{"node_", nodes, next.Nodes}, {"link_", links, next.Links}} {
			seen := map[string]bool{}
			for _, row := range group.rows {
				if r.PostFormValue("show_"+group.prefix+row.ID) != "on" {
					continue
				}
				alias := strings.TrimSpace(r.PostFormValue(group.prefix + row.ID))
				if group.prefix == "link_" && alias == "" {
					group.values[row.ID] = ""
					continue
				}
				if !validMonitorAlias(alias) {
					return fmt.Errorf("公开别名须为 1–48 个文字、数字、空格、横线、下划线或括号；不能包含地址或 URL")
				}
				if seen[alias] {
					return fmt.Errorf("同类对象的公开别名不能重复")
				}
				seen[alias], group.values[row.ID] = true, alias
			}
		}
		// Do not publish a connection unless both endpoint aliases were chosen.
		for id := range next.Links {
			route := state.Attachments[state.Links[id].AttachmentID]
			exitID := route.AgentID
			if strings.HasPrefix(id, "external:") {
				route = state.Attachments[strings.TrimPrefix(id, "external:")]
				exitID = route.UpstreamID
			}
			if next.Nodes[route.GatewayID] == "" || next.Nodes[exitID] == "" {
				return fmt.Errorf("公开链路前，请在上方勾选它的入口和出口节点，并填写节点公开别名")
			}
		}
		state.Monitor = next
		return nil
	})
	if err != nil {
		// Store errors can contain filesystem paths. Only validation messages
		// are shown to the authenticated administrator.
		if strings.HasPrefix(err.Error(), "公开") || strings.HasPrefix(err.Error(), "同类") {
			http.Error(w, err.Error(), http.StatusBadRequest)
		} else {
			s.logger.Error("save monitor settings", "error", err)
			http.Error(w, "保存失败，请重试", http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/admin/monitor?saved=1", http.StatusSeeOther)
}
