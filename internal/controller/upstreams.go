package controller

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"xmesh/internal/model"
)

func upstreamAvailable(state *model.State, attachment model.Attachment) bool {
	if attachment.UpstreamID == "" {
		return false
	}
	upstream, ok := state.Upstreams[attachment.UpstreamID]
	return ok && upstream.Enabled && upstream.Endpoint.UUID != ""
}

func routeAvailable(state *model.State, attachment model.Attachment) bool {
	if !attachment.Enabled || !state.Gateways[attachment.GatewayID].Enabled {
		return false
	}
	if attachment.UpstreamID != "" {
		return state.NodeStatus[attachment.GatewayID].ExternalUpstreams && upstreamAvailable(state, attachment)
	}
	return state.Agents[attachment.AgentID].Enabled && attachmentHasEnabledLink(state, attachment.ID)
}

func sortedUpstreams(state model.State) []model.VMessUpstream {
	result := make([]model.VMessUpstream, 0, len(state.Upstreams))
	for _, upstream := range state.Upstreams {
		result = append(result, upstream)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func bumpUpstreamRoutes(state *model.State, upstreamID string) {
	for _, attachment := range state.Attachments {
		if attachment.UpstreamID != upstreamID {
			continue
		}
		bumpGateway(state, attachment.GatewayID)
		for id, grant := range state.Grants {
			if grant.AttachmentID == attachment.ID {
				grant.Published = false
				state.Grants[id] = grant
			}
		}
	}
}

func (s *Server) createUpstream(w http.ResponseWriter, r *http.Request) {
	name, source := strings.TrimSpace(r.FormValue("name")), normalizeExternalLink(r.FormValue("source"))
	if name == "" || source == "" {
		http.Error(w, "name and VMess or VLESS link or subscription URL are required", 400)
		return
	}
	id, err := newID("up")
	if err != nil {
		http.Error(w, "could not create upstream", 500)
		return
	}
	upstream := model.VMessUpstream{ID: id, Name: name, Enabled: true, CreatedAt: s.now().UTC()}
	if strings.HasPrefix(source, "vmess://") || strings.HasPrefix(source, "vless://") {
		upstream.Endpoint, err = parseExternalURI(source)
	} else {
		upstream.SubscriptionURL = source
		var nodes []model.VMessEndpoint
		nodes, err = fetchVMessSubscription(r.Context(), source, s.cfg.AllowPrivateUpstreamSources)
		if err == nil {
			if len(nodes) == 0 {
				http.Error(w, "subscription has no supported VMess or VLESS node", 400)
				return
			}
			upstream.Candidates, upstream.LastRefresh = subscriptionCandidates(nodes), s.now().UTC()
			if len(nodes) == 1 {
				upstream.SelectedKey, upstream.Endpoint = upstreamKey(nodes[0]), nodes[0]
			}
		}
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.store.Update(func(state *model.State) error { state.Upstreams[id] = upstream; return nil }); err != nil {
		http.Error(w, "could not save upstream", 500)
		return
	}
	http.Redirect(w, r, "/#upstreams", http.StatusSeeOther)
}

func (s *Server) editUpstream(w http.ResponseWriter, r *http.Request) {
	id, name, source := r.PathValue("id"), strings.TrimSpace(r.FormValue("name")), normalizeExternalLink(r.FormValue("source"))
	if name == "" {
		http.Error(w, "name is required", 400)
		return
	}
	current, ok := s.store.Snapshot().Upstreams[id]
	if !ok {
		http.Error(w, "upstream not found", 404)
		return
	}
	updated := current
	updated.Name = name
	replaceSource := source != "" && source != current.SubscriptionURL
	if replaceSource {
		updated.SubscriptionURL, updated.SelectedKey = "", ""
		updated.Endpoint = model.VMessEndpoint{}
		updated.Candidates = nil
		updated.LastRefresh, updated.LastError = time.Time{}, ""
		if strings.HasPrefix(source, "vmess://") || strings.HasPrefix(source, "vless://") {
			endpoint, err := parseExternalURI(source)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			updated.Endpoint = endpoint
		} else {
			nodes, err := fetchVMessSubscription(r.Context(), source, s.cfg.AllowPrivateUpstreamSources)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if len(nodes) == 0 {
				http.Error(w, "subscription has no supported VMess or VLESS node", 400)
				return
			}
			updated.SubscriptionURL = source
			updated.Candidates, updated.LastRefresh = subscriptionCandidates(nodes), s.now().UTC()
			if len(nodes) == 1 {
				updated.SelectedKey, updated.Endpoint = upstreamKey(nodes[0]), nodes[0]
			}
		}
	}
	err := s.store.Update(func(state *model.State) error {
		existing, ok := state.Upstreams[id]
		if !ok || (replaceSource && (existing.SubscriptionURL != current.SubscriptionURL || existing.Endpoint != current.Endpoint || existing.SelectedKey != current.SelectedKey)) {
			return errors.New("upstream changed during edit")
		}
		next := existing
		next.Name = name
		if replaceSource {
			next.SubscriptionURL, next.SelectedKey, next.Endpoint = updated.SubscriptionURL, updated.SelectedKey, updated.Endpoint
			next.Candidates, next.LastRefresh, next.LastError = updated.Candidates, updated.LastRefresh, updated.LastError
		}
		state.Upstreams[id] = next
		if !sameVMessConnection(existing.Endpoint, next.Endpoint) {
			bumpUpstreamRoutes(state, id)
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	http.Redirect(w, r, "/#upstreams", http.StatusSeeOther)
}

func (s *Server) selectUpstream(w http.ResponseWriter, r *http.Request) {
	id, key := r.PathValue("id"), strings.TrimSpace(r.FormValue("selected_key"))
	current, ok := s.store.Snapshot().Upstreams[id]
	if !ok || current.SubscriptionURL == "" {
		http.Error(w, "subscription not found", 404)
		return
	}
	nodes, err := fetchVMessSubscription(r.Context(), current.SubscriptionURL, s.cfg.AllowPrivateUpstreamSources)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	endpoint, err := selectedEndpoint(nodes, key)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		upstream, ok := state.Upstreams[id]
		if !ok || upstream.SubscriptionURL != current.SubscriptionURL {
			return errors.New("subscription changed during selection")
		}
		changed := !sameVMessConnection(upstream.Endpoint, endpoint)
		upstream.SelectedKey, upstream.Endpoint = upstreamKey(endpoint), endpoint
		upstream.Candidates, upstream.LastRefresh, upstream.LastError = subscriptionCandidates(nodes), s.now().UTC(), ""
		state.Upstreams[id] = upstream
		if changed {
			bumpUpstreamRoutes(state, id)
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	http.Redirect(w, r, "/#upstreams", http.StatusSeeOther)
}

func (s *Server) refreshUpstream(ctx context.Context, id string) error {
	current, ok := s.store.Snapshot().Upstreams[id]
	if !ok || current.SubscriptionURL == "" {
		return errors.New("subscription not found")
	}
	nodes, fetchErr := fetchVMessSubscription(ctx, current.SubscriptionURL, s.cfg.AllowPrivateUpstreamSources)
	var refreshErr error
	updateErr := s.store.Update(func(state *model.State) error {
		upstream, ok := state.Upstreams[id]
		if !ok || upstream.SubscriptionURL != current.SubscriptionURL {
			return errors.New("subscription changed during refresh")
		}
		if fetchErr != nil {
			upstream.LastError = fetchErr.Error()
			state.Upstreams[id] = upstream
			refreshErr = fetchErr
			return nil
		}
		upstream.Candidates, upstream.LastRefresh = subscriptionCandidates(nodes), s.now().UTC()
		upstream.LastError = ""
		if upstream.SelectedKey != "" {
			selectedKey := upstream.SelectedKey
			if !strings.HasPrefix(selectedKey, "v3-") && upstream.Endpoint.UUID != "" {
				selectedKey = upstreamKey(upstream.Endpoint)
			}
			endpoint, err := selectedEndpoint(nodes, selectedKey)
			if err != nil && selectedKey != upstream.SelectedKey {
				endpoint, err = selectedEndpoint(nodes, upstream.SelectedKey)
			}
			if err != nil {
				upstream.LastError = err.Error()
				refreshErr = err
				endpoint = model.VMessEndpoint{}
			} else {
				upstream.SelectedKey = upstreamKey(endpoint)
			}
			if !sameVMessConnection(upstream.Endpoint, endpoint) {
				bumpUpstreamRoutes(state, id)
			}
			upstream.Endpoint = endpoint
		} else if len(nodes) == 1 {
			upstream.SelectedKey, upstream.Endpoint = upstreamKey(nodes[0]), nodes[0]
			bumpUpstreamRoutes(state, id)
		}
		state.Upstreams[id] = upstream
		return nil
	})
	if updateErr != nil {
		return updateErr
	}
	return refreshErr
}

func (s *Server) refreshUpstreamHTTP(w http.ResponseWriter, r *http.Request) {
	if err := s.refreshUpstream(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/#upstreams", http.StatusSeeOther)
}

func (s *Server) RefreshUpstreamSubscriptions(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		for id, upstream := range s.store.Snapshot().Upstreams {
			if upstream.SubscriptionURL == "" {
				continue
			}
			if err := s.refreshUpstream(ctx, id); err != nil {
				s.logger.Warn("refresh VMess subscription", "upstream", id, "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) toggleUpstream(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		upstream, ok := state.Upstreams[r.PathValue("id")]
		if !ok {
			return errors.New("upstream not found")
		}
		upstream.Enabled = !upstream.Enabled
		state.Upstreams[upstream.ID] = upstream
		bumpUpstreamRoutes(state, upstream.ID)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	http.Redirect(w, r, "/#upstreams", http.StatusSeeOther)
}

func (s *Server) deleteUpstream(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(state *model.State) error {
		upstream, ok := state.Upstreams[r.PathValue("id")]
		if !ok {
			return errors.New("upstream not found")
		}
		if r.FormValue("confirm_name") != upstream.Name {
			return errors.New("confirmation name does not match")
		}
		for id, attachment := range state.Attachments {
			if attachment.UpstreamID == upstream.ID {
				removeAttachment(state, id)
			}
		}
		delete(state.Upstreams, upstream.ID)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/#upstreams", http.StatusSeeOther)
}

func (s *Server) attachUpstream(w http.ResponseWriter, r *http.Request) {
	gatewayID, upstreamID := r.FormValue("gateway_id"), r.FormValue("upstream_id")
	id, err := newID("node")
	if err != nil {
		http.Error(w, "could not create route", 500)
		return
	}
	err = s.store.Update(func(state *model.State) error {
		if _, ok := state.Gateways[gatewayID]; !ok {
			return errors.New("gateway not found")
		}
		if _, ok := state.Upstreams[upstreamID]; !ok {
			return errors.New("upstream not found")
		}
		for _, route := range state.Attachments {
			if route.GatewayID == gatewayID && route.UpstreamID == upstreamID {
				return errors.New("route already exists")
			}
		}
		state.Attachments[id] = model.Attachment{ID: id, GatewayID: gatewayID, UpstreamID: upstreamID, Enabled: true, CreatedAt: s.now().UTC()}
		bumpGateway(state, gatewayID)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/#upstreams", http.StatusSeeOther)
}
