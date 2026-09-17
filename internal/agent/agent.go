package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/xtaci/smux"

	"xmesh/internal/controller"
	"xmesh/internal/model"
	"xmesh/internal/nodeclient"
	"xmesh/internal/protocol"
	"xmesh/internal/relay"
	"xmesh/internal/runtimecfg"
)

type Runtime struct {
	local             runtimecfg.Config
	client            *nodeclient.Client
	logger            *slog.Logger
	mu                sync.RWMutex
	config            controller.AgentConfig
	configFingerprint string
	workers           map[string]context.CancelFunc
	statuses          map[string]*linkRuntimeStatus
	realityMu         sync.Mutex
	realities         map[string]*realityCore
	streamSlots       chan struct{}
	tcpConnections    atomic.Int64
	udpAssociations   atomic.Int64
	lastError         atomic.Value
}

type linkRuntimeStatus struct {
	mu sync.Mutex
	model.LinkStatus
}

func New(local runtimecfg.Config, logger *slog.Logger) *Runtime {
	limit := local.MaxActiveStreams
	if limit <= 0 {
		limit = 1024
	}
	return &Runtime{local: local, client: nodeclient.New(local.ControllerURL, local.Credential), logger: logger, workers: map[string]context.CancelFunc{}, statuses: map[string]*linkRuntimeStatus{}, realities: map[string]*realityCore{}, streamSlots: make(chan struct{}, limit)}
}

func (r *Runtime) Run(ctx context.Context) error {
	if err := r.refresh(ctx); err != nil {
		return fmt.Errorf("initial controller config: %w", err)
	}
	r.reconcile(ctx)
	poll := time.NewTicker(r.local.PollInterval.Value(15 * time.Second))
	defer poll.Stop()
	status := time.NewTicker(r.local.StatusInterval.Value(10 * time.Second))
	defer status.Stop()
	for {
		select {
		case <-ctx.Done():
			r.stopWorkers()
			return nil
		case <-poll.C:
			if err := r.refresh(ctx); err != nil {
				r.setError("config: " + err.Error())
			} else {
				r.reconcile(ctx)
			}
		case <-status.C:
			if err := r.report(ctx); err != nil {
				r.setError("status: " + err.Error())
			}
		}
	}
}

func (r *Runtime) refresh(ctx context.Context) error {
	config, err := nodeclient.FetchAgent(ctx, r.client)
	if err != nil {
		return err
	}
	return r.ApplyConfig(config)
}

// ApplyConfig validates and atomically installs a Controller snapshot.
func (r *Runtime) ApplyConfig(config controller.AgentConfig) error {
	if config.Agent.ID != r.local.NodeID {
		return errors.New("controller returned another agent identity")
	}
	if _, err := compilePolicy(config.Agent); err != nil {
		return err
	}
	b, _ := json.Marshal(config)
	r.mu.Lock()
	changed := r.configFingerprint != "" && r.configFingerprint != string(b)
	if changed {
		for _, cancel := range r.workers {
			cancel()
		}
		r.workers = map[string]context.CancelFunc{}
		r.closeRealities()
	}
	r.config = config
	r.configFingerprint = string(b)
	r.mu.Unlock()
	r.setError("")
	return nil
}

// RunLink runs one tunnel connection until the connection or context ends.
func (r *Runtime) RunLink(ctx context.Context, link controller.AgentLinkConfig) error {
	defer r.closeRealities()
	return r.runSession(ctx, link.ID+"#manual", link, 1)
}

func (r *Runtime) reconcile(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	desired := map[string]controller.AgentLinkConfig{}
	for _, link := range r.config.Links {
		for i := 0; i < link.Connections; i++ {
			desired[link.ID+"#"+strconv.Itoa(i)] = link
		}
	}
	for key, cancel := range r.workers {
		if _, ok := desired[key]; !ok {
			cancel()
			delete(r.workers, key)
		}
	}
	for key, link := range desired {
		if _, ok := r.workers[key]; ok {
			continue
		}
		workerCtx, cancel := context.WithCancel(ctx)
		r.workers[key] = cancel
		if r.statuses[link.ID] == nil {
			r.statuses[link.ID] = &linkRuntimeStatus{LinkStatus: model.LinkStatus{LinkID: link.ID}}
		}
		go r.linkLoop(workerCtx, key, link)
	}
}

func (r *Runtime) stopWorkers() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cancel := range r.workers {
		cancel()
	}
	r.workers = map[string]context.CancelFunc{}
	r.closeRealities()
}

func (r *Runtime) linkLoop(ctx context.Context, key string, link controller.AgentLinkConfig) {
	backoff := time.Second
	generation := uint64(0)
	for {
		if ctx.Err() != nil {
			return
		}
		generation++
		err := r.runSession(ctx, key, link, generation)
		if ctx.Err() != nil {
			return
		}
		r.updateStatus(link.ID, func(s *model.LinkStatus) { s.Online = false; s.Ready = false; s.LastError = errString(err) })
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (r *Runtime) runSession(ctx context.Context, key string, link controller.AgentLinkConfig, generation uint64) error {
	var ws *websocket.Conn
	var err error
	if strings.HasPrefix(link.URL, "reality://") {
		var cleanup func()
		ws, cleanup, err = r.dialReality(ctx, link)
		if cleanup != nil {
			defer cleanup()
		}
	} else {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: link.TLSServerName, InsecureSkipVerify: !link.TLSVerify}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = tlsConfig
		ws, _, err = websocket.Dial(ctx, link.URL, &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport, Timeout: 20 * time.Second}, Host: link.HTTPHost, CompressionMode: websocket.CompressionDisabled})
	}
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = ws.Close(websocket.StatusGoingAway, "")
		case <-done:
		}
	}()
	conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	registration := protocol.Message{Type: protocol.TypeRegister, Version: protocol.Version, AgentID: r.local.NodeID, LinkID: link.ID, Token: link.TunnelToken, SessionID: key, Generation: generation}
	if err := protocol.WriteMessage(conn, registration); err != nil {
		return err
	}
	response, err := protocol.ReadMessage(conn)
	if err != nil {
		return err
	}
	if response.Type != protocol.TypeReady || !response.Success {
		return fmt.Errorf("tunnel rejected: %s", response.Error)
	}
	_ = conn.SetDeadline(time.Time{})
	config, err := protocol.NewSMuxConfig()
	if err != nil {
		return fmt.Errorf("configure smux: %w", err)
	}
	session, err := smux.Client(conn, config)
	if err != nil {
		return err
	}
	defer session.Close()
	r.updateStatus(link.ID, func(s *model.LinkStatus) {
		s.Online = true
		s.Ready = true
		s.Generation = generation
		s.Connections++
		s.LastSuccess = time.Now().UTC()
		s.LastError = ""
	})
	defer r.updateStatus(link.ID, func(s *model.LinkStatus) {
		if s.Connections > 0 {
			s.Connections--
		}
		if s.Connections == 0 {
			s.Online = false
			s.Ready = false
		}
	})
	r.logger.Info("agent tunnel ready", "link", link.ID, "worker", key, "generation", generation)
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return err
		}
		if r.acquireStreamSlot() {
			go func() {
				defer r.releaseStreamSlot()
				r.handleStream(ctx, link.ID, stream)
			}()
		} else {
			r.updateStatus(link.ID, func(s *model.LinkStatus) { s.QueueDrops++ })
			_ = stream.Close()
		}
	}
}

func (r *Runtime) acquireStreamSlot() bool {
	select {
	case r.streamSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (r *Runtime) releaseStreamSlot() { <-r.streamSlots }

func (r *Runtime) handleStream(ctx context.Context, linkID string, stream *smux.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(20 * time.Second))
	request, err := protocol.ReadMessage(stream)
	if err != nil {
		return
	}
	if request.Version != protocol.Version {
		_ = protocol.WriteMessage(stream, protocol.Message{Type: protocol.TypeResponse, Version: protocol.Version, ErrorCode: "version", Error: "unsupported protocol version"})
		return
	}
	if request.Type == protocol.TypePing {
		_ = protocol.WriteMessage(stream, protocol.Message{Type: protocol.TypePong, Version: protocol.Version, Success: true})
		return
	}
	if !r.grantAllowed(request.GrantID) {
		_ = protocol.WriteMessage(stream, protocol.Message{Type: protocol.TypeResponse, Version: protocol.Version, ErrorCode: "unauthorized", Error: "grant is not authorized"})
		return
	}
	policy, err := r.policy()
	if err != nil {
		return
	}
	switch request.Type {
	case protocol.TypeTCP:
		r.handleTCP(ctx, linkID, stream, policy, request)
	case protocol.TypeUDP:
		r.handleUDP(ctx, linkID, stream, policy, request)
	default:
		_ = protocol.WriteMessage(stream, protocol.Message{Type: protocol.TypeResponse, Version: protocol.Version, ErrorCode: "unsupported", Error: "unsupported stream type"})
	}
}

func (r *Runtime) handleTCP(ctx context.Context, linkID string, stream *smux.Stream, policy accessPolicy, request protocol.Message) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	target, err := policy.dialTCP(dialCtx, request.Host, request.Port)
	if err != nil {
		_ = protocol.WriteMessage(stream, protocol.Message{Type: protocol.TypeResponse, Version: protocol.Version, ErrorCode: classifyTargetError(err), Error: err.Error()})
		return
	}
	r.tcpConnections.Add(1)
	defer r.tcpConnections.Add(-1)
	if err := protocol.WriteMessage(stream, protocol.Message{Type: protocol.TypeResponse, Version: protocol.Version, Success: true}); err != nil {
		target.Close()
		return
	}
	_ = stream.SetDeadline(time.Time{})
	relay.Bidirectional(stream, target, func(n int) { r.addTraffic(linkID, uint64(n), 0) }, func(n int) { r.addTraffic(linkID, 0, uint64(n)) }, relay.Options{WriteStallTimeout: r.local.WriteStallTimeout.Value(30 * time.Second)})
}

func (r *Runtime) handleUDP(ctx context.Context, linkID string, stream *smux.Stream, policy accessPolicy, request protocol.Message) {
	socket, err := net.ListenUDP("udp", nil)
	if err != nil {
		_ = protocol.WriteMessage(stream, protocol.Message{Type: protocol.TypeResponse, Version: protocol.Version, ErrorCode: "udp_socket", Error: err.Error()})
		return
	}
	defer socket.Close()
	r.udpAssociations.Add(1)
	defer r.udpAssociations.Add(-1)
	if err := protocol.WriteMessage(stream, protocol.Message{Type: protocol.TypeResponse, Version: protocol.Version, Success: true}); err != nil {
		return
	}
	_ = stream.SetDeadline(time.Time{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer socket.Close()
		for {
			datagram, err := protocol.ReadDatagram(stream)
			if err != nil {
				r.logger.Debug("agent UDP tunnel reader ended", "link", linkID, "error", err)
				return
			}
			target, err := policy.udpAddress(ctx, datagram.Host, datagram.Port)
			if err != nil {
				r.logger.Warn("agent UDP target denied", "link", linkID, "target", net.JoinHostPort(datagram.Host, fmt.Sprint(datagram.Port)), "error", err)
				continue
			}
			if _, err = socket.WriteToUDP(datagram.Payload, target); err != nil {
				r.logger.Warn("agent UDP target write failed", "link", linkID, "bytes", len(datagram.Payload), "error", err)
				return
			}
			r.addTraffic(linkID, uint64(len(datagram.Payload)), 0)
		}
	}()
	buffer := make([]byte, protocol.MaxDatagramPayload)
	for {
		_ = socket.SetReadDeadline(time.Now().Add(2 * time.Minute))
		n, source, err := socket.ReadFromUDP(buffer)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				r.logger.Debug("agent UDP socket reader ended", "link", linkID, "error", err)
			}
			return
		}
		reply := protocol.Datagram{Host: source.IP.String(), Port: source.Port, Payload: append([]byte(nil), buffer[:n]...)}
		if err := protocol.WriteDatagram(stream, reply); err != nil {
			r.logger.Warn("agent UDP tunnel write failed", "link", linkID, "bytes", n, "error", err)
			return
		}
		r.addTraffic(linkID, 0, uint64(n))
		select {
		case <-done:
			return
		default:
		}
	}
}

func (r *Runtime) grantAllowed(id string) bool {
	if id == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, allowed := range r.config.GrantIDs {
		if allowed == id {
			return true
		}
	}
	return false
}
func (r *Runtime) policy() (accessPolicy, error) {
	r.mu.RLock()
	agent := r.config.Agent
	r.mu.RUnlock()
	return compilePolicy(agent)
}
func (r *Runtime) updateStatus(linkID string, fn func(*model.LinkStatus)) {
	r.mu.RLock()
	status := r.statuses[linkID]
	r.mu.RUnlock()
	if status == nil {
		return
	}
	status.mu.Lock()
	fn(&status.LinkStatus)
	status.mu.Unlock()
}
func (r *Runtime) addTraffic(linkID string, up, down uint64) {
	r.updateStatus(linkID, func(s *model.LinkStatus) { s.UploadBytes += up; s.DownloadBytes += down })
}
func (r *Runtime) report(ctx context.Context) error {
	r.mu.RLock()
	config := r.config
	statuses := make([]*linkRuntimeStatus, 0, len(r.statuses))
	for _, status := range r.statuses {
		statuses = append(statuses, status)
	}
	r.mu.RUnlock()
	links := make([]model.LinkStatus, 0, len(statuses))
	tunnels := 0
	ready := false
	for _, status := range statuses {
		status.mu.Lock()
		copy := status.LinkStatus
		status.mu.Unlock()
		links = append(links, copy)
		tunnels += copy.Connections
		ready = ready || copy.Ready
	}
	last, _ := r.lastError.Load().(string)
	node := model.NodeStatus{NodeID: r.local.NodeID, Role: model.RoleAgent, BinaryVersion: r.local.BinaryVersion, Online: true, Ready: ready, DesiredVersion: config.Revision, AppliedVersion: config.Revision, LastSeen: time.Now().UTC(), TunnelConnections: tunnels, TCPConnections: int(r.tcpConnections.Load()), UDPAssociations: int(r.udpAssociations.Load()), LastError: last}
	return r.client.Report(ctx, node, links, nil)
}
func (r *Runtime) setError(value string) { r.lastError.Store(value) }
func errString(err error) string {
	if err == nil {
		return "session ended"
	}
	return err.Error()
}
func classifyTargetError(err error) string {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "target_dns"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "target_timeout"
	}
	return "target_dial"
}
