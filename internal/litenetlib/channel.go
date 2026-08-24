package litenetlib

import "encoding/binary"

const bitsInByte = 8

type netChannel interface {
	base() *baseChannel
	processPacket(p *packet) bool
	sendNextPackets() bool
}

type baseChannel struct {
	peer     *Peer
	outgoing []*packet
	inQueue  bool
}

func (b *baseChannel) base() *baseChannel {
	return b
}

func (b *baseChannel) enqueue(p *packet) {
	b.outgoing = append(b.outgoing, p)
}

func (b *baseChannel) dequeue() *packet {
	if len(b.outgoing) == 0 {
		return nil
	}

	p := b.outgoing[0]
	b.outgoing[0] = nil
	b.outgoing = b.outgoing[1:]

	return p
}

type pendingPacket struct {
	pkt       *packet
	timeStamp int64
	isSent    bool
}

func (pp *pendingPacket) trySend(currentTime int64, peer *Peer) bool {
	if pp.pkt == nil {
		return false
	}

	if pp.isSent {
		resendDelay := int64(peer.resendDelay * ticksPerMillisecond)

		if currentTime-pp.timeStamp < resendDelay {
			return true
		}
	}

	pp.timeStamp = currentTime
	pp.isSent = true
	peer.sendUserData(pp.pkt)

	return true
}

func (pp *pendingPacket) clear() bool {
	if pp.pkt == nil {
		return false
	}

	pp.pkt = nil

	return true
}

type reliableChannel struct {
	baseChannel

	id              byte
	ordered         bool
	delivery        Delivery
	windowSize      int
	outgoingAcks    *packet
	pendingPackets  []pendingPacket
	receivedPackets []*packet
	earlyReceived   []bool
	localSequence   int
	remoteSequence  int
	localWindow     int
	remoteWindow    int
	mustSendAcks    bool
}

func newReliableChannel(peer *Peer, ordered bool, id byte) *reliableChannel {
	c := &reliableChannel{
		baseChannel:    baseChannel{peer: peer},
		id:             id,
		ordered:        ordered,
		windowSize:     defaultWindowSize,
		pendingPackets: make([]pendingPacket, defaultWindowSize),
	}

	if ordered {
		c.delivery = ReliableOrdered
		c.receivedPackets = make([]*packet, defaultWindowSize)
	}

	if !ordered {
		c.delivery = ReliableUnordered
		c.earlyReceived = make([]bool, defaultWindowSize)
	}

	c.outgoingAcks = newPacket(propAck, (defaultWindowSize-1)/bitsInByte+2)
	c.outgoingAcks.setChannelID(id)

	return c
}

func (c *reliableChannel) processAck(p *packet) {
	if p.size() != c.outgoingAcks.size() {
		return
	}

	ackWindowStart := int(p.sequence())
	windowRel := relativeSequence(c.localWindow, ackWindowStart)

	if ackWindowStart >= maxSequence || windowRel < 0 {
		return
	}

	if windowRel >= c.windowSize {
		return
	}

	for pendingSeq := c.localWindow; pendingSeq != c.localSequence; pendingSeq = (pendingSeq + 1) % maxSequence {
		if relativeSequence(pendingSeq, ackWindowStart) >= c.windowSize {
			break
		}

		idx := pendingSeq % c.windowSize
		currentByte := channeledHeaderSize + idx/bitsInByte
		currentBit := idx % bitsInByte

		if c.outgoingAcks == nil {
			return
		}

		if p.data[currentByte]&(1<<currentBit) == 0 {
			continue
		}

		if pendingSeq == c.localWindow {
			c.localWindow = (c.localWindow + 1) % maxSequence
		}

		c.pendingPackets[idx].clear()
	}
}

func (c *reliableChannel) sendNextPackets() bool {
	if c.mustSendAcks {
		c.mustSendAcks = false
		c.peer.sendUserData(c.outgoingAcks)
	}

	currentTime := nowTicks()
	hasPending := false

	for len(c.outgoing) > 0 {
		if relativeSequence(c.localSequence, c.localWindow) >= c.windowSize {
			break
		}

		p := c.dequeue()
		p.setSequence(uint16(c.localSequence))
		p.setChannelID(c.id)
		c.pendingPackets[c.localSequence%c.windowSize] = pendingPacket{pkt: p}
		c.localSequence = (c.localSequence + 1) % maxSequence
	}

	for pendingSeq := c.localWindow; pendingSeq != c.localSequence; pendingSeq = (pendingSeq + 1) % maxSequence {
		if c.pendingPackets[pendingSeq%c.windowSize].trySend(currentTime, c.peer) {
			hasPending = true
		}
	}

	return hasPending || c.mustSendAcks || len(c.outgoing) > 0
}

func (c *reliableChannel) processPacket(p *packet) bool {
	if p.property() == propAck {
		c.processAck(p)
		return false
	}

	seq := int(p.sequence())

	if seq >= maxSequence {
		c.peer.mgr.tracef("channel %d: sequence %d out of range", c.id, seq)
		return false
	}

	relate := relativeSequence(seq, c.remoteWindow)
	relateSeq := relativeSequence(seq, c.remoteSequence)

	if relateSeq > c.windowSize {
		c.peer.mgr.tracef("channel %d: dropped seq=%d relateSeq=%d remoteSeq=%d", c.id, seq, relateSeq, c.remoteSequence)
		return false
	}

	if relate < 0 || relate >= c.windowSize*2 {
		c.peer.mgr.tracef("channel %d: dropped seq=%d relate=%d windowStart=%d", c.id, seq, relate, c.remoteWindow)
		return false
	}

	if relate >= c.windowSize {
		newWindowStart := (c.remoteWindow + relate - c.windowSize + 1) % maxSequence
		c.outgoingAcks.setSequence(uint16(newWindowStart))

		for c.remoteWindow != newWindowStart {
			idx := c.remoteWindow % c.windowSize
			c.outgoingAcks.data[channeledHeaderSize+idx/bitsInByte] &^= byte(1 << (idx % bitsInByte))
			c.remoteWindow = (c.remoteWindow + 1) % maxSequence
		}
	}

	c.mustSendAcks = true

	ackIdx := seq % c.windowSize
	ackByte := channeledHeaderSize + ackIdx/bitsInByte
	ackBit := ackIdx % bitsInByte

	if c.outgoingAcks.data[ackByte]&(1<<ackBit) != 0 {
		c.peer.mgr.tracef("channel %d: duplicate seq=%d", c.id, seq)
		c.peer.enqueueChannel(c)

		return false
	}

	c.outgoingAcks.data[ackByte] |= byte(1 << ackBit)

	c.peer.enqueueChannel(c)

	if seq != c.remoteSequence {
		c.peer.mgr.tracef("channel %d: holding seq=%d, waiting for %d", c.id, seq, c.remoteSequence)
	}

	if seq == c.remoteSequence {
		c.processIncoming(p)
		c.remoteSequence = (c.remoteSequence + 1) % maxSequence

		if c.ordered {
			for {
				held := c.receivedPackets[c.remoteSequence%c.windowSize]

				if held == nil {
					break
				}

				c.receivedPackets[c.remoteSequence%c.windowSize] = nil
				c.processIncoming(held)
				c.remoteSequence = (c.remoteSequence + 1) % maxSequence
			}

			return true
		}

		for c.earlyReceived[c.remoteSequence%c.windowSize] {
			c.earlyReceived[c.remoteSequence%c.windowSize] = false
			c.remoteSequence = (c.remoteSequence + 1) % maxSequence
		}

		return true
	}

	if c.ordered {
		c.receivedPackets[ackIdx] = p
		return true
	}

	c.earlyReceived[ackIdx] = true
	c.processIncoming(p)

	return true
}

func (c *reliableChannel) processIncoming(p *packet) {
	if p.property() != propReliableMerged {
		c.peer.addReliablePacket(c.delivery, p)
		return
	}

	pos := channeledHeaderSize

	for pos+mergeHeaderSize <= p.size() {
		size := int(binary.LittleEndian.Uint16(p.data[pos:]))
		pos += mergeHeaderSize

		if size == 0 || pos+size > p.size() {
			c.peer.mgr.tracef("channel %d: corrupt ReliableMerged at offset %d", c.id, pos)
			return
		}

		inner := newPacket(propChanneled, size)
		inner.setChannelID(p.channelID())
		copy(inner.data[channeledHeaderSize:], p.data[pos:pos+size])
		pos += size

		c.peer.addReliablePacket(c.delivery, inner)
	}
}

type sequencedChannel struct {
	baseChannel

	id            byte
	reliable      bool
	localSequence int
	remoteSeq     uint16
	lastPacket    *packet
	lastSendTime  int64
	ackPacket     *packet
	mustSendAck   bool
}

func newSequencedChannel(peer *Peer, reliable bool, id byte) *sequencedChannel {
	c := &sequencedChannel{
		baseChannel: baseChannel{peer: peer},
		id:          id,
		reliable:    reliable,
	}

	if reliable {
		c.ackPacket = newPacket(propAck, 0)
		c.ackPacket.setChannelID(id)
	}

	return c
}

func (c *sequencedChannel) sendNextPackets() bool {
	if c.reliable && len(c.outgoing) == 0 {
		currentTime := nowTicks()

		if c.lastPacket != nil && currentTime-c.lastSendTime >= int64(c.peer.resendDelay*ticksPerMillisecond) {
			c.lastSendTime = currentTime
			c.peer.sendUserData(c.lastPacket)
		}
	}

	if !c.reliable || len(c.outgoing) > 0 {
		for len(c.outgoing) > 0 {
			p := c.dequeue()
			c.localSequence = (c.localSequence + 1) % maxSequence
			p.setSequence(uint16(c.localSequence))
			p.setChannelID(c.id)
			c.peer.sendUserData(p)

			if c.reliable && len(c.outgoing) == 0 {
				c.lastSendTime = nowTicks()
				c.lastPacket = p
			}
		}
	}

	if c.reliable && c.mustSendAck {
		c.mustSendAck = false
		c.ackPacket.setSequence(c.remoteSeq)
		c.peer.sendUserData(c.ackPacket)
	}

	return c.lastPacket != nil
}

func (c *sequencedChannel) processPacket(p *packet) bool {
	if p.isFragmented() {
		return false
	}

	if p.property() == propAck {
		if c.reliable && c.lastPacket != nil && p.sequence() == c.lastPacket.sequence() {
			c.lastPacket = nil
		}

		return false
	}

	relative := relativeSequence(int(p.sequence()), int(c.remoteSeq))
	processed := false

	if int(p.sequence()) < maxSequence && relative > 0 {
		c.remoteSeq = p.sequence()

		delivery := Sequenced

		if c.reliable {
			delivery = ReliableSequenced
		}

		c.peer.deliver(p, delivery, p.channelID()/channelTypeCount, channeledHeaderSize)
		processed = true
	}

	if c.reliable {
		c.mustSendAck = true
		c.peer.enqueueChannel(c)
	}

	return processed
}
