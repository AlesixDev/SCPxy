package proxy

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/AlesixDev/scpxy/internal/config"
	"github.com/AlesixDev/scpxy/internal/events"
	"github.com/AlesixDev/scpxy/internal/litenetlib"
	"github.com/AlesixDev/scpxy/internal/preauth"
	"github.com/AlesixDev/scpxy/internal/security"
)

const (
	scopeProxy   = "proxy"
	scopeSession = "session"
	scopeBackend = "backend"
	scopeRelay   = "relay"
	scopeWire    = "wire"

	maintenanceMessage = "The proxy is under maintenance. Try again later."
	backendDownMessage = "No server available right now. Try again later."
	relayBrokenMessage = "Connection lost."
	movedMessage       = "Reconnect to be sent to %s."
	shutdownMessage    = "The proxy is shutting down."

	relayReportPeriod = 10 * time.Second
	relayPreviewBytes = 16
	bannedMessage     = "You have been banned from this proxy."
)

var (
	ErrPlayerNotFound  = errors.New("no player with that id")
	ErrBackendNotFound = errors.New("no backend with that name")
)

type Stats struct {
	Uptime        time.Duration
	Players       int
	Pending       int
	BytesToClient uint64
	BytesToServer uint64
	Security      security.Stats
	Maintenance   bool
}

type Proxy struct {
	cfg    *config.Config
	bus    *events.Bus
	gate   *security.Gate
	pool   *backendPool
	listen *litenetlib.Manager

	mu          sync.Mutex
	sessions    map[string]*session
	byID        map[int]*session
	redirects   map[string]string
	links       map[string]*upstreamLink
	maintenance bool
	nextID      int
	started     time.Time
	stopped     bool

	upstreamSent uint64
	upstreamRecv uint64

	done chan struct{}
}

func New(cfg *config.Config, bus *events.Bus) (*Proxy, error) {
	p := &Proxy{
		cfg:       cfg,
		bus:       bus,
		pool:      newBackendPool(cfg.Backends),
		sessions:  make(map[string]*session),
		byID:      make(map[int]*session),
		redirects: make(map[string]string),
		links:     make(map[string]*upstreamLink),
		done:      make(chan struct{}),
		started:   time.Now(),
	}

	p.gate = security.NewGate(security.Options{
		RatePerSecond:    cfg.Security.ConnectRatePerIP,
		Burst:            cfg.Security.ConnectBurstPerIP,
		BlockDuration:    cfg.Security.BlockDuration.Duration,
		MaxSessionsPerIP: cfg.Security.MaxSessionsPerIP,
		MaxSessions:      cfg.Proxy.MaxPlayers,
		Banned:           cfg.Security.Banned,
	})

	listen, err := litenetlib.Listen(cfg.Proxy.Bind, litenetlib.Config{
		AcceptIncoming:      true,
		DisableMtuDiscovery: true,
		Trace:               p.wireTracer("cliente"),
		Handler: litenetlib.Handler{
			OnConnectionRequest: p.onConnectionRequest,
			OnPeerConnected:     p.onClientConnected,
			OnPeerDisconnected:  p.onClientDisconnected,
			OnReceive:           p.onClientReceive,
		},
	})

	if err != nil {
		return nil, fmt.Errorf("cannot open listen socket: %w", err)
	}

	p.listen = listen

	go p.sweepLinks()
	go p.reportRelay()

	return p, nil
}

func (p *Proxy) Close() {
	p.mu.Lock()

	if p.stopped {
		p.mu.Unlock()
		return
	}

	p.stopped = true
	sessions := make([]*session, 0, len(p.sessions))

	for _, s := range p.sessions {
		sessions = append(sessions, s)
	}

	p.mu.Unlock()

	for _, s := range sessions {
		p.closeSession(s, []byte(shutdownMessage))
	}

	close(p.done)
	p.listen.Stop()
	p.stopAllLinks()
}

func (p *Proxy) onConnectionRequest(req *litenetlib.ConnectionRequest) {
	ip := req.Addr().IP

	if decision := p.gate.Allow(ip); decision != security.Allowed {
		p.bus.Warnf(scopeProxy, "connection refused from %s: %s", ip, decision)
		req.Reject([]byte(decision.Message()))

		return
	}

	p.mu.Lock()
	maintenance := p.maintenance
	stopped := p.stopped
	p.mu.Unlock()

	if maintenance || stopped {
		req.Reject([]byte(maintenanceMessage))
		return
	}

	parsed, err := preauth.Parse(req.Data())

	if err != nil {
		p.bus.Debugf(scopeProxy, "unreadable preauth from %s: %v", ip, err)
	}

	preferred := p.takeRedirect(ip)
	target, addr, ok := p.pool.pick(preferred)

	if !ok {
		p.bus.Errorf(scopeProxy, "no backend available for %s", ip)
		req.Reject([]byte(backendDownMessage))

		return
	}

	connectData := req.Data()

	if p.cfg.Proxy.IPPassthrough {
		connectData = preauth.AppendRealIP(req.Data(), ip)
	}

	p.mu.Lock()
	p.nextID++
	s := newSession(p.nextID, ip, req.Addr(), target, req)

	if parsed != nil {
		s.userID = parsed.UserID
		s.masked = parsed.MaskedUserID()
		s.version = parsed.Version()
	}

	p.sessions[req.Addr().String()] = s
	p.byID[s.id] = s
	p.mu.Unlock()

	p.gate.Acquire(ip)
	p.pool.addPlayer(target, 1)

	link, err := p.acquireLink(req.Addr().String())

	if err != nil {
		p.bus.Errorf(scopeSession, "cannot open upstream socket for %s: %v", ip, err)
		p.dropSession(s)
		req.Reject([]byte(backendDownMessage))

		return
	}

	link.bind(s)

	s.mu.Lock()
	s.link = link
	s.upstream = link.mgr
	s.mu.Unlock()

	go p.pumpToBackend(s)
	go p.pumpToClient(s)

	link.mgr.Connect(addr, connectData)

	time.AfterFunc(p.cfg.Proxy.HandshakeTimeout.Duration, func() {
		p.expireHandshake(s)
	})
}

func (p *Proxy) expireHandshake(s *session) {
	s.mu.Lock()
	pending := s.state == sessionPending
	s.mu.Unlock()

	if !pending {
		return
	}

	p.bus.Warnf(scopeSession, "backend %s did not answer in time for %s", s.backendName, s.realIP)

	if p.pool.markFailure(s.backend, "handshake timeout") {
		p.bus.Errorf(scopeBackend, "%s marked as down", s.backendName)
	}

	s.request.Reject([]byte(backendDownMessage))
	p.dropSession(s)
}

func (p *Proxy) onUpstreamConnected(s *session, peer *litenetlib.Peer) {
	s.mu.Lock()

	if s.state != sessionPending {
		s.mu.Unlock()
		return
	}

	s.state = sessionActive
	s.mu.Unlock()

	s.markBackendReady(peer)
	p.pool.markSuccess(s.backend)
	s.request.Accept()

	p.bus.Infof(scopeSession, "player %s (%s) → %s", s.displayName(), s.realIP, s.backendName)
}

func (p *Proxy) onUpstreamDisconnected(s *session, reason litenetlib.DisconnectReason, data []byte) {
	s.mu.Lock()
	wasPending := s.state == sessionPending
	s.mu.Unlock()

	if wasPending {
		if reason == litenetlib.ReasonConnectionRejected {
			p.bus.Debugf(scopeSession, "backend rejected %s, forwarding its reply verbatim (%d bytes)", s.realIP, len(data))
			s.request.Reject(data)
			p.dropSession(s)

			return
		}

		if p.pool.markFailure(s.backend, reason.String()) {
			p.bus.Errorf(scopeBackend, "%s marked as down (%s)", s.backendName, reason)
		}

		s.request.Reject([]byte(backendDownMessage))
		p.dropSession(s)

		return
	}

	p.bus.Infof(scopeSession, "player %s dropped by the backend (%s)", s.displayName(), reason)
	p.closeSession(s, data)
}

func (p *Proxy) onClientDisconnected(peer *litenetlib.Peer, reason litenetlib.DisconnectReason, data []byte) {
	s := p.sessionByAddr(peer.Addr())

	if s == nil {
		return
	}

	p.bus.Infof(scopeSession, "player %s disconnected (%s)", s.displayName(), reason)
	p.closeSession(s, data)
}

func (p *Proxy) sessionByAddr(addr *net.UDPAddr) *session {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.sessions[addr.String()]
}

func (p *Proxy) dropSession(s *session) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.state = sessionClosed
		link := s.link
		s.mu.Unlock()

		if link != nil {
			link.release()
		}

		close(s.closed)

		p.mu.Lock()
		delete(p.sessions, s.clientAt.String())
		delete(p.byID, s.id)
		p.mu.Unlock()

		p.gate.Release(s.realIP)
		p.pool.addPlayer(s.backend, -1)
	})
}

func (p *Proxy) closeSession(s *session, reason []byte) {
	s.mu.Lock()
	clientPeer := s.clientPeer
	upstream := s.upstream
	upstreamPeer := s.upstreamPeer
	alreadyClosed := s.state == sessionClosed
	s.mu.Unlock()

	if alreadyClosed {
		return
	}

	if clientPeer != nil {
		p.listen.Disconnect(clientPeer, reason)
	}

	if upstream != nil && upstreamPeer != nil {
		upstream.Disconnect(upstreamPeer, reason)
	}

	p.dropSession(s)
}

func (p *Proxy) addTraffic(toServer, toClient uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.upstreamSent += toServer
	p.upstreamRecv += toClient
}

func (p *Proxy) takeRedirect(ip net.IP) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := ip.String()
	target, ok := p.redirects[key]

	if !ok {
		return ""
	}

	delete(p.redirects, key)

	return target
}

func (p *Proxy) Stats() Stats {
	p.mu.Lock()
	pending := 0
	active := 0

	for _, s := range p.sessions {
		s.mu.Lock()

		if s.state == sessionActive {
			active++
		}

		if s.state == sessionPending {
			pending++
		}

		s.mu.Unlock()
	}

	stats := Stats{
		Uptime:        time.Since(p.started),
		Players:       active,
		Pending:       pending,
		BytesToClient: p.upstreamRecv,
		BytesToServer: p.upstreamSent,
		Maintenance:   p.maintenance,
	}

	p.mu.Unlock()

	stats.Security = p.gate.Stats()

	return stats
}

func (p *Proxy) Players() []PlayerState {
	p.mu.Lock()
	sessions := make([]*session, 0, len(p.sessions))

	for _, s := range p.sessions {
		sessions = append(sessions, s)
	}

	p.mu.Unlock()

	out := make([]PlayerState, 0, len(sessions))

	for _, s := range sessions {
		out = append(out, s.snapshot())
	}

	return out
}

func (p *Proxy) Backends() []BackendState {
	return p.pool.snapshot()
}

func (p *Proxy) Kick(id int, reason string) error {
	p.mu.Lock()
	s, ok := p.byID[id]
	p.mu.Unlock()

	if !ok {
		return ErrPlayerNotFound
	}

	if reason == "" {
		reason = "Kicked by an administrator."
	}

	p.closeSession(s, []byte(reason))

	return nil
}

func (p *Proxy) Move(id int, backendName string) error {
	if p.pool.byName(backendName) == nil {
		return ErrBackendNotFound
	}

	p.mu.Lock()
	s, ok := p.byID[id]

	if !ok {
		p.mu.Unlock()
		return ErrPlayerNotFound
	}

	p.redirects[s.realIP.String()] = backendName
	p.mu.Unlock()

	p.closeSession(s, []byte(fmt.Sprintf(movedMessage, backendName)))

	return nil
}

func (p *Proxy) SetBackendEnabled(name string, enabled bool) error {
	if !p.pool.setEnabled(name, enabled) {
		return ErrBackendNotFound
	}

	return nil
}

func (p *Proxy) SetMaintenance(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.maintenance = on
}

func (p *Proxy) Ban(raw string) error {
	ip := net.ParseIP(strings.TrimSpace(raw))

	if ip == nil {
		return fmt.Errorf("%q is not a valid IP address", raw)
	}

	p.gate.Ban(ip)

	for _, s := range p.sessionsForIP(ip) {
		p.closeSession(s, []byte(bannedMessage))
	}

	return nil
}

func (p *Proxy) Unban(raw string) error {
	ip := net.ParseIP(strings.TrimSpace(raw))

	if ip == nil {
		return fmt.Errorf("%q is not a valid IP address", raw)
	}

	if !p.gate.Unban(ip) {
		return fmt.Errorf("%s was not banned", ip)
	}

	return nil
}

func (p *Proxy) BannedList() []string {
	return p.gate.BannedList()
}

func (p *Proxy) sessionsForIP(ip net.IP) []*session {
	key := ip.String()

	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]*session, 0, 1)

	for _, s := range p.sessions {
		if s.realIP.String() != key {
			continue
		}

		out = append(out, s)
	}

	return out
}

func (p *Proxy) LocalAddr() *net.UDPAddr {
	return p.listen.LocalAddr()
}

func (p *Proxy) reportRelay() {
	ticker := time.NewTicker(relayReportPeriod)
	defer ticker.Stop()

	var lastSent, lastRecv uint64

	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			stats := p.Stats()

			if stats.Players == 0 && stats.Pending == 0 {
				continue
			}

			deltaSent := stats.BytesToServer - lastSent
			deltaRecv := stats.BytesToClient - lastRecv
			lastSent = stats.BytesToServer
			lastRecv = stats.BytesToClient

			if deltaSent == 0 && deltaRecv == 0 {
				p.bus.Warnf(scopeSession, "relay stalled: %d active sessions and 0 bytes in %s", stats.Players, relayReportPeriod)
				continue
			}

			p.bus.Infof(scopeSession, "relay: %d players, %d B upstream, %d B downstream in %s",
				stats.Players, deltaSent, deltaRecv, relayReportPeriod)
		}
	}
}

func relayable(peer *litenetlib.Peer, data []byte, delivery litenetlib.Delivery) bool {
	if len(data) <= peer.MaxSinglePacketSize(delivery) {
		return true
	}

	return delivery == litenetlib.ReliableOrdered || delivery == litenetlib.ReliableUnordered
}

func (p *Proxy) onClientConnected(peer *litenetlib.Peer) {
	s := p.sessionByAddr(peer.Addr())

	if s == nil {
		p.listen.Disconnect(peer, []byte(relayBrokenMessage))
		return
	}

	s.markClientReady(peer)
}

func (p *Proxy) onClientReceive(peer *litenetlib.Peer, data []byte, channel byte, delivery litenetlib.Delivery) {
	s := p.sessionByAddr(peer.Addr())

	if s == nil {
		p.listen.Disconnect(peer, []byte(relayBrokenMessage))
		return
	}

	if s.enqueue(s.toBackend, data, channel, delivery) {
		return
	}

	p.bus.Warnf(scopeSession, "upstream queue full for %s, closing session", s.displayName())
	go p.closeSession(s, []byte(relayBrokenMessage))
}

func (p *Proxy) onUpstreamReceive(s *session, data []byte, channel byte, delivery litenetlib.Delivery) {
	if s.enqueue(s.toClient, data, channel, delivery) {
		return
	}

	p.bus.Warnf(scopeSession, "downstream queue full for %s, closing session", s.displayName())
	go p.closeSession(s, []byte(relayBrokenMessage))
}

func (p *Proxy) pumpToBackend(s *session) {
	select {
	case <-s.backendReady:
	case <-s.closed:
		return
	}

	s.mu.Lock()
	upstream := s.upstream
	peer := s.upstreamPeer
	s.mu.Unlock()

	for {
		select {
		case <-s.closed:
			return
		case msg := <-s.toBackend:
			if !relayable(peer, msg.data, msg.delivery) {
				p.bus.Warnf(scopeSession, "cannot forward %d-byte message from %s upstream, closing session", len(msg.data), s.displayName())
				p.closeSession(s, []byte(relayBrokenMessage))

				return
			}

			p.traceRelay("cliente→backend", s, msg)
			upstream.Send(peer, msg.data, msg.channel, msg.delivery)
			p.addTraffic(uint64(len(msg.data)), 0)
		}
	}
}

func (p *Proxy) pumpToClient(s *session) {
	select {
	case <-s.clientReady:
	case <-s.closed:
		return
	}

	s.mu.Lock()
	peer := s.clientPeer
	s.mu.Unlock()

	for {
		select {
		case <-s.closed:
			return
		case msg := <-s.toClient:
			if !relayable(peer, msg.data, msg.delivery) {
				p.bus.Warnf(scopeSession, "cannot forward %d-byte message to client %s, closing session", len(msg.data), s.displayName())
				p.closeSession(s, []byte(relayBrokenMessage))

				return
			}

			p.traceRelay("backend→cliente", s, msg)
			p.listen.Send(peer, msg.data, msg.channel, msg.delivery)
			p.addTraffic(0, uint64(len(msg.data)))
		}
	}
}

func (p *Proxy) traceRelay(direction string, s *session, msg relayMsg) {
	if !p.cfg.Proxy.DebugRelay {
		return
	}

	preview := msg.data

	if len(preview) > relayPreviewBytes {
		preview = preview[:relayPreviewBytes]
	}

	p.bus.Debugf(scopeRelay, "%s %s ch=%d %s %d B %x",
		s.displayName(), direction, msg.channel, msg.delivery, len(msg.data), preview)
}

func (p *Proxy) wireTracer(side string) func(string, ...any) {
	if !p.cfg.Proxy.DebugRelay {
		return nil
	}

	return func(format string, args ...any) {
		p.bus.Debugf(scopeWire, side+" "+format, args...)
	}
}
