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
	started             time.Time
	tcpConnections      atomic.Int64
	udpAssociations     atomic.Int64
	lastError           atomic.Value
	xrayApply           chan struct{}
	xrayReady           atomic.Bool
	xrayError           atomic.Value
	xrayAppliedRevision atomic.Uint64
	grantMu             sync.Mutex
	grantStats          map[string]*model.GrantStatus
}

func New(local runtimecfg.Config, logger *slog.Logger) *Runtime {
	return &Runtime{local: local, client: nodeclient.New(local.ControllerURL, local.Credential), logger: logger, pool: scheduler.New(), started: time.Now(), xrayApply: make(chan struct{}, 1), grantStats: map[string]*model.GrantStatus{}}
}

func (r *Runtime) Run(ctx context.Context) error {
	if err := r.refresh(ctx); err != nil {
		return fmt.Errorf("initial controller config: %w", err)
	}
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
	config, err := nodeclient.FetchGateway(ctx, r.client)
	if err != nil {
		return err
	}
	if config.Gateway.ID != r.local.NodeID {
		return errors.New("controller returned another gateway identity")
	}
	r.mu.Lock()
	changed := r.config.Revision != config.Revision || r.config.Gateway.ID == ""
	r.config = config
	r.mu.Unlock()
	if changed {
		select {
		case r.xrayApply <- struct{}{}:
		default:
		}
	}
	r.setError("")
	return nil
}

func (r *Runtime) report(ctx context.Context) error {
	r.mu.RLock()
	config := r.config
	r.mu.RUnlock()
	links := map[string]model.LinkStatus{}
	for _, item := range config.Links {
		links[item.ID] = model.LinkStatus{LinkID: item.ID}
	}
	for _, session := range r.pool.Snapshot() {
		status := links[session.LinkID]
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
		links[session.LinkID] = status
	}
	linkList := make([]model.LinkStatus, 0, len(links))
	tunnelCount := 0
	for _, status := range links {
		tunnelCount += status.Connections
		linkList = append(linkList, status)
	}
	lastError, _ := r.lastError.Load().(string)
	xrayError, _ := r.xrayError.Load().(string)
	ready := lastError == "" && r.xrayReady.Load()
	grantList, upload, download := r.grantSnapshot()
	nodeStatus := model.NodeStatus{NodeID: r.local.NodeID, Role: model.RoleGateway, Online: true, Ready: ready, DesiredVersion: config.Revision, AppliedVersion: r.xrayAppliedRevision.Load(), XrayReady: r.xrayReady.Load(), XrayError: xrayError, LastSeen: time.Now().UTC(), TunnelConnections: tunnelCount, TCPConnections: int(r.tcpConnections.Load()), UDPAssociations: int(r.udpAssociations.Load()), UploadBytes: upload, DownloadBytes: download, LastError: lastError}
	return r.client.Report(ctx, nodeStatus, linkList, grantList)
}

func (r *Runtime) updateGrant(id string, fn func(*model.GrantStatus)) {
	r.grantMu.Lock()
	defer r.grantMu.Unlock()
	status := r.grantStats[id]
	if status == nil {
		status = &model.GrantStatus{GrantID: id}
		r.grantStats[id] = status
	}
	fn(status)
}

func (r *Runtime) grantSnapshot() ([]model.GrantStatus, uint64, uint64) {
	r.grantMu.Lock()
	defer r.grantMu.Unlock()
	result := make([]model.GrantStatus, 0, len(r.grantStats))
	var upload, download uint64
	for _, status := range r.grantStats {
		copy := *status
		result = append(result, copy)
		upload += copy.UploadBytes
		download += copy.DownloadBytes
	}
	return result, upload, download
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
	smuxConfig := smux.DefaultConfig()
	smuxConfig.Version = protocol.SMuxVersion
	smuxConfig.KeepAliveInterval = 10 * time.Second
	smuxConfig.KeepAliveTimeout = 35 * time.Second
	session, err := smux.Server(conn, smuxConfig)
	if err != nil {
		return
	}
	sessionID := registration.SessionID
	if sessionID == "" {
		sessionID, _ = identity.Token(12)
	}
	entry := &scheduler.Session{ID: sessionID, AgentID: registration.AgentID, LinkID: registration.LinkID, Priority: link.Priority, Weight: link.Weight, MaxStreams: link.MaxStreams, SMux: session, Generation: registration.Generation, Ready: true, LastOK: time.Now()}
	r.pool.Add(entry)
	defer r.pool.Remove(sessionID)
	defer session.Close()
	go r.probeSession(sessionID, session)
	r.logger.Info("tunnel registered", "agent", registration.AgentID, "link", registration.LinkID, "session", sessionID)
	<-session.CloseChan()
}

func (r *Runtime) probeSession(id string, session *smux.Session) {
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
		r.pool.UpdateProbe(id, time.Since(started), err)
	}
}

func (r *Runtime) authorizeTunnel(registration protocol.Message) (controller.GatewayLinkConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, link := range r.config.Links {
		if link.ID == registration.LinkID && link.AgentID == registration.AgentID && auth.EqualSecretHash(auth.SecretHash(link.TunnelToken), registration.Token) {
			return link, true
		}
	}
	return controller.GatewayLinkConfig{}, false
}

func (r *Runtime) grant(username, password string) (model.Grant, string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
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
	lease, err := r.pool.Acquire(agentID)
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
