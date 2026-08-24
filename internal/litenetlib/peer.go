package litenetlib

import (
	"encoding/binary"
	"net"
	"time"
)

type connectionState byte

const (
	stateOutgoing connectionState = iota
	stateConnected
	stateShutdownRequested
	stateDisconnected
)

type incomingFragments struct {
	fragments []*packet
	received  int
	totalSize int
	channelID byte
}

type Peer struct {
	mgr  *Manager
	addr *net.UDPAddr
	key  string

	id       int
	remoteID int
	state    connectionState

	connectTime int64
	connectNum  byte

	channels    []netChannel
	sendQueue   []netChannel
	unreliable  []*packet
	heldFrags   map[uint16]*incomingFragments
	fragmentID  uint16
	mtu         int
	mtuIdx      int
	finishMtu   bool
	mtuTimer    time.Duration
	mtuAttempts int

	mergeData  *packet
	mergePos   int
	mergeCount int

	pingPacket           *packet
	pongPacket           *packet
	connectRequestPacket *packet
	connectAcceptPacket  *packet
	shutdownPacket       *packet

	pingSendTimer   time.Duration
	rttResetTimer   time.Duration
	connectTimer    time.Duration
	shutdownTimer   time.Duration
	connectAttempts int
	sinceLastPacket time.Duration

	rtt         int
	avgRtt      int
	rttCount    int
	resendDelay float64
	pingStart   time.Time
	pingRunning bool

	Tag any
}

func (p *Peer) Addr() *net.UDPAddr {
	return p.addr
}

func (p *Peer) ID() int {
	return p.id
}

func (p *Peer) RoundTripTime() int {
	return p.avgRtt
}

func (p *Peer) Connected() bool {
	return p.state == stateConnected
}

func newPeer(mgr *Manager, addr *net.UDPAddr, id int) *Peer {
	p := &Peer{
		mgr:         mgr,
		addr:        addr,
		key:         addr.String(),
		id:          id,
		state:       stateConnected,
		channels:    make([]netChannel, int(mgr.channelsCount)*channelTypeCount),
		heldFrags:   make(map[uint16]*incomingFragments),
		resendDelay: 27.0,
		mergeData:   newPacket(propMerged, maxPacketSize),
		pongPacket:  newPacket(propPong, 0),
		pingPacket:  newPacket(propPing, 0),
	}

	p.pingPacket.setSequence(1)
	p.resetMtu()

	return p
}

func newOutgoingPeer(mgr *Manager, addr *net.UDPAddr, id int, connectNum byte, connectData []byte) *Peer {
	p := newPeer(mgr, addr, id)
	p.connectTime = nowTicks()
	p.state = stateOutgoing
	p.setConnectionNum(connectNum)

	target := serializeSocketAddress(addr)
	p.connectRequestPacket = newPacket(propConnectRequest, len(connectData)+len(target))
	binary.LittleEndian.PutUint32(p.connectRequestPacket.data[1:], uint32(protocolID))
	binary.LittleEndian.PutUint64(p.connectRequestPacket.data[5:], uint64(p.connectTime))
	binary.LittleEndian.PutUint32(p.connectRequestPacket.data[13:], uint32(id))
	p.connectRequestPacket.data[connectRequestHeader-1] = byte(len(target))
	copy(p.connectRequestPacket.data[connectRequestHeader:], target)
	copy(p.connectRequestPacket.data[connectRequestHeader+len(target):], connectData)
	p.connectRequestPacket.setConnectionNumber(connectNum)

	mgr.sendRaw(p.connectRequestPacket, addr)

	return p
}

func newAcceptedPeer(mgr *Manager, req *connectRequest, id int) *Peer {
	p := newPeer(mgr, req.addr, id)
	p.connectTime = req.connectionTime
	p.setConnectionNum(req.connectionNumber)
	p.remoteID = req.peerID

	p.connectAcceptPacket = newPacket(propConnectAccept, 0)
	binary.LittleEndian.PutUint64(p.connectAcceptPacket.data[1:], uint64(p.connectTime))
	p.connectAcceptPacket.data[9] = p.connectNum
	binary.LittleEndian.PutUint32(p.connectAcceptPacket.data[11:], uint32(id))

	p.state = stateConnected
	mgr.sendRaw(p.connectAcceptPacket, p.addr)

	return p
}

func (p *Peer) setConnectionNum(v byte) {
	p.connectNum = v
	p.mergeData.setConnectionNumber(v)
	p.pingPacket.setConnectionNumber(v)
	p.pongPacket.setConnectionNumber(v)
}

func (p *Peer) resetMtu() {
	p.finishMtu = false
	p.setMtu(0)
}

func (p *Peer) setMtu(idx int) {
	p.mtuIdx = idx
	p.mtu = possibleMtu[idx]
}

func (p *Peer) createChannel(idx byte) netChannel {
	if int(idx) >= len(p.channels) {
		return nil
	}

	if p.channels[idx] != nil {
		return p.channels[idx]
	}

	var ch netChannel

	switch Delivery(int(idx) % channelTypeCount) {
	case ReliableUnordered:
		ch = newReliableChannel(p, false, idx)
	case Sequenced:
		ch = newSequencedChannel(p, false, idx)
	case ReliableOrdered:
		ch = newReliableChannel(p, true, idx)
	case ReliableSequenced:
		ch = newSequencedChannel(p, true, idx)
	}

	p.channels[idx] = ch

	return ch
}

func (p *Peer) enqueueChannel(ch netChannel) {
	if ch.base().inQueue {
		return
	}

	ch.base().inQueue = true
	p.sendQueue = append(p.sendQueue, ch)
}

func (p *Peer) MaxSinglePacketSize(delivery Delivery) int {
	if delivery == Unreliable {
		return p.mtu - propertyHeaderSize(propUnreliable)
	}

	return p.mtu - propertyHeaderSize(propChanneled)
}

func (p *Peer) send(data []byte, channelNumber byte, delivery Delivery) {
	if p.state != stateConnected {
		return
	}

	if delivery != Unreliable && int(channelNumber)*channelTypeCount+int(delivery) >= len(p.channels) {
		return
	}

	prop := propChanneled
	var ch netChannel

	if delivery == Unreliable {
		prop = propUnreliable
	}

	if delivery != Unreliable {
		ch = p.createChannel(channelNumber*channelTypeCount + byte(delivery))
	}

	hdr := propertyHeaderSize(prop)
	mtu := p.mtu

	if len(data)+hdr <= mtu {
		pkt := newPacket(prop, len(data))
		copy(pkt.data[hdr:], data)

		if ch == nil {
			p.unreliable = append(p.unreliable, pkt)
			return
		}

		ch.base().enqueue(pkt)
		p.enqueueChannel(ch)

		return
	}

	if delivery != ReliableOrdered && delivery != ReliableUnordered {
		return
	}

	packetDataSize := mtu - hdr - fragmentHeaderSize
	totalPackets := len(data) / packetDataSize

	if len(data)%packetDataSize != 0 {
		totalPackets++
	}

	if totalPackets > 65535 {
		return
	}

	p.fragmentID++
	currentFragmentID := p.fragmentID
	remaining := len(data)

	for part := 0; part < totalPackets; part++ {
		sendLength := packetDataSize

		if remaining < packetDataSize {
			sendLength = remaining
		}

		pkt := newRawPacket(hdr + sendLength + fragmentHeaderSize)
		pkt.setProperty(prop)
		pkt.setFragmentID(currentFragmentID)
		pkt.setFragmentPart(uint16(part))
		pkt.setFragmentsTotal(uint16(totalPackets))
		pkt.markFragmented()
		copy(pkt.data[fragmentedHeaderTotal:], data[part*packetDataSize:part*packetDataSize+sendLength])
		ch.base().enqueue(pkt)
		remaining -= sendLength
	}

	p.enqueueChannel(ch)
}

func (p *Peer) processDisconnect(pkt *packet) bool {
	if p.state != stateConnected && p.state != stateOutgoing {
		return false
	}

	if pkt.size() < 9 {
		return false
	}

	if int64(binary.LittleEndian.Uint64(pkt.data[1:])) != p.connectTime {
		return false
	}

	return pkt.connectionNumber() == p.connectNum
}

func (p *Peer) shutdown(data []byte, force bool) bool {
	if p.state == stateDisconnected || p.state == stateShutdownRequested {
		return false
	}

	wasConnected := p.state == stateConnected

	if force {
		p.state = stateDisconnected
		return wasConnected
	}

	p.sinceLastPacket = 0
	p.shutdownPacket = newPacket(propDisconnect, len(data))
	p.shutdownPacket.setConnectionNumber(p.connectNum)
	binary.LittleEndian.PutUint64(p.shutdownPacket.data[1:], uint64(p.connectTime))

	if len(data) > 0 && p.shutdownPacket.size() < p.mtu {
		copy(p.shutdownPacket.data[9:], data)
	}

	p.state = stateShutdownRequested
	p.mgr.sendRaw(p.shutdownPacket, p.addr)

	return wasConnected
}

func (p *Peer) updateRoundTripTime(rtt int) {
	p.rtt += rtt
	p.rttCount++
	p.avgRtt = p.rtt / p.rttCount
	p.resendDelay = 25.0 + float64(p.avgRtt)*2.1
}

func (p *Peer) addReliablePacket(method Delivery, pkt *packet) {
	if !pkt.isFragmented() {
		p.deliver(pkt, method, pkt.channelID()/channelTypeCount, channeledHeaderSize)
		return
	}

	fragID := pkt.fragmentID()
	held, ok := p.heldFrags[fragID]

	if !ok {
		held = &incomingFragments{
			fragments: make([]*packet, pkt.fragmentsTotal()),
			channelID: pkt.channelID(),
		}
		p.heldFrags[fragID] = held
	}

	if int(pkt.fragmentPart()) >= len(held.fragments) || held.fragments[pkt.fragmentPart()] != nil || pkt.channelID() != held.channelID {
		return
	}

	held.fragments[pkt.fragmentPart()] = pkt
	held.received++
	held.totalSize += pkt.size() - fragmentedHeaderTotal

	if held.received != len(held.fragments) {
		return
	}

	result := newRawPacket(held.totalSize)
	pos := 0

	for i := range held.fragments {
		fragment := held.fragments[i]
		written := fragment.size() - fragmentedHeaderTotal

		if pos+written > len(result.data) {
			delete(p.heldFrags, fragID)
			return
		}

		copy(result.data[pos:], fragment.data[fragmentedHeaderTotal:])
		pos += written
		held.fragments[i] = nil
	}

	delete(p.heldFrags, fragID)
	p.deliver(result, method, held.channelID/channelTypeCount, 0)
}

func (p *Peer) deliver(pkt *packet, method Delivery, channel byte, offset int) {
	p.mgr.dispatchReceive(p, pkt.data[offset:], channel, method)
}

func (p *Peer) processMtuPacket(pkt *packet) {
	if pkt.size() < possibleMtu[0] {
		return
	}

	receivedMtu := int(int32(binary.LittleEndian.Uint32(pkt.data[1:])))
	endCheck := int(int32(binary.LittleEndian.Uint32(pkt.data[pkt.size()-4:])))

	if receivedMtu != pkt.size() || receivedMtu != endCheck || receivedMtu > maxPacketSize {
		return
	}

	if p.mgr.disableMtu {
		return
	}

	if pkt.property() == propMtuCheck {
		p.mtuAttempts = 0
		pkt.setProperty(propMtuOk)
		p.mgr.sendRaw(pkt, p.addr)

		return
	}

	if receivedMtu <= p.mtu || p.finishMtu {
		return
	}

	if p.mtuIdx+1 >= len(possibleMtu) || receivedMtu != possibleMtu[p.mtuIdx+1] {
		return
	}

	p.setMtu(p.mtuIdx + 1)

	if p.mtuIdx == len(possibleMtu)-1 {
		p.finishMtu = true
	}
}

func (p *Peer) updateMtuLogic(delta time.Duration) {
	if p.mgr.disableMtu {
		p.finishMtu = true
		return
	}

	if p.finishMtu {
		return
	}

	p.mtuTimer += delta

	if p.mtuTimer < mtuCheckDelay {
		return
	}

	p.mtuTimer = 0
	p.mtuAttempts++

	if p.mtuAttempts >= maxMtuCheckAttempts {
		p.finishMtu = true
		return
	}

	if p.mtuIdx >= len(possibleMtu)-1 {
		return
	}

	newMtu := possibleMtu[p.mtuIdx+1]
	pkt := newRawPacket(newMtu)
	pkt.setProperty(propMtuCheck)
	binary.LittleEndian.PutUint32(pkt.data[1:], uint32(newMtu))
	binary.LittleEndian.PutUint32(pkt.data[pkt.size()-4:], uint32(newMtu))

	if p.mgr.sendRaw(pkt, p.addr) <= 0 {
		p.finishMtu = true
	}
}

func (p *Peer) processConnectAccept(pkt *packet) bool {
	if p.state != stateOutgoing {
		return false
	}

	if pkt.size() != connectAcceptSize {
		return false
	}

	if int64(binary.LittleEndian.Uint64(pkt.data[1:])) != p.connectTime {
		return false
	}

	connectionNumber := pkt.data[9]

	if connectionNumber >= maxConnectionNumber {
		return false
	}

	peerID := int(int32(binary.LittleEndian.Uint32(pkt.data[11:])))

	if peerID < 0 {
		return false
	}

	p.setConnectionNum(connectionNumber)
	p.remoteID = peerID
	p.sinceLastPacket = 0
	p.state = stateConnected

	return true
}

func (p *Peer) processPacket(pkt *packet) {
	if p.state == stateOutgoing || p.state == stateDisconnected {
		return
	}

	if pkt.property() == propShutdownOk {
		if p.state == stateShutdownRequested {
			p.state = stateDisconnected
		}

		return
	}

	if pkt.connectionNumber() != p.connectNum {
		return
	}

	p.sinceLastPacket = 0

	switch pkt.property() {
	case propMerged:
		pos := headerSize

		for pos+2 <= pkt.size() {
			size := int(binary.LittleEndian.Uint16(pkt.data[pos:]))
			pos += 2

			if pkt.size()-pos < size {
				break
			}

			merged := newRawPacket(size)
			copy(merged.data, pkt.data[pos:pos+size])

			if !merged.verify() {
				break
			}

			pos += size
			p.processPacket(merged)
		}
	case propPing:
		if relativeSequence(int(pkt.sequence()), int(p.pongPacket.sequence())) > 0 {
			binary.LittleEndian.PutUint64(p.pongPacket.data[3:], uint64(nowTicks()))
			p.pongPacket.setSequence(pkt.sequence())
			p.mgr.sendRaw(p.pongPacket, p.addr)
		}
	case propPong:
		if pkt.sequence() == p.pingPacket.sequence() && p.pingRunning {
			p.pingRunning = false
			p.updateRoundTripTime(int(time.Since(p.pingStart).Milliseconds()))
		}
	case propAck, propChanneled, propReliableMerged:
		if int(pkt.channelID()) >= len(p.channels) {
			return
		}

		ch := p.channels[pkt.channelID()]

		if ch == nil && pkt.property() != propAck {
			ch = p.createChannel(pkt.channelID())
		}

		if ch != nil {
			ch.processPacket(pkt)
		}
	case propUnreliable:
		p.deliver(pkt, Unreliable, 0, headerSize)
	case propMtuCheck, propMtuOk:
		p.processMtuPacket(pkt)
	}
}

func (p *Peer) sendMerged() {
	if p.mergeCount == 0 {
		return
	}

	if p.mergeCount > 1 {
		p.mgr.sendRawBytes(p.mergeData.data[:headerSize+p.mergePos], p.addr)
	}

	if p.mergeCount == 1 {
		p.mgr.sendRawBytes(p.mergeData.data[headerSize+2:headerSize+p.mergePos], p.addr)
	}

	p.mergePos = 0
	p.mergeCount = 0
}

func (p *Peer) sendUserData(pkt *packet) {
	pkt.setConnectionNumber(p.connectNum)
	mergedSize := headerSize + pkt.size() + 2

	if mergedSize+mergeSizeThreshold >= p.mtu {
		p.mgr.sendRaw(pkt, p.addr)
		return
	}

	if p.mergePos+mergedSize > p.mtu {
		p.sendMerged()
	}

	binary.LittleEndian.PutUint16(p.mergeData.data[p.mergePos+headerSize:], uint16(pkt.size()))
	copy(p.mergeData.data[p.mergePos+headerSize+2:], pkt.data)
	p.mergePos += pkt.size() + 2
	p.mergeCount++
}

func (p *Peer) update(delta time.Duration) {
	p.sinceLastPacket += delta

	switch p.state {
	case stateConnected:
		if p.sinceLastPacket > p.mgr.disconnectTimeout {
			p.mgr.disconnectPeerForce(p, ReasonTimeout, nil)
			return
		}
	case stateShutdownRequested:
		if p.sinceLastPacket > p.mgr.disconnectTimeout {
			p.state = stateDisconnected
			return
		}

		p.shutdownTimer += delta

		if p.shutdownTimer >= shutdownResendDelay {
			p.shutdownTimer = 0
			p.mgr.sendRaw(p.shutdownPacket, p.addr)
		}

		return
	case stateOutgoing:
		p.connectTimer += delta

		if p.connectTimer <= p.mgr.reconnectDelay {
			return
		}

		p.connectTimer = 0
		p.connectAttempts++

		if p.connectAttempts > p.mgr.maxConnectAttempts {
			p.mgr.disconnectPeerForce(p, ReasonConnectionFailed, nil)
			return
		}

		p.mgr.sendRaw(p.connectRequestPacket, p.addr)

		return
	case stateDisconnected:
		return
	}

	p.pingSendTimer += delta

	if p.pingSendTimer >= p.mgr.pingInterval {
		p.pingSendTimer = 0
		p.pingPacket.setSequence(p.pingPacket.sequence() + 1)

		if p.pingRunning {
			p.updateRoundTripTime(int(time.Since(p.pingStart).Milliseconds()))
		}

		p.pingStart = time.Now()
		p.pingRunning = true
		p.mgr.sendRaw(p.pingPacket, p.addr)
	}

	p.rttResetTimer += delta

	if p.rttResetTimer >= p.mgr.pingInterval*3 {
		p.rttResetTimer = 0
		p.rtt = p.avgRtt
		p.rttCount = 1
	}

	p.updateMtuLogic(delta)

	pending := len(p.sendQueue)

	for pending > 0 {
		pending--
		ch := p.sendQueue[0]
		p.sendQueue = p.sendQueue[1:]

		if ch.sendNextPackets() {
			p.sendQueue = append(p.sendQueue, ch)
			continue
		}

		ch.base().inQueue = false
	}

	for _, pkt := range p.unreliable {
		p.sendUserData(pkt)
	}

	p.unreliable = p.unreliable[:0]
	p.sendMerged()
}

func serializeSocketAddress(addr *net.UDPAddr) []byte {
	if ip4 := addr.IP.To4(); ip4 != nil {
		out := make([]byte, 16)
		binary.LittleEndian.PutUint16(out[0:], addressFamilyIPv4)
		binary.BigEndian.PutUint16(out[2:], uint16(addr.Port))
		copy(out[4:], ip4)

		return out
	}

	out := make([]byte, 28)
	binary.LittleEndian.PutUint16(out[0:], addressFamilyIPv6)
	binary.BigEndian.PutUint16(out[2:], uint16(addr.Port))
	copy(out[8:], addr.IP.To16())

	return out
}

func (p *Peer) Mtu() int {
	return p.mtu
}
