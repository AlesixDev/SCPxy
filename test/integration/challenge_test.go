package integration

import (
	"bytes"
	"testing"
)

const challengeToken = "CHALLENGE-TOKEN-1234"

func TestRejectionIsForwardedVerbatimAndSocketIsReused(t *testing.T) {
	backend := startFakeBackend(t, backendOptions{rejectFirstWith: []byte(challengeToken)})
	instance := startProxy(t, backend.mgr.LocalAddr().String())
	client := startFakeClient(t)

	client.mu.Lock()
	client.onReject = func(data []byte) {
		go client.mgr.Connect(instance.LocalAddr(), buildTestPreAuth())
	}
	client.mu.Unlock()

	client.mgr.Connect(instance.LocalAddr(), buildTestPreAuth())

	waitClosed(t, "the client is accepted after the challenge", client.connected)
	waitClosed(t, "the backend accepts the second connection", backend.accepted)

	_, _, rejected := client.snapshot()

	if !bytes.Equal(rejected, []byte(challengeToken)) {
		t.Fatalf("the proxy did not forward the rejection verbatim: %q", rejected)
	}

	_, _, ports, _ := backend.snapshot()

	if len(ports) < 2 {
		t.Fatalf("the backend only saw %d attempts", len(ports))
	}

	if ports[0] != ports[1] {
		t.Fatalf("the proxy changed upstream port between attempts: %d and %d", ports[0], ports[1])
	}

	for _, item := range instance.Backends() {
		if !item.Healthy {
			t.Fatal("a backend rejection must not mark it as down")
		}
	}
}
