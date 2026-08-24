package litenetlib

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func buildReliableMerged(channelID byte, sequence uint16, parts [][]byte) []byte {
	out := make([]byte, channeledHeaderSize)
	out[0] = byte(propReliableMerged)
	binary.LittleEndian.PutUint16(out[1:], sequence)
	out[3] = channelID

	for _, part := range parts {
		size := make([]byte, mergeHeaderSize)
		binary.LittleEndian.PutUint16(size, uint16(len(part)))
		out = append(out, size...)
		out = append(out, part...)
	}

	return out
}

func TestReliableMergedHeaderMatchesCapture(t *testing.T) {
	captured := []byte{0x12, 0x00, 0x00, 0x02, 0x33, 0x00}
	pkt := &packet{data: append(captured, bytes.Repeat([]byte{0xAB}, 51)...)}

	if !pkt.verify() {
		t.Fatal("the captured packet is still rejected")
	}

	if pkt.property() != propReliableMerged {
		t.Fatalf("property = %d, expected %d", pkt.property(), propReliableMerged)
	}

	if pkt.sequence() != 0 {
		t.Fatalf("sequence = %d", pkt.sequence())
	}

	if pkt.channelID() != 2 {
		t.Fatalf("channelID = %d", pkt.channelID())
	}

	if got := propertyHeaderSize(propReliableMerged); got != channeledHeaderSize {
		t.Fatalf("headerSize = %d, expected %d", got, channeledHeaderSize)
	}

	if pkt.size() != 57 {
		t.Fatalf("size = %d, expected 57 as in the capture", pkt.size())
	}
}

func TestReliableMergedIsSplitIntoMessages(t *testing.T) {
	var delivered [][]byte

	mgr := &Manager{channelsCount: defaultChannelsCount}
	mgr.handler.OnReceive = func(peer *Peer, data []byte, channel byte, delivery Delivery) {
		if delivery != ReliableOrdered {
			t.Errorf("delivery = %s", delivery)
		}

		if channel != 0 {
			t.Errorf("channel = %d", channel)
		}

		delivered = append(delivered, append([]byte(nil), data...))
	}

	peer := &Peer{
		mgr:       mgr,
		channels:  make([]netChannel, int(defaultChannelsCount)*channelTypeCount),
		heldFrags: make(map[uint16]*incomingFragments),
	}

	channel := newReliableChannel(peer, true, 2)

	first := bytes.Repeat([]byte{0x11}, 20)
	second := bytes.Repeat([]byte{0x22}, 31)
	raw := buildReliableMerged(2, 0, [][]byte{first, second})

	if !channel.processPacket(&packet{data: raw}) {
		t.Fatal("processPacket rejected the ReliableMerged packet")
	}

	if len(delivered) != 2 {
		t.Fatalf("delivered %d messages, expected 2", len(delivered))
	}

	if !bytes.Equal(delivered[0], first) {
		t.Fatalf("first message = %v", delivered[0])
	}

	if !bytes.Equal(delivered[1], second) {
		t.Fatalf("second message = %v", delivered[1])
	}
}

func TestReliableMergedStopsOnCorruptLength(t *testing.T) {
	var delivered int

	mgr := &Manager{channelsCount: defaultChannelsCount}
	mgr.handler.OnReceive = func(peer *Peer, data []byte, channel byte, delivery Delivery) {
		delivered++
	}

	peer := &Peer{
		mgr:       mgr,
		channels:  make([]netChannel, int(defaultChannelsCount)*channelTypeCount),
		heldFrags: make(map[uint16]*incomingFragments),
	}

	channel := newReliableChannel(peer, true, 2)

	raw := buildReliableMerged(2, 0, [][]byte{bytes.Repeat([]byte{0x33}, 8)})
	binary.LittleEndian.PutUint16(raw[channeledHeaderSize:], 9000)

	channel.processPacket(&packet{data: raw})

	if delivered != 0 {
		t.Fatalf("delivered %d messages from a corrupt packet", delivered)
	}
}
