package litenetlib

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"
)

func TestPacketHeaderLayout(t *testing.T) {
	p := newPacket(propChanneled, 10)

	if p.size() != channeledHeaderSize+10 {
		t.Fatalf("size = %d", p.size())
	}

	p.setSequence(1234)
	p.setChannelID(7)
	p.setConnectionNumber(2)
	p.markFragmented()

	if p.property() != propChanneled {
		t.Fatalf("property = %d", p.property())
	}

	if p.sequence() != 1234 {
		t.Fatalf("sequence = %d", p.sequence())
	}

	if p.channelID() != 7 {
		t.Fatalf("channelID = %d", p.channelID())
	}

	if p.connectionNumber() != 2 {
		t.Fatalf("connectionNumber = %d", p.connectionNumber())
	}

	if !p.isFragmented() {
		t.Fatal("isFragmented = false")
	}
}

func TestHeaderSizesMatchUpstream(t *testing.T) {
	cases := map[property]int{
		propUnreliable:     1,
		propChanneled:      4,
		propAck:            4,
		propPing:           3,
		propPong:           11,
		propConnectRequest: 18,
		propConnectAccept:  15,
		propDisconnect:     9,
		propMerged:         1,
	}

	for prop, want := range cases {
		if got := propertyHeaderSize(prop); got != want {
			t.Errorf("headerSize(%d) = %d, expected %d", prop, got, want)
		}
	}
}

func TestRelativeSequenceWrapsAround(t *testing.T) {
	if got := relativeSequence(1, 0); got != 1 {
		t.Fatalf("relativeSequence(1,0) = %d", got)
	}

	if got := relativeSequence(0, maxSequence-1); got != 1 {
		t.Fatalf("relativeSequence across the wrap = %d", got)
	}

	if got := relativeSequence(maxSequence-1, 0); got != -1 {
		t.Fatalf("relativeSequence going backwards = %d", got)
	}
}

func TestStringEncodingMatchesNetDataWriter(t *testing.T) {
	w := NewWriter()
	w.PutString("hola")

	want := []byte{0x05, 0x00, 'h', 'o', 'l', 'a'}

	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("encoding = %v, expected %v", w.Bytes(), want)
	}

	w = NewWriter()
	w.PutString("")

	if !bytes.Equal(w.Bytes(), []byte{0x00, 0x00}) {
		t.Fatalf("empty string = %v", w.Bytes())
	}
}

func TestSocketAddressIPv4Layout(t *testing.T) {
	addr := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 7777}
	out := serializeSocketAddress(addr)

	if len(out) != 16 {
		t.Fatalf("len = %d", len(out))
	}

	if out[0] != addressFamilyIPv4 || out[1] != 0 {
		t.Fatalf("family = %v", out[:2])
	}

	if out[2] != 0x1E || out[3] != 0x61 {
		t.Fatalf("big endian port = %v", out[2:4])
	}

	if !bytes.Equal(out[4:8], []byte{192, 0, 2, 10}) {
		t.Fatalf("ip = %v", out[4:8])
	}
}

func TestHandshakeAndReliableRelay(t *testing.T) {
	var (
		mu       sync.Mutex
		received [][]byte
		serverUp = make(chan struct{})
		clientUp = make(chan struct{})
	)

	server, err := Listen("127.0.0.1:0", Config{
		AcceptIncoming: true,
		Handler: Handler{
			OnConnectionRequest: func(req *ConnectionRequest) {
				if string(req.Data()) != "hello-preauth" {
					req.Reject(nil)
					return
				}

				req.Accept()
			},
			OnPeerConnected: func(peer *Peer) { close(serverUp) },
			OnReceive: func(peer *Peer, data []byte, channel byte, delivery Delivery) {
				mu.Lock()
				received = append(received, append([]byte(nil), data...))
				mu.Unlock()
			},
		},
	})

	if err != nil {
		t.Fatalf("cannot open the server: %v", err)
	}

	defer server.Stop()

	var clientPeer *Peer

	client, err := Listen("127.0.0.1:0", Config{
		Handler: Handler{
			OnPeerConnected: func(peer *Peer) {
				clientPeer = peer
				close(clientUp)
			},
		},
	})

	if err != nil {
		t.Fatalf("cannot open the client: %v", err)
	}

	defer client.Stop()

	client.Connect(server.LocalAddr(), []byte("hello-preauth"))

	select {
	case <-clientUp:
	case <-time.After(3 * time.Second):
		t.Fatal("the client did not finish the handshake")
	}

	select {
	case <-serverUp:
	case <-time.After(3 * time.Second):
		t.Fatal("the server did not accept the connection")
	}

	payload := bytes.Repeat([]byte{0x42}, 32)
	client.Send(clientPeer, payload, 0, ReliableOrdered)

	deadline := time.After(3 * time.Second)

	for {
		mu.Lock()
		count := len(received)
		mu.Unlock()

		if count > 0 {
			break
		}

		select {
		case <-deadline:
			t.Fatal("the server did not receive the message")
		case <-time.After(20 * time.Millisecond):
		}
	}

	mu.Lock()
	got := received[0]
	mu.Unlock()

	if !bytes.Equal(got, payload) {
		t.Fatalf("received payload = %v", got)
	}
}

func TestFragmentedMessageIsReassembled(t *testing.T) {
	var (
		mu       sync.Mutex
		received []byte
		clientUp = make(chan struct{})
	)

	server, err := Listen("127.0.0.1:0", Config{
		AcceptIncoming: true,
		Handler: Handler{
			OnConnectionRequest: func(req *ConnectionRequest) { req.Accept() },
			OnReceive: func(peer *Peer, data []byte, channel byte, delivery Delivery) {
				mu.Lock()
				received = append([]byte(nil), data...)
				mu.Unlock()
			},
		},
	})

	if err != nil {
		t.Fatalf("cannot open the server: %v", err)
	}

	defer server.Stop()

	var clientPeer *Peer

	client, err := Listen("127.0.0.1:0", Config{
		Handler: Handler{
			OnPeerConnected: func(peer *Peer) {
				clientPeer = peer
				close(clientUp)
			},
		},
	})

	if err != nil {
		t.Fatalf("cannot open the client: %v", err)
	}

	defer client.Stop()

	client.Connect(server.LocalAddr(), nil)

	select {
	case <-clientUp:
	case <-time.After(3 * time.Second):
		t.Fatal("the client did not finish the handshake")
	}

	payload := make([]byte, 5000)

	for i := range payload {
		payload[i] = byte(i % 251)
	}

	client.Send(clientPeer, payload, 0, ReliableOrdered)

	deadline := time.After(5 * time.Second)

	for {
		mu.Lock()
		done := len(received) == len(payload)
		mu.Unlock()

		if done {
			break
		}

		select {
		case <-deadline:
			mu.Lock()
			got := len(received)
			mu.Unlock()
			t.Fatalf("the fragmented message did not arrive in full: %d of %d bytes", got, len(payload))
		case <-time.After(20 * time.Millisecond):
		}
	}

	mu.Lock()
	defer mu.Unlock()

	if !bytes.Equal(received, payload) {
		t.Fatal("the reassembled message does not match")
	}
}
