package litenetlib

import "time"

type Delivery byte

const (
	ReliableUnordered Delivery = 0
	Sequenced         Delivery = 1
	ReliableOrdered   Delivery = 2
	ReliableSequenced Delivery = 3
	Unreliable        Delivery = 4
)

func (d Delivery) String() string {
	switch d {
	case ReliableUnordered:
		return "ReliableUnordered"
	case Sequenced:
		return "Sequenced"
	case ReliableOrdered:
		return "ReliableOrdered"
	case ReliableSequenced:
		return "ReliableSequenced"
	case Unreliable:
		return "Unreliable"
	}

	return "Unknown"
}

const (
	protocolID = 13

	headerSize              = 1
	channeledHeaderSize     = 4
	fragmentHeaderSize      = 6
	fragmentedHeaderTotal   = channeledHeaderSize + fragmentHeaderSize
	maxSequence             = 32768
	halfMaxSequence         = maxSequence / 2
	channelTypeCount        = 4
	maxConnectionNumber     = 4
	defaultWindowSize       = 64
	maxUDPHeaderSize        = 68
	connectRequestHeader    = 18
	connectAcceptSize       = 15
	socketBufferSize        = 1 << 20
	mergeSizeThreshold      = 20
	ticksAtUnixEpoch        = 621355968000000000
	ticksPerMillisecond     = 10000
	defaultChannelsCount    = 64
	shutdownResendDelay     = 300 * time.Millisecond
	mtuCheckDelay           = 1000 * time.Millisecond
	maxMtuCheckAttempts     = 4
	defaultUpdateInterval   = 15 * time.Millisecond
	defaultPingInterval     = 1000 * time.Millisecond
	defaultDisconnectAfter  = 5000 * time.Millisecond
	defaultReconnectDelay   = 500 * time.Millisecond
	defaultConnectAttempts  = 10
	receiveBufferSize       = 2048
	incomingPacketQueueSize = 4096
	commandQueueSize        = 1024
	rejectPreviewBytes      = 12
	mergeHeaderSize         = 2
	addressFamilyIPv4       = 2
	addressFamilyIPv6       = 23
)

var possibleMtu = [...]int{
	1024,
	1232 - maxUDPHeaderSize,
	1460 - maxUDPHeaderSize,
	1472 - maxUDPHeaderSize,
	1492 - maxUDPHeaderSize,
	1500 - maxUDPHeaderSize,
}

var maxPacketSize = possibleMtu[len(possibleMtu)-1]

type DisconnectReason byte

const (
	ReasonConnectionFailed DisconnectReason = iota
	ReasonTimeout
	ReasonHostUnreachable
	ReasonNetworkUnreachable
	ReasonRemoteConnectionClose
	ReasonDisconnectPeerCalled
	ReasonConnectionRejected
	ReasonInvalidProtocol
	ReasonUnknownHost
	ReasonReconnect
	ReasonPeerNotFound
)

func (r DisconnectReason) String() string {
	switch r {
	case ReasonConnectionFailed:
		return "connection failed"
	case ReasonTimeout:
		return "timeout"
	case ReasonHostUnreachable:
		return "host unreachable"
	case ReasonNetworkUnreachable:
		return "network unreachable"
	case ReasonRemoteConnectionClose:
		return "closed by remote"
	case ReasonDisconnectPeerCalled:
		return "disconnected locally"
	case ReasonConnectionRejected:
		return "rejected"
	case ReasonInvalidProtocol:
		return "invalid protocol"
	case ReasonUnknownHost:
		return "unknown host"
	case ReasonReconnect:
		return "reconnect"
	case ReasonPeerNotFound:
		return "peer not found"
	}

	return "unknown"
}

func nowTicks() int64 {
	return time.Now().UTC().UnixNano()/100 + ticksAtUnixEpoch
}

func relativeSequence(number, expected int) int {
	return (number-expected+maxSequence+halfMaxSequence)%maxSequence - halfMaxSequence
}
