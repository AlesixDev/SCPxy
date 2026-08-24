package integration

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/AlesixDev/scpxy/internal/litenetlib"
)

const burstMessages = 2000

var burstSizes = []int{16, 300, 900, 1100, 1400, 3000}

func payloadOfSize(seq int, size int) []byte {
	out := make([]byte, size)
	binary.LittleEndian.PutUint32(out, uint32(seq))

	for i := 4; i < size; i++ {
		out[i] = byte(seq + i)
	}

	return out
}

func TestRelayKeepsOrderAndLosesNothing(t *testing.T) {
	backend := startFakeBackend(t, backendOptions{})
	instance := startProxy(t, backend.mgr.LocalAddr().String())
	client := startFakeClient(t)

	client.mgr.Connect(instance.LocalAddr(), buildTestPreAuth())

	waitClosed(t, "the client connects", client.connected)
	waitClosed(t, "the backend connects", backend.accepted)

	clientPeer, _, _ := client.snapshot()
	backendPeer, _, _, _ := backend.snapshot()

	for seq := 0; seq < burstMessages; seq++ {
		payload := payloadOfSize(seq, burstSizes[seq%len(burstSizes)])
		client.mgr.Send(clientPeer, payload, 0, litenetlib.ReliableOrdered)
		backend.mgr.Send(backendPeer, payload, 0, litenetlib.ReliableOrdered)
	}

	waitFor(t, "every message crosses the proxy", func() bool {
		_, upstream, _, _ := backend.snapshot()
		_, downstream, _ := client.snapshot()

		return len(upstream) >= burstMessages && len(downstream) >= burstMessages
	})

	_, upstream, _, _ := backend.snapshot()
	_, downstream, _ := client.snapshot()

	for i := 0; i < burstMessages; i++ {
		if got := int(binary.LittleEndian.Uint32(upstream[i])); got != i {
			t.Fatalf("the backend received %d at position %d", got, i)
		}

		if got := int(binary.LittleEndian.Uint32(downstream[i])); got != i {
			t.Fatalf("the client received %d at position %d", got, i)
		}
	}
}

func TestProxyPinsMtuOnBothHops(t *testing.T) {
	backend := startFakeBackend(t, backendOptions{})
	instance := startProxy(t, backend.mgr.LocalAddr().String())
	client := startFakeClient(t)

	client.mgr.Connect(instance.LocalAddr(), buildTestPreAuth())

	waitClosed(t, "the client connects", client.connected)
	waitClosed(t, "the backend connects", backend.accepted)

	time.Sleep(6 * time.Second)

	backendPeer, _, _, _ := backend.snapshot()
	clientPeer, _, _ := client.snapshot()

	if got := backendPeer.Mtu(); got != pinnedMtu {
		t.Fatalf("the backend raised its MTU to %d; the proxy should prevent that", got)
	}

	if got := clientPeer.Mtu(); got != pinnedMtu {
		t.Fatalf("the client raised its MTU to %d; the proxy should prevent that", got)
	}
}
