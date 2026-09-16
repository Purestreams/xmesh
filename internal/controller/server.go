package controller

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"xmesh/internal/auth"
	"xmesh/internal/identity"
	"xmesh/internal/model"
	"xmesh/internal/store"
)

const sessionCookie = "xmesh_admin"

type Server struct {
	cfg               Config
	store             *store.Store
	logger            *slog.Logger
	templates         *template.Template
	now               func() time.Time
	releaseMu         sync.Mutex
	releaseHTTPClient *http.Client
}

func New(cfg Config, state *store.Store, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	t, err := template.New("panel").Funcs(template.FuncMap{
		"short": func(value string) string {
			if len(value) <= 8 {
				return value
			}
			return value[:8]
		},
		"status": func(applied, desired uint64, applyErr string) string {
			if applyErr != "" {
				return "failed: " + applyErr
			}
			if applied < desired {
				return "pending"
			}
			return "applied"
		},
	}).Parse(panelTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse panel template: %w", err)
	}
	return &Server{cfg: cfg, store: state, logger: logger, templates: t, now: time.Now}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.requireAdmin(s.logout))
	mux.HandleFunc("GET /", s.requireAdmin(s.panel))
	mux.HandleFunc("POST /admin/users", s.requireAdmin(s.csrf(s.createUser)))
	mux.HandleFunc("POST /admin/users/{id}/toggle", s.requireAdmin(s.csrf(s.toggleUser)))
	mux.HandleFunc("POST /admin/users/{id}/reset-subscription", s.requireAdmin(s.csrf(s.resetSubscription)))
	mux.HandleFunc("POST /admin/gateways", s.requireAdmin(s.csrf(s.createGateway)))
	mux.HandleFunc("POST /admin/quick-setup", s.requireAdmin(s.csrf(s.quickSetup)))
	mux.HandleFunc("POST /admin/gateways/{id}/toggle", s.requireAdmin(s.csrf(s.toggleGateway)))
	mux.HandleFunc("POST /admin/agents", s.requireAdmin(s.csrf(s.createAgent)))
	mux.HandleFunc("POST /admin/agents/{id}/toggle", s.requireAdmin(s.csrf(s.toggleAgent)))
	mux.HandleFunc("POST /admin/attachments", s.requireAdmin(s.csrf(s.createAttachment)))
	mux.HandleFunc("POST /admin/attachments/{id}/toggle", s.requireAdmin(s.csrf(s.toggleAttachment)))
	mux.HandleFunc("POST /admin/links", s.requireAdmin(s.csrf(s.createLink)))
	mux.HandleFunc("POST /admin/links/{id}/toggle", s.requireAdmin(s.csrf(s.toggleLink)))
	mux.HandleFunc("POST /admin/links/{id}/policy", s.requireAdmin(s.csrf(s.updateLinkPolicy)))
	mux.HandleFunc("POST /admin/grants", s.requireAdmin(s.csrf(s.createGrant)))
	mux.HandleFunc("POST /admin/grants/{id}/toggle", s.requireAdmin(s.csrf(s.toggleGrant)))
	mux.HandleFunc("POST /admin/enrollments", s.requireAdmin(s.csrf(s.createEnrollment)))
	mux.HandleFunc("POST /admin/enrollments/{id}/revoke", s.requireAdmin(s.csrf(s.revokeEnrollment)))
	mux.HandleFunc("GET /releases/{version}/{asset}", s.releaseAsset)
	mux.HandleFunc("HEAD /releases/{version}/{asset}", s.releaseAsset)
	mux.HandleFunc("GET /subscription/{token}", s.subscription)
	mux.HandleFunc("POST /api/v1/enroll", s.enroll)
	mux.HandleFunc("GET /api/v1/config", s.nodeConfig)
	mux.HandleFunc("POST /api/v1/status", s.nodeStatus)
	return s.securityHeaders(s.accessLog(mux))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderLogin(w, "")
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	usernameOK := subtle.ConstantTimeCompare([]byte(username), []byte(s.cfg.AdminUsername)) == 1
	if !usernameOK || !auth.CheckPassword(s.cfg.AdminPasswordHash, password) {
		time.Sleep(150 * time.Millisecond)
		s.renderLogin(w, "Invalid credentials")
		return
	}
	expires := s.now().Add(12 * time.Hour)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: auth.SignSession(s.cfg.sessionKey(), username, expires),
		Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: strings.HasPrefix(s.cfg.PublicURL, "https://"), Expires: expires,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdmin(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (s *Server) isAdmin(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	user, ok := auth.VerifySession(s.cfg.sessionKey(), cookie.Value, s.now())
	return ok && user == s.cfg.AdminUsername
}

func (s *Server) csrf(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || r.FormValue("csrf") != auth.Derive(s.cfg.sessionKey(), "csrf", cookie.Value) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (s *Server) panel(w http.ResponseWriter, r *http.Request) {
	state := s.store.Snapshot()
	for id, status := range state.NodeStatus {
		if s.now().Sub(status.LastSeen) > time.Duration(s.cfg.NodeOfflineAfterSeconds)*time.Second {
			status.Online = false
			status.Ready = false
			state.NodeStatus[id] = status
		}
	}
	for id, status := range state.LinkStatus {
		if s.now().Sub(status.LastSeen) > time.Duration(s.cfg.NodeOfflineAfterSeconds)*time.Second {
			status.Online = false
			status.Ready = false
			state.LinkStatus[id] = status
		}
	}
	cookie, _ := r.Cookie(sessionCookie)
	data := panelData{
		State: state, CSRF: auth.Derive(s.cfg.sessionKey(), "csrf", cookie.Value),
		PublicURL: strings.TrimSuffix(s.cfg.PublicURL, "/"),
		UserList:  sortedUsers(state), GatewayList: sortedGateways(state), AgentList: sortedAgents(state),
		AttachmentList: sortedAttachments(state), LinkList: sortedLinks(state), GrantList: sortedGrants(state),
		LinkSummary: summarizeLinks(state), LinkReports: sortedLinkReports(state), GrantSummary: summarizeGrants(state),
		Release:     s.releaseStatus(),
		Deployments: deploymentStatuses(state),
		Enrollments: enrollmentStatuses(state, s.now()),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "panel", data); err != nil {
		s.logger.Error("render panel", "error", err)
	}
}

func (s *Server) subscription(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	state := s.store.Snapshot()
	userID := ""
	for _, user := range state.Users {
		if subtle.ConstantTimeCompare([]byte(user.SubscriptionToken), []byte(token)) == 1 {
			userID = user.ID
			break
		}
	}
	if userID == "" {
		http.NotFound(w, r)
		return
	}
	payload, err := BuildSubscription(state, userID)
	if err != nil {
		http.Error(w, "subscription unavailable", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(payload))
}

func (s *Server) renderLogin(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>xmesh login</title><style>%s</style></head><body><main class="login"><h1>xmesh</h1><p class="error">%s</p><form method="post" action="/login"><label>Username<input name="username" autocomplete="username" required></label><label>Password<input type="password" name="password" autocomplete="current-password" required></label><button>Sign in</button></form></main></body></html>`, panelCSS, template.HTMLEscapeString(message))
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		next.ServeHTTP(w, r)
		s.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", s.now().Sub(start))
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func parsePositive(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, errors.New("must be a positive integer")
	}
	return n, nil
}

func validateURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "wss" && u.Scheme != "ws") || u.Host == "" {
		return errors.New("link URL must be an absolute ws:// or wss:// URL")
	}
	return nil
}

func newID(prefix string) (string, error) {
	id, err := identity.Token(9)
	if err != nil {
		return "", err
	}
	return prefix + "_" + id, nil
}

type panelData struct {
	model.State
	CSRF           string
	PublicURL      string
	UserList       []model.User
	GatewayList    []model.Gateway
	AgentList      []model.Agent
	AttachmentList []model.Attachment
	LinkList       []model.Link
	GrantList      []model.Grant
	LinkSummary    map[string]model.LinkStatus
	LinkReports    []model.LinkStatus
	GrantSummary   map[string]model.GrantStatus
	Release        releaseStatus
	Deployments    []deploymentStatus
	Enrollments    []enrollmentStatus
}

type enrollmentStatus struct {
	ID, NodeID, Role, State string
	ExpiresAt               time.Time
}

func enrollmentStatuses(state model.State, now time.Time) []enrollmentStatus {
	result := make([]enrollmentStatus, 0, len(state.Enrollments))
	for _, enrollment := range state.Enrollments {
		status := "active"
		if !enrollment.UsedAt.IsZero() {
			status = "used"
		} else if !now.Before(enrollment.ExpiresAt) {
			status = "expired"
		}
		result = append(result, enrollmentStatus{ID: enrollment.ID, NodeID: enrollment.NodeID, Role: string(enrollment.Role), State: status, ExpiresAt: enrollment.ExpiresAt})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ExpiresAt.After(result[j].ExpiresAt) })
	return result
}

type deploymentStatus struct {
	Name, Role, ID, Identity, Online, Applied, Runtime string
}

func deploymentStatuses(state model.State) []deploymentStatus {
	result := make([]deploymentStatus, 0, len(state.Gateways)+len(state.Agents))
	for _, gateway := range sortedGateways(state) {
		st := state.NodeStatus[gateway.ID]
		result = append(result, deploymentStatus{Name: gateway.Name, Role: "Gateway", ID: gateway.ID,
			Identity: yesNo(gateway.CredentialHash != ""), Online: yesNo(st.Online),
			Applied: configStage(st.AppliedVersion, gateway.DesiredVersion, st.ApplyError), Runtime: yesNo(st.Ready && st.XrayReady)})
	}
	for _, agent := range sortedAgents(state) {
		st := state.NodeStatus[agent.ID]
		result = append(result, deploymentStatus{Name: agent.Name, Role: "Agent", ID: agent.ID,
			Identity: yesNo(agent.CredentialHash != ""), Online: yesNo(st.Online),
			Applied: configStage(st.AppliedVersion, agent.DesiredVersion, st.ApplyError), Runtime: yesNo(st.Ready)})
	}
	return result
}

func yesNo(ok bool) string {
	if ok {
		return "ready"
	}
	return "pending"
}

func configStage(applied, desired uint64, applyError string) string {
	if applyError != "" {
		return "failed: " + applyError
	}
	return yesNo(applied >= desired && desired > 0)
}

func sortedUsers(s model.State) []model.User {
	result := make([]model.User, 0, len(s.Users))
	for _, v := range s.Users {
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func sortedGateways(s model.State) []model.Gateway {
	result := make([]model.Gateway, 0, len(s.Gateways))
	for _, v := range s.Gateways {
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func sortedAgents(s model.State) []model.Agent {
	result := make([]model.Agent, 0, len(s.Agents))
	for _, v := range s.Agents {
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func sortedAttachments(s model.State) []model.Attachment {
	result := make([]model.Attachment, 0, len(s.Attachments))
	for _, v := range s.Attachments {
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func sortedLinks(s model.State) []model.Link {
	result := make([]model.Link, 0, len(s.Links))
	for _, v := range s.Links {
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func sortedGrants(s model.State) []model.Grant {
	result := make([]model.Grant, 0, len(s.Grants))
	for _, v := range s.Grants {
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func summarizeLinks(s model.State) map[string]model.LinkStatus {
	result := map[string]model.LinkStatus{}
	for _, status := range s.LinkStatus {
		current, exists := result[status.LinkID]
		_, reporterIsGateway := s.Gateways[status.ReporterNodeID]
		_, currentIsGateway := s.Gateways[current.ReporterNodeID]
		if !exists || reporterIsGateway || !currentIsGateway {
			result[status.LinkID] = status
		}
	}
	return result
}

func sortedLinkReports(s model.State) []model.LinkStatus {
	result := make([]model.LinkStatus, 0, len(s.LinkStatus))
	for _, value := range s.LinkStatus {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].LinkID == result[j].LinkID {
			return result[i].ReporterNodeID < result[j].ReporterNodeID
		}
		return result[i].LinkID < result[j].LinkID
	})
	return result
}

func summarizeGrants(s model.State) map[string]model.GrantStatus {
	result := map[string]model.GrantStatus{}
	for _, status := range s.GrantStatus {
		result[status.GrantID] = status
	}
	return result
}
