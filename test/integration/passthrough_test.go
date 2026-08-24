package integration

import (
	"bytes"
	"testing"
	"time"

	"github.com/AlesixDev/scpxy/internal/config"
	"github.com/AlesixDev/scpxy/internal/events"
	"github.com/AlesixDev/scpxy/internal/litenetlib"
	"github.com/AlesixDev/scpxy/internal/preauth"
	"github.com/AlesixDev/scpxy/internal/proxy"
)

func TestProxyForwardsPreAuthWithRealIPAndRelays(t *testing.T) {
	backend := startFakeBackend(t, backendOptions{})
	instance := startProxy(t, backend.mgr.LocalAddr().String())
	client := startFakeClient(t)

	raw := buildTestPreAuth()
	client.mgr.Connect(instance.LocalAddr(), raw)

	waitClosed(t, "the backend accepts the proxy connection", backend.accepted)

	_, _, _, forwarded := backend.snapshot()

	if !bytes.HasPrefix(forwarded, raw) {
		t.Fatal("the proxy altered the original PreAuth")
	}

	appended := litenetlib.NewReader(forwarded[len(raw):])
	realIP, err := appended.String()

	if err != nil {
		t.Fatalf("cannot read the appended IP: %v", err)
	}

	if realIP != "127.0.0.1" {
		t.Fatalf("appended IP = %q", realIP)
	}

	if appended.Remaining() != 0 {
		t.Fatalf("%d bytes left after the appended IP", appended.Remaining())
	}

	waitClosed(t, "the client connects to the proxy", client.connected)

	upstream := bytes.Repeat([]byte{0x77}, 64)
	clientPeer, _, _ := client.snapshot()
	client.mgr.Send(clientPeer, upstream, 0, litenetlib.ReliableOrdered)

	waitFor(t, "the backend receives the client message", func() bool {
		_, received, _, _ := backend.snapshot()

		return len(received) > 0
	})

	backendPeer, received, _, _ := backend.snapshot()

	if !bytes.Equal(received[0], upstream) {
		t.Fatalf("the backend received %v", received[0])
	}

	downstream := bytes.Repeat([]byte{0x33}, 48)
	backend.mgr.Send(backendPeer, downstream, 0, litenetlib.ReliableOrdered)

	waitFor(t, "the client receives the backend message", func() bool {
		_, got, _ := client.snapshot()

		return len(got) > 0
	})

	_, got, _ := client.snapshot()

	if !bytes.Equal(got[0], downstream) {
		t.Fatalf("the client received %v", got[0])
	}

	players := instance.Players()

	if len(players) != 1 {
		t.Fatalf("players = %d", len(players))
	}

	if players[0].Backend != "test" {
		t.Fatalf("player backend = %q", players[0].Backend)
	}

	if players[0].RealIP != "127.0.0.1" {
		t.Fatalf("player real ip = %q", players[0].RealIP)
	}

	parsed, err := preauth.Parse(raw)

	if err != nil {
		t.Fatalf("cannot parse the test PreAuth: %v", err)
	}

	if players[0].Masked != parsed.MaskedUserID() {
		t.Fatalf("masked user = %q", players[0].Masked)
	}
}

func TestProxyRejectsWhenNoBackendAvailable(t *testing.T) {
	disabled := false
	cfg := config.Default()
	cfg.Proxy.Bind = "127.0.0.1:0"
	cfg.Backends = []config.Backend{{
		Name:           "offline",
		Address:        "127.0.0.1:1",
		Enabled:        &disabled,
		HealthFailures: 1,
	}}

	if err := cfg.Normalize(); err != nil {
		t.Fatalf("invalid configuration: %v", err)
	}

	instance, err := proxy.New(cfg, events.NewBus(events.Warn))

	if err != nil {
		t.Fatalf("cannot create the proxy: %v", err)
	}

	defer instance.Close()

	client := startFakeClient(t)
	client.mgr.Connect(instance.LocalAddr(), buildTestPreAuth())

	select {
	case <-client.connected:
		t.Fatal("the proxy accepted a player with no backend available")
	case <-time.After(time.Second):
	}
}
