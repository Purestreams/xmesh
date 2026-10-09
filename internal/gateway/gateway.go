package gateway

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/xtaci/smux"

	"xmesh/internal/auth"
	"xmesh/internal/controller"
	"xmesh/internal/identity"
	"xmesh/internal/model"
	"xmesh/internal/nodeclient"
	"xmesh/internal/protocol"
	"xmesh/internal/runtimecfg"
	"xmesh/internal/scheduler"
)

type Runtime struct {
	local               runtimecfg.Config
	client              *nodeclient.Client
	logger              *slog.Logger
	pool                *scheduler.Pool
	mu                  sync.RWMutex
	config              controller.GatewayConfig
	authorizationUntil  time.Time
	authorizationEpoch  uint64
	activeConnections   map[net.Conn]authorizedConnection
	xrayCancel          context.CancelFunc
	xrayStatsConfig     controller.GatewayConfig
	xrayRunningPayload  string
	pendingStats        map[uint64]pendingStatsConfig
	statsMu             sync.Mutex
	started             time.Time
	tcpConnections      atomic.Int64
	udpAssociations     atomic.Int64
	lastError           atomic.Value
	xrayApply           chan struct{}
	xrayReady           atomic.Bool
	xrayError           atomic.Value
	xrayAppliedRevision atomic.Uint64
	grantMu             sync.Mutex
	grantStats          map[string]*grantCounters
	grantLinks          map[string]map[string]*grantCounters
	udpQueueDrops       atomic.Uint64
}

type grantCounters struct {
	upload   atomic.Uint64
	download atomic.Uint64
	tcp      atomic.Int64
	udp      atomic.Int64
}

type pendingStatsConfig struct {
	config    controller.GatewayConfig
	expiresAt time.Time
}

type authorizedConnection struct {
	grant  model.Grant
	cancel context.CancelFunc
}

const pendingStatsLifetime = 7 * 24 * time.Hour

func New(local runtimecfg.Config, logger *slog.Logger) *Runtime {
	return &Runtime{local: local, client: nodeclient.New(local.ControllerURL, local.Credential), logger: logger, pool: scheduler.New(), started: time.Now(), xrayApply: make(chan struct{}, 1), grantStats: map[string]*grantCounters{}, grantLinks: map[string]map[string]*grantCounters{}, pendingStats: map[uint64]pendingStatsConfig{}, authorizationUntil: time.Now().Add(nodeclient.ConfigLease), activeConnections: map[net.Conn]authorizedConnection{}}
}

func (r *Runtime) Run(ctx context.Context) error {
	if err := r.refresh(ctx); err != nil {
		return fmt.Errorf("initial controller config: %w", err)
	}
	go r.watchAuthorization(ctx)
	errCh := make(chan error, 4)
	go func() { errCh <- r.runTunnelServer(ctx) }()
	go func() { errCh <- r.runSOCKS(ctx) }()
	go func() { errCh <- r.controlLoop(ctx) }()
	go func() { errCh <- r.xrayLoop(ctx) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (r *Runtime) controlLoop(ctx context.Context) error {
	poll := time.NewTicker(r.local.PollInterval.Value(15 * time.Second))
	defer poll.Stop()
	status := time.NewTicker(r.local.StatusInterval.Value(10 * time.Second))
	defer status.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			if err := r.refresh(ctx); err != nil {
				r.setError("config: " + err.Error())
			}
		case <-status.C:
			if err := r.report(ctx); err != nil {
				r.setError("status: " + err.Error())
			}
		}
	}
}

func (r *Runtime) refresh(ctx context.Context) error {
	r.mu.RLock()
	epoch := r.authorizationEpoch
	r.mu.RUnlock()
	config, err := nodeclient.FetchGateway(ctx, r.client)
	if err != nil {
		if nodeclient.AuthorizationRejected(err) {
			r.invalidateAuthorization()
		}
		return err
	}
	if config.Gateway.ID != r.local.NodeID {
		return errors.New("controller returned another gateway identity")
	}
	r.mu.Lock()
	if epoch != r.authorizationEpoch {
		r.mu.Unlock()
		return errors.New("authorization changed while fetching configuration")
	}
	changed := r.config.Revision != config.Revision || r.config.Gateway.ID == "" || r.config.Gateway.Enabled != config.Gateway.Enabled
	previous := r.config
	r.mu.Unlock()
	reload := changed
	var nextPayload []byte
	// Capabilities reported to Controller can change the returned users/routes
	// without advancing DesiredVersion. Compare every accepted payload.
	if config.Gateway.Enabled {
		nextPayload, err = r.xrayPayload(config)
		if err != nil {
			return err
		}
		if previous.Gateway.ID != "" && previous.Gateway.Enabled {
			oldPayload, oldErr := r.xrayPayload(previous)
			reload = oldErr != nil || string(oldPayload) != string(nextPayload)
		}
	}
	if reload && config.Gateway.Enabled {
		if err := r.validateXrayCandidate(ctx, config); err != nil {
			return err
		}
	}
	if reload && config.Gateway.Enabled && r.xrayReady.Load() {
		if err := r.report(ctx); err != nil {
			if nodeclient.AuthorizationRejected(err) {
				return err
			}
			r.logger.Warn("report traffic before Xray update", "error", err)
		}
	}
	r.mu.Lock()
	if epoch != r.authorizationEpoch {
		r.mu.Unlock()
		return errors.New("authorization changed while applying configuration")
	}
	r.retireConnectionsLocked(config)
	for _, link := range config.Links {
		r.pool.UpdatePolicy(link.ID, link.AgentID, link.Priority, link.Weight, link.MaxStreams)
	}
	for _, session := range r.pool.Snapshot() {
		keep := false
		for _, link := range config.Links {
			if config.Gateway.Enabled && link.Enabled && link.ID == session.LinkID && link.AgentID == session.AgentID {
				for _, old := range r.config.Links {
					if old.ID == link.ID && old.TunnelToken == link.TunnelToken {
						keep = true
					}
				}
			}
		}
		if !keep && session.SMux != nil {
			_ = session.SMux.Close()
		}
	}
	r.config = config
	r.authorizationUntil = time.Now().Add(nodeclient.ConfigLease)
	if !config.Gateway.Enabled && r.xrayCancel != nil {
		r.xrayCancel()
		r.setXray(false, "gateway is disabled")
	}
	if !reload && r.xrayReady.Load() && r.xrayRunningPayload == string(nextPayload) {
		r.xrayStatsConfig = config
		r.xrayAppliedRevision.Store(config.Revision)
	}
	r.mu.Unlock()
	if reload {
		select {
		case r.xrayApply <- struct{}{}:
		default:
		}
	}
	r.setError("")
	return nil
}

func (r *Runtime) retireConnectionsLocked(config controller.GatewayConfig) {
	for conn, active := range r.activeConnections {
		previous := active.grant
		keep := false
		for _, grant := range config.Grants {
			if config.Gateway.Enabled && grant.Enabled && grant.ID == previous.ID && grant.AttachmentID == previous.AttachmentID && grant.SOCKSUsername == previous.SOCKSUsername && grant.SOCKSPassword == previous.SOCKSPassword && grant.VMessUUID == previous.VMessUUID {
				keep = true
				break
			}
		}
		if !keep {
			active.cancel()
			_ = conn.Close()
		}
	}
}

func (r *Runtime) invalidateAuthorizationLocked() {
	r.authorizationEpoch++
	r.config.Gateway.Enabled = false
	if r.xrayCancel != nil {
		r.xrayCancel()
	}
	r.setXray(false, "gateway authorization revoked")
	r.config.Grants, r.config.Links, r.config.Upstreams = nil, nil, nil
	r.retireConnectionsLocked(r.config)
	for _, session := range r.pool.Snapshot() {
		if session.SMux != nil {
			_ = session.SMux.Close()
		}
	}
	select {
	case r.xrayApply <- struct{}{}:
	default:
	}
}

func (r *Runtime) invalidateAuthorization() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidateAuthorizationLocked()
}

func (r *Runtime) watchAuthorization(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.mu.Lock()
			if r.config.Gateway.Enabled && !time.Now().Before(r.authorizationUntil) {
				r.invalidateAuthorizationLocked()
			}
			r.mu.Unlock()
		}
	}
}

func (r *Runtime) report(ctx context.Context) error {
	r.collectXrayStats(ctx)
	r.mu.RLock()
	config := r.config
	activeXrayConfig := r.xrayStatsConfig
	pending := make(map[uint64]controller.GatewayConfig, len(r.pendingStats))
	pendingExpiry := make(map[uint64]time.Time, len(r.pendingStats))
	expired := make([]uint64, 0)
	for revision, item := range r.pendingStats {
		if time.Now().After(item.expiresAt) {
			expired = append(expired, revision)
			continue
		}
		pending[revision] = item.config
		pendingExpiry[revision] = item.expiresAt
	}
	r.mu.RUnlock()
	if len(expired) > 0 {
		r.mu.Lock()
		for _, revision := range expired {
			if item, ok := r.pendingStats[revision]; ok && time.Now().After(item.expiresAt) {
				delete(r.pendingStats, revision)
			}
		}
		r.mu.Unlock()
	}
	links := map[string]model.LinkStatus{}
	for _, item := range config.Links {
		links[item.ID] = model.LinkStatus{LinkID: item.ID}
	}
	for _, item := range activeXrayConfig.Links {
		if _, exists := links[item.ID]; !exists {
			links[item.ID] = model.LinkStatus{LinkID: item.ID}
		}
	}
	for _, upstream := range config.Upstreams {
		links[upstream.AttachmentID] = model.LinkStatus{LinkID: upstream.AttachmentID, Online: r.xrayReady.Load(), Ready: r.xrayReady.Load()}
	}
	for _, upstream := range activeXrayConfig.Upstreams {
		if _, exists := links[upstream.AttachmentID]; !exists {
			links[upstream.AttachmentID] = model.LinkStatus{LinkID: upstream.AttachmentID, Online: r.xrayReady.Load(), Ready: r.xrayReady.Load()}
		}
	}
	for _, old := range pending {
		for _, item := range old.Links {
			if _, exists := links[item.ID]; !exists {
				links[item.ID] = model.LinkStatus{LinkID: item.ID}
			}
		}
		for _, upstream := range old.Upstreams {
			if _, exists := links[upstream.AttachmentID]; !exists {
				links[upstream.AttachmentID] = model.LinkStatus{LinkID: upstream.AttachmentID}
			}
		}
	}
	for _, session := range r.pool.Snapshot() {
		status, configured := links[session.LinkID]
		if !configured {
			continue
		}
		status.LinkID = session.LinkID
		status.Connections++
		status.ActiveStreams += session.ActiveStreams
		status.Online = status.Online || !session.SMux.IsClosed()
		status.Ready = status.Ready || session.Ready
		if session.Generation > status.Generation {
			status.Generation = session.Generation
		}
		if session.LastOK.After(status.LastSuccess) {
			status.LastSuccess = session.LastOK
		}
		if session.LastError != "" {
			status.LastError = session.LastError
		}
		if session.RTTMillis > 0 {
			status.RTTMillis = session.RTTMillis
		}
		status.ProbeTimeouts += session.ProbeTimeouts
		status.WriteBlockedMillis += session.WriteBlockedMillis
		status.WriteStalls += session.WriteStalls
		links[session.LinkID] = status
	}
	tunnelCount := 0
	for _, status := range links {
		tunnelCount += status.Connections
	}
	lastError, _ := r.lastError.Load().(string)
	xrayError, _ := r.xrayError.Load().(string)
	ready := lastError == "" && r.xrayReady.Load()
	grantList, linkUsage, upload, download := r.trafficSnapshot()
	activeGrants := map[string]bool{}
	for _, grant := range config.Grants {
		activeGrants[grant.ID] = true
	}
	for _, grant := range activeXrayConfig.Grants {
		activeGrants[grant.ID] = true
	}
	for _, old := range pending {
		for _, grant := range old.Grants {
			activeGrants[grant.ID] = true
		}
	}
	filteredGrants := grantList[:0]
	for _, grant := range grantList {
		if !activeGrants[grant.GrantID] {
			continue
		}
		filteredLinks := grant.Links[:0]
		for _, usage := range grant.Links {
			if _, active := links[usage.LinkID]; active {
				filteredLinks = append(filteredLinks, usage)
			}
		}
		grant.Links = filteredLinks
		filteredGrants = append(filteredGrants, grant)
	}
	grantList = filteredGrants
	for id, usage := range linkUsage {
		status, ok := links[id]
		if !ok {
			continue
		}
		status.UploadBytes, status.DownloadBytes = usage.UploadBytes, usage.DownloadBytes
		links[id] = status
	}
	linkList := make([]model.LinkStatus, 0, len(links))
	for _, status := range links {
		linkList = append(linkList, status)
	}
	nodeStatus := model.NodeStatus{NodeID: r.local.NodeID, Role: model.RoleGateway, BinaryVersion: r.local.BinaryVersion, InstanceID: r.local.InstanceID, Online: true, Ready: ready, DesiredVersion: config.Revision, AppliedVersion: r.xrayAppliedRevision.Load(), XrayReady: r.xrayReady.Load(), XrayError: xrayError, ExternalUpstreams: true, LastSeen: time.Now().UTC(), TunnelConnections: tunnelCount, TCPConnections: int(r.tcpConnections.Load()), UDPAssociations: int(r.udpAssociations.Load()), UploadBytes: upload, DownloadBytes: download, LastError: lastError, FailureCounters: map[string]uint64{"udp_queue_drops": r.udpQueueDrops.Load()}}
	if err := r.client.Report(ctx, nodeStatus, linkList, grantList); err != nil {
		if nodeclient.AuthorizationRejected(err) {
			r.invalidateAuthorization()
		}
		return err
	}
	r.mu.Lock()
	for revision := range pending {
		if item, ok := r.pendingStats[revision]; ok && item.expiresAt.Equal(pendingExpiry[revision]) {
			delete(r.pendingStats, revision)
		}
	}
	r.mu.Unlock()
	return nil
}

func (r *Runtime) grantCounters(id string) *grantCounters {
	r.grantMu.Lock()
	defer r.grantMu.Unlock()
	status := r.grantStats[id]
	if status == nil {
		status = &grantCounters{}
		r.grantStats[id] = status
	}
	return status
}

func (r *Runtime) grantLinkCounters(grantID, linkID string) *grantCounters {
	r.grantMu.Lock()
	defer r.grantMu.Unlock()
	links := r.grantLinks[grantID]
	if links == nil {
		links = map[string]*grantCounters{}
		r.grantLinks[grantID] = links
	}
	status := links[linkID]
	if status == nil {
		status = &grantCounters{}
		links[linkID] = status
	}
	return status
}

func (r *Runtime) trafficSnapshot() ([]model.GrantStatus, map[string]model.GrantLinkUsage, uint64, uint64) {
	r.grantMu.Lock()
	defer r.grantMu.Unlock()
	result := make([]model.GrantStatus, 0, len(r.grantStats))
	linkTotals := map[string]model.GrantLinkUsage{}
	var upload, download uint64
	for id, status := range r.grantStats {
		copy := model.GrantStatus{GrantID: id, UploadBytes: status.upload.Load(), DownloadBytes: status.download.Load(), TCPConnections: int(status.tcp.Load()), UDPAssociations: int(status.udp.Load())}
		for linkID, counters := range r.grantLinks[id] {
			usage := model.GrantLinkUsage{LinkID: linkID, UploadBytes: counters.upload.Load(), DownloadBytes: counters.download.Load()}
			copy.Links = append(copy.Links, usage)
			total := linkTotals[linkID]
			total.LinkID = linkID
			total.UploadBytes += usage.UploadBytes
			total.DownloadBytes += usage.DownloadBytes
			linkTotals[linkID] = total
		}
		result = append(result, copy)
		upload += copy.UploadBytes
		download += copy.DownloadBytes
	}
	return result, linkTotals, upload, download
}

func (r *Runtime) setError(value string) { r.lastError.Store(value) }

func (r *Runtime) runTunnelServer(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+r.local.Gateway.TunnelPath, r.acceptTunnel)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	server := &http.Server{Addr: r.local.Gateway.TunnelListen, Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	r.logger.Info("gateway tunnel listener started", "address", server.Addr, "path", r.local.Gateway.TunnelPath)
	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (r *Runtime) acceptTunnel(w http.ResponseWriter, request *http.Request) {
	ws, err := websocket.Accept(w, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	conn := websocket.NetConn(request.Context(), ws, websocket.MessageBinary)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	registration, err := protocol.ReadMessage(conn)
	if err != nil || registration.Type != protocol.TypeRegister || registration.Version != protocol.Version {
		_ = protocol.WriteMessage(conn, protocol.Message{Type: protocol.TypeResponse, Version: protocol.Version, ErrorCode: "invalid_registration", Error: "invalid tunnel registration"})
		return
	}
	link, ok := r.authorizeTunnel(registration)
	if !ok {
		_ = protocol.WriteMessage(conn, protocol.Message{Type: protocol.TypeResponse, Version: protocol.Version, ErrorCode: "unauthorized", Error: "invalid tunnel identity"})
		return
	}
	_ = conn.SetDeadline(time.Time{})
	if err := protocol.WriteMessage(conn, protocol.Message{Type: protocol.TypeReady, Version: protocol.Version, Success: true}); err != nil {
		return
	}
	smuxConfig, err := protocol.NewSMuxConfig()
	if err != nil {
		r.logger.Error("configure smux", "error", err)
		return
	}
	session, err := smux.Server(conn, smuxConfig)
	if err != nil {
		return
	}
	clientSessionID := registration.SessionID
	if clientSessionID == "" {
		clientSessionID, _ = identity.Token(12)
	}
	sessionID := registration.AgentID + "/" + registration.LinkID + "/" + clientSessionID
	entry := &scheduler.Session{ID: sessionID, AgentID: registration.AgentID, LinkID: registration.LinkID, Priority: link.Priority, Weight: link.Weight, MaxStreams: link.MaxStreams, SMux: session, Generation: registration.Generation, Ready: true, LastOK: time.Now()}
	r.mu.RLock()
	currentLink, ok := r.authorizeTunnelLocked(registration)
	if !ok {
		r.mu.RUnlock()
		_ = session.Close()
		return
	}
	entry.Priority, entry.Weight, entry.MaxStreams = currentLink.Priority, currentLink.Weight, currentLink.MaxStreams
	previous := r.pool.Add(entry)
	r.mu.RUnlock()
	if previous != nil && previous.SMux != nil {
		_ = previous.SMux.Close()
	}
	defer r.pool.Remove(sessionID, entry)
	defer session.Close()
	go r.probeSession(entry)
	r.logger.Info("tunnel registered", "agent", registration.AgentID, "link", registration.LinkID, "session", sessionID)
	// smux reports transport errors through AcceptStream before CloseChan.
	// Waiting only for CloseChan leaves dead slots in the pool until keepalive
	// expiry. Gateway initiates business/probe streams; reject unsolicited ones.
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return
		}
		_ = stream.Close()
	}
}

func (r *Runtime) probeSession(entry *scheduler.Session) {
	session := entry.SMux
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-session.CloseChan():
			return
		case <-ticker.C:
		}
		started := time.Now()
		stream, err := session.OpenStream()
		if err == nil {
			_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
			err = protocol.WriteMessage(stream, protocol.Message{Type: protocol.TypePing, Version: protocol.Version})
			if err == nil {
				var response protocol.Message
				response, err = protocol.ReadMessage(stream)
				if err == nil && response.Type != protocol.TypePong {
					err = errors.New("invalid probe response")
				}
			}
			_ = stream.Close()
		}
		r.pool.UpdateProbe(entry, time.Since(started), err)
	}
}

func (r *Runtime) authorizeTunnel(registration protocol.Message) (controller.GatewayLinkConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.authorizeTunnelLocked(registration)
}

func (r *Runtime) authorizeTunnelLocked(registration protocol.Message) (controller.GatewayLinkConfig, bool) {
	if !r.config.Gateway.Enabled || !time.Now().Before(r.authorizationUntil) {
		return controller.GatewayLinkConfig{}, false
	}
	for _, link := range r.config.Links {
		if link.Enabled && link.ID == registration.LinkID && link.AgentID == registration.AgentID && auth.EqualSecretHash(auth.SecretHash(link.TunnelToken), registration.Token) {
			return link, true
		}
	}
	return controller.GatewayLinkConfig{}, false
}

func (r *Runtime) grant(username, password string) (model.Grant, string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.config.Gateway.Enabled || !time.Now().Before(r.authorizationUntil) {
		return model.Grant{}, "", false
	}
	for _, grant := range r.config.Grants {
		userOK := subtle.ConstantTimeCompare([]byte(grant.SOCKSUsername), []byte(username)) == 1
		passwordOK := subtle.ConstantTimeCompare([]byte(grant.SOCKSPassword), []byte(password)) == 1
		if userOK && passwordOK && grant.Enabled {
			for _, attachment := range r.config.Links {
				if attachment.AttachmentID == grant.AttachmentID {
					return grant, attachment.AgentID, true
				}
			}
		}
	}
	return model.Grant{}, "", false
}

func (r *Runtime) openStream(agentID string, request protocol.Message) (net.Conn, *scheduler.Lease, error) {
	r.mu.RLock()
	if !r.config.Gateway.Enabled || !time.Now().Before(r.authorizationUntil) {
		r.mu.RUnlock()
		return nil, nil, errors.New("gateway authorization expired or disabled")
	}
	allowed := map[string]bool{}
	for _, grant := range r.config.Grants {
		if grant.ID != request.GrantID || !grant.Enabled {
			continue
		}
		for _, link := range r.config.Links {
			if link.AgentID == agentID && link.AttachmentID == grant.AttachmentID {
				allowed[link.ID] = true
			}
		}
		break
	}
	r.mu.RUnlock()
	lease, err := r.pool.AcquireLinks(agentID, allowed)
	if err != nil {
		return nil, nil, err
	}
	stream, err := lease.Session.SMux.OpenStream()
	if err != nil {
		lease.Release()
		r.pool.SetReady(lease.Session.ID, false, err.Error())
		return nil, nil, err
	}
	_ = stream.SetDeadline(time.Now().Add(20 * time.Second))
	if err := protocol.WriteMessage(stream, request); err != nil {
		stream.Close()
		lease.Release()
		return nil, nil, err
	}
	response, err := protocol.ReadMessage(stream)
	if err != nil {
		stream.Close()
		lease.Release()
		return nil, nil, err
	}
	if response.Type != protocol.TypeResponse || !response.Success {
		stream.Close()
		lease.Release()
		return nil, nil, fmt.Errorf("%s: %s", response.ErrorCode, response.Error)
	}
	_ = stream.SetDeadline(time.Time{})
	return stream, lease, nil
}

func (r *Runtime) linkAgentForGrant(grantID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, grant := range r.config.Grants {
		if grant.ID == grantID && grant.Enabled {
			for _, link := range r.config.Links {
				if link.AttachmentID == grant.AttachmentID {
					return link.AgentID, true
				}
			}
		}
	}
	return "", false
}

var _ = net.IPv4len
