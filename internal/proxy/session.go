package proxy

import (
	"net"
	"sync"
	"time"

	"github.com/AlesixDev/scpxy/internal/litenetlib"
)

const relayQueueSize = 8192

type sessionState byte

const (
	sessionPending sessionState = iota
	sessionActive
	sessionClosed
)

type PlayerState struct {
	ID        int
	UserID    string
	Masked    string
	Version   string
	RealIP    string
	Backend   string
	Latency   int
	Connected time.Duration
	Active    bool
}

type relayMsg struct {
	data     []byte
	channel  byte
	delivery litenetlib.Delivery
}

type session struct {
	mu sync.Mutex

	id       int
	realIP   net.IP
	clientAt *net.UDPAddr
	userID   string
	masked   string
	version  string

	backend     *backend
	backendName string

	link         *upstreamLink
	request      *litenetlib.ConnectionRequest
	clientPeer   *litenetlib.Peer
	upstream     *litenetlib.Manager
	upstreamPeer *litenetlib.Peer

	state   sessionState
	started time.Time

	toClient  chan relayMsg
	toBackend chan relayMsg

	clientReady  chan struct{}
	backendReady chan struct{}
	closed       chan struct{}

	clientOnce  sync.Once
	backendOnce sync.Once
	closeOnce   sync.Once
}

func newSession(id int, ip net.IP, clientAt *net.UDPAddr, target *backend, req *litenetlib.ConnectionRequest) *session {
	return &session{
		id:           id,
		realIP:       ip,
		clientAt:     clientAt,
		backend:      target,
		backendName:  target.cfg.Name,
		request:      req,
		state:        sessionPending,
		started:      time.Now(),
		toClient:     make(chan relayMsg, relayQueueSize),
		toBackend:    make(chan relayMsg, relayQueueSize),
		clientReady:  make(chan struct{}),
		backendReady: make(chan struct{}),
		closed:       make(chan struct{}),
	}
}

func (s *session) markClientReady(peer *litenetlib.Peer) {
	s.mu.Lock()
	s.clientPeer = peer
	s.mu.Unlock()

	s.clientOnce.Do(func() { close(s.clientReady) })
}

func (s *session) markBackendReady(peer *litenetlib.Peer) {
	s.mu.Lock()
	s.upstreamPeer = peer
	s.mu.Unlock()

	s.backendOnce.Do(func() { close(s.backendReady) })
}

func (s *session) enqueue(queue chan relayMsg, data []byte, channel byte, delivery litenetlib.Delivery) bool {
	msg := relayMsg{
		data:     append([]byte(nil), data...),
		channel:  channel,
		delivery: delivery,
	}

	select {
	case <-s.closed:
		return true
	case queue <- msg:
		return true
	default:
		return false
	}
}

func (s *session) snapshot() PlayerState {
	s.mu.Lock()
	defer s.mu.Unlock()

	latency := 0

	if s.clientPeer != nil {
		latency = s.clientPeer.RoundTripTime()
	}

	return PlayerState{
		ID:        s.id,
		UserID:    s.userID,
		Masked:    s.masked,
		Version:   s.version,
		RealIP:    s.realIP.String(),
		Backend:   s.backendName,
		Latency:   latency,
		Connected: time.Since(s.started),
		Active:    s.state == sessionActive,
	}
}

func (s *session) displayName() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.masked != "" {
		return s.masked
	}

	return s.realIP.String()
}
