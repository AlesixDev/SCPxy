package litenetlib

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

var (
	ErrManagerStopped = errors.New("litenetlib: manager stopped")
	ErrBadPacket      = errors.New("litenetlib: malformed packet")
)

type Handler struct {
	OnConnectionRequest func(req *ConnectionRequest)
	OnPeerConnected     func(peer *Peer)
	OnPeerDisconnected  func(peer *Peer, reason DisconnectReason, data []byte)
	OnReceive           func(peer *Peer, data []byte, channel byte, delivery Delivery)
}

type Config struct {
	Handler             Handler
	AcceptIncoming      bool
	ChannelsCount       byte
	PingInterval        time.Duration
	DisconnectTimeout   time.Duration
	ReconnectDelay      time.Duration
	MaxConnectAttempts  int
	UpdateInterval      time.Duration
	DisableMtuDiscovery bool
	Trace               func(format string, args ...any)
}

func (c *Config) applyDefaults() {
	if c.ChannelsCount == 0 {
		c.ChannelsCount = defaultChannelsCount
	}

	if c.PingInterval == 0 {
		c.PingInterval = defaultPingInterval
	}

	if c.DisconnectTimeout == 0 {
		c.DisconnectTimeout = defaultDisconnectAfter
	}

	if c.ReconnectDelay == 0 {
		c.ReconnectDelay = defaultReconnectDelay
	}

	if c.MaxConnectAttempts == 0 {
		c.MaxConnectAttempts = defaultConnectAttempts
	}

	if c.UpdateInterval == 0 {
		c.UpdateInterval = defaultUpdateInterval
	}
}

type connectRequest struct {
	addr             *net.UDPAddr
	connectionTime   int64
	connectionNumber byte
	peerID           int
	data             []byte
}

type ConnectionRequest struct {
	mgr      *Manager
	internal *connectRequest
	resolved bool
}

func (r *ConnectionRequest) Addr() *net.UDPAddr {
	return r.internal.addr
}

func (r *ConnectionRequest) Data() []byte {
	return r.internal.data
}

func (r *ConnectionRequest) Accept() {
	r.mgr.post(func() {
		r.mgr.resolveRequest(r, true, nil)
	})
}

func (r *ConnectionRequest) Reject(data []byte) {
	payload := append([]byte(nil), data...)

	r.mgr.post(func() {
		r.mgr.resolveRequest(r, false, payload)
	})
}

type inbound struct {
	data []byte
	addr *net.UDPAddr
}

type Manager struct {
	conn     *net.UDPConn
	handler  Handler
	accept   bool
	stopOnce sync.Once

	channelsCount      byte
	pingInterval       time.Duration
	disconnectTimeout  time.Duration
	reconnectDelay     time.Duration
	maxConnectAttempts int
	updateInterval     time.Duration
	disableMtu         bool
	trace              func(format string, args ...any)

	peers    map[string]*Peer
	requests map[string]*ConnectionRequest
	lastID   int

	packets  chan inbound
	commands chan func()
	done     chan struct{}
	closed   chan struct{}

	statsMu       sync.Mutex
	bytesSent     uint64
	bytesReceived uint64
}

func Listen(bind string, cfg Config) (*Manager, error) {
	cfg.applyDefaults()

	addr, err := net.ResolveUDPAddr("udp", bind)

	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", bind, err)
	}

	conn, err := net.ListenUDP("udp", addr)

	if err != nil {
		return nil, fmt.Errorf("listen %q: %w", bind, err)
	}

	_ = conn.SetReadBuffer(socketBufferSize)
	_ = conn.SetWriteBuffer(socketBufferSize)

	m := &Manager{
		conn:               conn,
		handler:            cfg.Handler,
		accept:             cfg.AcceptIncoming,
		channelsCount:      cfg.ChannelsCount,
		pingInterval:       cfg.PingInterval,
		disconnectTimeout:  cfg.DisconnectTimeout,
		reconnectDelay:     cfg.ReconnectDelay,
		maxConnectAttempts: cfg.MaxConnectAttempts,
		updateInterval:     cfg.UpdateInterval,
		disableMtu:         cfg.DisableMtuDiscovery,
		trace:              cfg.Trace,
		peers:              make(map[string]*Peer),
		requests:           make(map[string]*ConnectionRequest),
		packets:            make(chan inbound, incomingPacketQueueSize),
		commands:           make(chan func(), commandQueueSize),
		done:               make(chan struct{}),
		closed:             make(chan struct{}),
	}

	go m.readLoop()
	go m.eventLoop()

	return m, nil
}

func (m *Manager) LocalAddr() *net.UDPAddr {
	return m.conn.LocalAddr().(*net.UDPAddr)
}

func (m *Manager) Traffic() (sent uint64, received uint64) {
	m.statsMu.Lock()
	defer m.statsMu.Unlock()

	return m.bytesSent, m.bytesReceived
}

func (m *Manager) post(fn func()) {
	select {
	case <-m.done:
	case m.commands <- fn:
	}
}

func (m *Manager) Connect(addr *net.UDPAddr, connectData []byte) {
	payload := append([]byte(nil), connectData...)

	m.post(func() {
		key := addr.String()

		if _, ok := m.peers[key]; ok {
			return
		}

		m.lastID++
		peer := newOutgoingPeer(m, addr, m.lastID, 0, payload)
		m.peers[key] = peer
	})
}

func (m *Manager) Send(peer *Peer, data []byte, channel byte, delivery Delivery) {
	payload := append([]byte(nil), data...)

	m.post(func() {
		peer.send(payload, channel, delivery)
	})
}

func (m *Manager) Disconnect(peer *Peer, data []byte) {
	payload := append([]byte(nil), data...)

	m.post(func() {
		m.disconnectPeer(peer, payload)
	})
}

func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		done := make(chan struct{})

		select {
		case m.commands <- func() {
			for _, peer := range m.peers {
				peer.shutdown(nil, false)
			}

			close(done)
		}:
			select {
			case <-done:
			case <-time.After(time.Second):
			}
		case <-time.After(time.Second):
		}

		close(m.done)
		_ = m.conn.Close()
		<-m.closed
	})
}

func (m *Manager) readLoop() {
	buf := make([]byte, receiveBufferSize)

	for {
		n, addr, err := m.conn.ReadFromUDP(buf)

		if err != nil {
			select {
			case <-m.done:
				return
			default:
			}

			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}

			continue
		}

		if n <= 0 {
			continue
		}

		data := make([]byte, n)
		copy(data, buf[:n])

		select {
		case m.packets <- inbound{data: data, addr: addr}:
		case <-m.done:
			return
		default:
		}
	}
}

func (m *Manager) eventLoop() {
	defer close(m.closed)

	ticker := time.NewTicker(m.updateInterval)
	defer ticker.Stop()

	last := time.Now()

	for {
		select {
		case <-m.done:
			return
		case fn := <-m.commands:
			fn()
		case pkt := <-m.packets:
			m.receive(pkt.data, pkt.addr)
		case now := <-ticker.C:
			delta := now.Sub(last)
			last = now
			m.update(delta)
		}
	}
}

func (m *Manager) update(delta time.Duration) {
	for key, peer := range m.peers {
		if peer.state == stateDisconnected && peer.sinceLastPacket > m.disconnectTimeout {
			delete(m.peers, key)
			continue
		}

		peer.update(delta)
	}
}

func (m *Manager) dispatchReceive(peer *Peer, data []byte, channel byte, delivery Delivery) {
	if m.handler.OnReceive == nil {
		return
	}

	m.handler.OnReceive(peer, data, channel, delivery)
}

func (m *Manager) sendRaw(pkt *packet, addr *net.UDPAddr) int {
	return m.sendRawBytes(pkt.data, addr)
}

func (m *Manager) sendRawBytes(data []byte, addr *net.UDPAddr) int {
	n, err := m.conn.WriteToUDP(data, addr)

	if err != nil {
		return 0
	}

	m.statsMu.Lock()
	m.bytesSent += uint64(n)
	m.statsMu.Unlock()

	return n
}

func (m *Manager) sendProperty(p property, addr *net.UDPAddr) {
	m.sendRaw(newPacket(p, 0), addr)
}

func (m *Manager) receive(data []byte, addr *net.UDPAddr) {
	m.statsMu.Lock()
	m.bytesReceived += uint64(len(data))
	m.statsMu.Unlock()

	pkt := &packet{data: data}

	if !pkt.verify() {
		m.traceRejected(data, addr)
		return
	}

	m.traceIncoming(pkt, addr)

	key := addr.String()
	peer := m.peers[key]

	switch pkt.property() {
	case propConnectRequest:
		if int32(binary.LittleEndian.Uint32(pkt.data[1:])) != protocolID {
			m.sendProperty(propInvalidProtocol, addr)
			return
		}

		req, err := parseConnectRequest(pkt, addr)

		if err != nil {
			return
		}

		m.processConnectRequest(req, peer)
	case propConnectAccept:
		if peer == nil {
			return
		}

		if !peer.processConnectAccept(pkt) {
			return
		}

		if m.handler.OnPeerConnected != nil {
			m.handler.OnPeerConnected(peer)
		}
	case propDisconnect:
		if peer != nil && peer.processDisconnect(pkt) {
			reason := ReasonRemoteConnectionClose

			if peer.state == stateOutgoing {
				reason = ReasonConnectionRejected
			}

			payload := append([]byte(nil), pkt.data[9:]...)
			m.disconnectPeerForce(peer, reason, payload)
		}

		m.sendProperty(propShutdownOk, addr)
	case propInvalidProtocol:
		if peer != nil && peer.state == stateOutgoing {
			m.disconnectPeerForce(peer, ReasonInvalidProtocol, nil)
		}
	case propPeerNotFound:
		if peer != nil && peer.state == stateConnected {
			m.disconnectPeerForce(peer, ReasonPeerNotFound, nil)
		}
	case propUnconnectedMessage, propBroadcast, propNatMessage:
		return
	default:
		if peer == nil {
			m.sendProperty(propPeerNotFound, addr)
			return
		}

		peer.processPacket(pkt)
	}
}

func parseConnectRequest(pkt *packet, addr *net.UDPAddr) (*connectRequest, error) {
	if pkt.size() < connectRequestHeader {
		return nil, ErrBadPacket
	}

	if pkt.connectionNumber() >= maxConnectionNumber {
		return nil, ErrBadPacket
	}

	addrSize := int(pkt.data[connectRequestHeader-1])

	if addrSize != 16 && addrSize != 28 {
		return nil, ErrBadPacket
	}

	if pkt.size() < connectRequestHeader+addrSize {
		return nil, ErrBadPacket
	}

	req := &connectRequest{
		addr:             addr,
		connectionTime:   int64(binary.LittleEndian.Uint64(pkt.data[5:])),
		connectionNumber: pkt.connectionNumber(),
		peerID:           int(int32(binary.LittleEndian.Uint32(pkt.data[13:]))),
	}

	payload := pkt.data[connectRequestHeader+addrSize:]
	req.data = make([]byte, len(payload))
	copy(req.data, payload)

	return req, nil
}

func (m *Manager) processConnectRequest(req *connectRequest, peer *Peer) {
	if !m.accept {
		return
	}

	key := req.addr.String()

	if peer != nil {
		switch peer.state {
		case stateOutgoing:
			return
		case stateConnected:
			if req.connectionTime == peer.connectTime {
				m.sendRaw(peer.connectAcceptPacket, peer.addr)
				return
			}

			if req.connectionTime < peer.connectTime {
				return
			}

			m.disconnectPeerForce(peer, ReasonReconnect, nil)
		case stateDisconnected, stateShutdownRequested:
			if req.connectionTime < peer.connectTime {
				return
			}

			m.disconnectPeerForce(peer, ReasonReconnect, nil)
		}

		delete(m.peers, key)
	}

	if existing, ok := m.requests[key]; ok {
		existing.internal = req
		return
	}

	request := &ConnectionRequest{mgr: m, internal: req}
	m.requests[key] = request

	if m.handler.OnConnectionRequest == nil {
		m.resolveRequest(request, false, nil)
		return
	}

	m.handler.OnConnectionRequest(request)
}

func (m *Manager) resolveRequest(req *ConnectionRequest, accept bool, rejectData []byte) {
	if req.resolved {
		return
	}

	req.resolved = true
	key := req.internal.addr.String()
	delete(m.requests, key)

	if !accept {
		pkt := newPacket(propDisconnect, len(rejectData))
		pkt.setConnectionNumber(req.internal.connectionNumber)
		binary.LittleEndian.PutUint64(pkt.data[1:], uint64(req.internal.connectionTime))

		if len(rejectData) > 0 {
			copy(pkt.data[9:], rejectData)
		}

		m.sendRaw(pkt, req.internal.addr)

		return
	}

	if _, ok := m.peers[key]; ok {
		return
	}

	m.lastID++
	peer := newAcceptedPeer(m, req.internal, m.lastID)
	m.peers[key] = peer

	if m.handler.OnPeerConnected != nil {
		m.handler.OnPeerConnected(peer)
	}
}

func (m *Manager) disconnectPeer(peer *Peer, data []byte) {
	if !peer.shutdown(data, false) {
		return
	}

	if m.handler.OnPeerDisconnected != nil {
		m.handler.OnPeerDisconnected(peer, ReasonDisconnectPeerCalled, nil)
	}
}

func (m *Manager) disconnectPeerForce(peer *Peer, reason DisconnectReason, data []byte) {
	wasConnected := peer.shutdown(nil, true)

	if !wasConnected && reason != ReasonConnectionFailed && reason != ReasonConnectionRejected {
		return
	}

	delete(m.peers, peer.key)

	if m.handler.OnPeerDisconnected != nil {
		m.handler.OnPeerDisconnected(peer, reason, data)
	}
}

func (m *Manager) tracef(format string, args ...any) {
	if m.trace == nil {
		return
	}

	m.trace(format, args...)
}

func (m *Manager) traceIncoming(pkt *packet, addr *net.UDPAddr) {
	if m.trace == nil {
		return
	}

	switch pkt.property() {
	case propPing, propPong, propAck:
		return
	case propChanneled:
		m.trace("rx %s from %s ch=%d seq=%d frag=%t %d B",
			pkt.property(), addr, pkt.channelID(), pkt.sequence(), pkt.isFragmented(), pkt.size())
	default:
		m.trace("rx %s from %s %d B", pkt.property(), addr, pkt.size())
	}
}

func (m *Manager) traceRejected(data []byte, addr *net.UDPAddr) {
	if m.trace == nil {
		return
	}

	preview := data

	if len(preview) > rejectPreviewBytes {
		preview = preview[:rejectPreviewBytes]
	}

	prop := data[0] & 0x1F
	m.trace("rx REJECTED from %s prop=%d connNum=%d frag=%t expectedHeader=%d (%d B) %x",
		addr, prop, (data[0]&0x60)>>5, data[0]&0x80 != 0, expectedHeaderSize(prop), len(data), preview)
}

func expectedHeaderSize(prop byte) int {
	if int(prop) >= len(headerSizes) {
		return -1
	}

	return headerSizes[prop]
}
