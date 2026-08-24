package integration

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/AlesixDev/scpxy/internal/config"
	"github.com/AlesixDev/scpxy/internal/events"
	"github.com/AlesixDev/scpxy/internal/litenetlib"
	"github.com/AlesixDev/scpxy/internal/proxy"
)

const (
	testTimeout = 5 * time.Second
	pinnedMtu   = 1024
)

type fakeBackend struct {
	mgr *litenetlib.Manager

	mu        sync.Mutex
	preAuth   []byte
	peer      *litenetlib.Peer
	received  [][]byte
	attempts  int
	seenPorts []int

	accepted chan struct{}
	once     sync.Once
}

type backendOptions struct {
	rejectFirstWith []byte
}

func startFakeBackend(t *testing.T, opts backendOptions) *fakeBackend {
	t.Helper()

	b := &fakeBackend{accepted: make(chan struct{})}

	mgr, err := litenetlib.Listen("127.0.0.1:0", litenetlib.Config{
		AcceptIncoming: true,
		Handler: litenetlib.Handler{
			OnConnectionRequest: func(req *litenetlib.ConnectionRequest) {
				b.mu.Lock()
				b.attempts++
				attempt := b.attempts
				b.seenPorts = append(b.seenPorts, req.Addr().Port)
				b.preAuth = append([]byte(nil), req.Data()...)
				b.mu.Unlock()

				if attempt == 1 && opts.rejectFirstWith != nil {
					req.Reject(opts.rejectFirstWith)
					return
				}

				req.Accept()
			},
			OnPeerConnected: func(peer *litenetlib.Peer) {
				b.mu.Lock()
				b.peer = peer
				b.mu.Unlock()
				b.once.Do(func() { close(b.accepted) })
			},
			OnReceive: func(peer *litenetlib.Peer, data []byte, channel byte, delivery litenetlib.Delivery) {
				b.mu.Lock()
				b.received = append(b.received, append([]byte(nil), data...))
				b.mu.Unlock()
			},
		},
	})

	if err != nil {
		t.Fatalf("cannot open the fake backend: %v", err)
	}

	b.mgr = mgr
	t.Cleanup(mgr.Stop)

	return b
}

func (b *fakeBackend) snapshot() (peer *litenetlib.Peer, received [][]byte, ports []int, preAuth []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.peer, append([][]byte(nil), b.received...), append([]int(nil), b.seenPorts...), b.preAuth
}

type fakeClient struct {
	mgr *litenetlib.Manager

	mu       sync.Mutex
	peer     *litenetlib.Peer
	received [][]byte
	rejected []byte

	connected chan struct{}
	once      sync.Once
	onReject  func(data []byte)
}

func startFakeClient(t *testing.T) *fakeClient {
	t.Helper()

	c := &fakeClient{connected: make(chan struct{})}

	mgr, err := litenetlib.Listen("127.0.0.1:0", litenetlib.Config{
		Handler: litenetlib.Handler{
			OnPeerConnected: func(peer *litenetlib.Peer) {
				c.mu.Lock()
				c.peer = peer
				c.mu.Unlock()
				c.once.Do(func() { close(c.connected) })
			},
			OnPeerDisconnected: func(peer *litenetlib.Peer, reason litenetlib.DisconnectReason, data []byte) {
				if reason != litenetlib.ReasonConnectionRejected {
					return
				}

				c.mu.Lock()
				first := c.rejected == nil
				c.rejected = append([]byte(nil), data...)
				handler := c.onReject
				c.mu.Unlock()

				if first && handler != nil {
					handler(data)
				}
			},
			OnReceive: func(peer *litenetlib.Peer, data []byte, channel byte, delivery litenetlib.Delivery) {
				c.mu.Lock()
				c.received = append(c.received, append([]byte(nil), data...))
				c.mu.Unlock()
			},
		},
	})

	if err != nil {
		t.Fatalf("cannot open the fake client: %v", err)
	}

	c.mgr = mgr
	t.Cleanup(mgr.Stop)

	return c
}

func (c *fakeClient) snapshot() (peer *litenetlib.Peer, received [][]byte, rejected []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.peer, append([][]byte(nil), c.received...), c.rejected
}

func startProxy(t *testing.T, backendAddr string) *proxy.Proxy {
	t.Helper()

	enabled := true
	cfg := config.Default()
	cfg.Proxy.Bind = "127.0.0.1:0"
	cfg.Backends = []config.Backend{{
		Name:           "test",
		Address:        backendAddr,
		Enabled:        &enabled,
		HealthFailures: 3,
	}}

	if err := cfg.Normalize(); err != nil {
		t.Fatalf("invalid configuration: %v", err)
	}

	instance, err := proxy.New(cfg, events.NewBus(events.Warn))

	if err != nil {
		t.Fatalf("cannot create the proxy: %v", err)
	}

	t.Cleanup(instance.Close)

	return instance
}

func buildTestPreAuth() []byte {
	w := litenetlib.NewWriter()
	w.PutByte(1)
	w.PutByte(14)
	w.PutByte(1)
	w.PutByte(0)
	w.PutBool(false)
	w.PutByte(0)
	w.PutString("challenge")
	w.PutString("76561198000000000@steam")
	w.PutInt64(1893456000)
	w.PutByte(0)
	w.PutString("ES")
	w.PutBytesWithLength(bytes.Repeat([]byte{0x11}, 32))

	return w.Bytes()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.After(testTimeout)

	for {
		if cond() {
			return
		}

		select {
		case <-deadline:
			t.Fatalf("timed out waiting for: %s", what)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func waitClosed(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for: %s", what)
	}
}
