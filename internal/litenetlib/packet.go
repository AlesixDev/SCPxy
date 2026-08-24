package litenetlib

import "encoding/binary"

type property byte

const (
	propUnreliable property = iota
	propChanneled
	propAck
	propPing
	propPong
	propConnectRequest
	propConnectAccept
	propDisconnect
	propUnconnectedMessage
	propMtuCheck
	propMtuOk
	propBroadcast
	propMerged
	propShutdownOk
	propPeerNotFound
	propInvalidProtocol
	propNatMessage
	propEmpty
	propReliableMerged
	propTotal
)

var headerSizes = buildHeaderSizes()

func buildHeaderSizes() [propTotal]int {
	var sizes [propTotal]int

	for i := range sizes {
		switch property(i) {
		case propChanneled, propAck, propReliableMerged:
			sizes[i] = channeledHeaderSize
		case propPing:
			sizes[i] = headerSize + 2
		case propConnectRequest:
			sizes[i] = connectRequestHeader
		case propConnectAccept:
			sizes[i] = connectAcceptSize
		case propDisconnect:
			sizes[i] = headerSize + 8
		case propPong:
			sizes[i] = headerSize + 10
		default:
			sizes[i] = headerSize
		}
	}

	return sizes
}

func propertyHeaderSize(p property) int {
	if int(p) >= len(headerSizes) {
		return headerSize
	}

	return headerSizes[p]
}

type packet struct {
	data []byte
}

func newPacket(p property, payload int) *packet {
	pkt := &packet{data: make([]byte, propertyHeaderSize(p)+payload)}
	pkt.setProperty(p)

	return pkt
}

func newRawPacket(size int) *packet {
	return &packet{data: make([]byte, size)}
}

func (p *packet) size() int {
	return len(p.data)
}

func (p *packet) property() property {
	return property(p.data[0] & 0x1F)
}

func (p *packet) setProperty(v property) {
	p.data[0] = (p.data[0] & 0xE0) | byte(v)
}

func (p *packet) connectionNumber() byte {
	return (p.data[0] & 0x60) >> 5
}

func (p *packet) setConnectionNumber(v byte) {
	p.data[0] = (p.data[0] & 0x9F) | (v << 5)
}

func (p *packet) isFragmented() bool {
	return p.data[0]&0x80 != 0
}

func (p *packet) markFragmented() {
	p.data[0] |= 0x80
}

func (p *packet) sequence() uint16 {
	return binary.LittleEndian.Uint16(p.data[1:])
}

func (p *packet) setSequence(v uint16) {
	binary.LittleEndian.PutUint16(p.data[1:], v)
}

func (p *packet) channelID() byte {
	return p.data[3]
}

func (p *packet) setChannelID(v byte) {
	p.data[3] = v
}

func (p *packet) fragmentID() uint16 {
	return binary.LittleEndian.Uint16(p.data[4:])
}

func (p *packet) setFragmentID(v uint16) {
	binary.LittleEndian.PutUint16(p.data[4:], v)
}

func (p *packet) fragmentPart() uint16 {
	return binary.LittleEndian.Uint16(p.data[6:])
}

func (p *packet) setFragmentPart(v uint16) {
	binary.LittleEndian.PutUint16(p.data[6:], v)
}

func (p *packet) fragmentsTotal() uint16 {
	return binary.LittleEndian.Uint16(p.data[8:])
}

func (p *packet) setFragmentsTotal(v uint16) {
	binary.LittleEndian.PutUint16(p.data[8:], v)
}

func (p *packet) verify() bool {
	if len(p.data) < 1 {
		return false
	}

	prop := p.data[0] & 0x1F

	if int(prop) >= int(propTotal) {
		return false
	}

	size := headerSizes[prop]

	if len(p.data) < size {
		return false
	}

	if p.isFragmented() && len(p.data) < size+fragmentHeaderSize {
		return false
	}

	return true
}

func (p *packet) clone() *packet {
	out := make([]byte, len(p.data))
	copy(out, p.data)

	return &packet{data: out}
}

func (p property) String() string {
	names := [...]string{
		"Unreliable", "Channeled", "Ack", "Ping", "Pong",
		"ConnectRequest", "ConnectAccept", "Disconnect", "UnconnectedMessage",
		"MtuCheck", "MtuOk", "Broadcast", "Merged", "ShutdownOk",
		"PeerNotFound", "InvalidProtocol", "NatMessage", "Empty",
		"ReliableMerged",
	}

	if int(p) >= len(names) {
		return "Unknown"
	}

	return names[p]
}
