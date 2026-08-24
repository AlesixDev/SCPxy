# Protocol notes

What SCPxy needs to know about the wire protocol to work, pieced together from the public LiteNetLib implementation (MIT) and from observing real traffic.

## Transport

SCP: Secret Laboratory runs Mirror over LiteNetLib over UDP. The packet format is stable across LiteNetLib 1.0.1.1 through 1.2.0, which is the relevant range — **but Northwood ships a modified build**, see [Northwood's additions](#northwoods-additions) below.

### Header

The first byte packs three fields:

| Bits | Field |
|---|---|
| 0–4 (`& 0x1F`) | `PacketProperty` |
| 5–6 (`& 0x60`) | `ConnectionNumber` |
| 7 (`& 0x80`) | fragmentation flag |

`PacketProperty` is an ordinal enum: `Unreliable=0`, `Channeled=1`, `Ack=2`, `Ping=3`, `Pong=4`, `ConnectRequest=5`, `ConnectAccept=6`, `Disconnect=7`, `UnconnectedMessage=8`, `MtuCheck=9`, `MtuOk=10`, `Broadcast=11`, `Merged=12`, `ShutdownOk=13`, `PeerNotFound=14`, `InvalidProtocol=15`, `NatMessage=16`, `Empty=17`.

> Current LiteNetLib `master` inserts `ReliableMerged` at index 2 and shifts the whole enum. SCP:SL does **not** use that numbering — see below.

Header sizes by type: `Channeled`, `Ack` and `ReliableMerged` = 4 bytes; `Ping` = 3; `Pong` = 11; `ConnectRequest` = 18; `ConnectAccept` = 15; `Disconnect` = 9; everything else = 1.

For packets with a channel header: sequence as little-endian `uint16` at offset 1, channel id at offset 3. If the fragmentation bit is set, `FragmentId`, `FragmentPart` and `FragmentsTotal` follow as `uint16` at offsets 4, 6 and 8.

Everything is little-endian except the port inside a serialized address.

### Channels

The channel id encodes two things: `channelId = channelNumber * 4 + deliveryMethod`, where `DeliveryMethod` is `ReliableUnordered=0`, `Sequenced=1`, `ReliableOrdered=2`, `ReliableSequenced=3`, `Unreliable=4`.

Sequences are modulo 32768 and comparisons use circular arithmetic with a half window (16384). Reliable channels use a 64-packet window, and `Ack` carries a bitmap of that window.

### ConnectRequest

```
offset  0       1 byte    property + connection number
offset  1       4 bytes   ProtocolId (13)
offset  5       8 bytes   ConnectionTime (.NET ticks, 100ns since year 1)
offset 13       4 bytes   sender's local PeerId
offset 17       1 byte    serialized address size (16 or 28)
offset 18       N bytes   recipient address, .NET SocketAddress format
offset 18+N     rest      connectionData  ← the PreAuth travels here
```

The serialized address follows .NET's `sockaddr` layout: family as a little-endian `uint16` (2 for IPv4, 23 for IPv6 — .NET enum values, not the operating system's), port as a **big-endian** `uint16`, then the address.

`ConnectAccept` echoes the same `ConnectionTime`, the `ConnectionNumber` and the remote `PeerId`.

## Northwood's additions

SCP:SL does not run stock LiteNetLib. Confirmed so far:

### `ReliableMerged` at property 18

Northwood implements the `ReliableMerged` optimisation — batching several small reliable messages into a single packet — but **appended it at the end of the enum, as property 18**, instead of renumbering. That keeps every other property compatible with the classic ordering, which is why handshakes, ping/pong and unreliable traffic work against a stock implementation while reliable traffic does not.

Layout: a normal 4-byte channel header, then a sequence of `uint16` length + payload entries:

```
12 00 00 02 | 33 00 | <51 bytes> | ...
^prop=18      ^len=51
   ^seq=0
         ^channel=2 (ReliableOrdered on channel 0)
```

It is acknowledged and sequenced exactly like a `Channeled` packet. On delivery, the payload is split and each entry becomes a `Channeled` packet on the same channel, delivered individually.

Sending plain `Channeled` packets is still accepted by the game, so SCPxy receives `ReliableMerged` but never produces it.

**If a future game update breaks the proxy, this is the first place to look.** Run with `log_level = "debug"` and `debug_relay = true` and watch for `rx REJECTED … prop=N` lines: an unknown property number means Northwood added another one.

## Data serialization

LiteNetLib's `NetDataWriter` uses little-endian primitives plus two composite formats that matter:

- **Strings**: `uint16` holding the UTF-8 byte length **plus one**, followed by the bytes. An empty string is a zero `uint16` with no bytes.
- **Length-prefixed byte arrays**: `int32` length followed by the bytes.

## PreAuth and IP passthrough

The client sends its PreAuth block as the `connectionData` of the `ConnectRequest`. It carries the client type, the game version, a challenge id, the UserID, an expiry, flags, a region and an ECDSA signature issued by Northwood.

The real server attributes the player's real IP instead of the socket source address when two conditions hold in its `config_gameplay.txt`:

```
enable_proxy_ip_passthrough: true
trusted_proxies_ip_addresses: <proxy IP>
```

The mechanism is to **append the real IP as one more LiteNetLib string at the end of the raw PreAuth**, touching nothing before it, and send the result as the `connectionData` of the connection the proxy opens towards the backend.

SCPxy never re-serializes the PreAuth: it copies the original bytes and writes the IP string after them. A format change in the PreAuth therefore cannot break the relay. It does parse the block independently to show the user and version on the dashboard, but a failure there does not affect the connection.

The field order the parser assumes is: `byte` client type, `byte` major, `byte` minor, `byte` revision, `bool` backward compatibility, `byte` backward revision, `string` challenge, `string` UserID, `int64` expiry, `byte` flags, `string` region, `byte[]` signature. **This is unverified against a live capture** — it currently fails to parse against the real client, which is why the dashboard falls back to showing the IP instead of the Steam id.

## The connection challenge

SCP:SL protects connections with a challenge. The flow is:

1. The client connects and sends its PreAuth.
2. The server **rejects the connection**, and the rejection payload carries the challenge.
3. The client reconnects with a signed response.
4. The server accepts and preauthenticates the player.

A proxy must therefore forward rejection payloads **verbatim** to the client, and must not treat a rejection as a backend failure — it is a normal protocol response.

## One upstream socket per player

LiteNetLib identifies peers by their source IP:port pair. If the proxy used a single socket for every connection to the same backend, all players would arrive from the same origin and the backend could not tell them apart.

Each session therefore opens its own UDP socket with an ephemeral port. The cost is one descriptor and one port per connected player, which is why large deployments need a raised `ulimit -n`. Sockets are leased per client address and reused across reconnects for a short window.

## MTU is pinned

Each hop negotiates its MTU independently, so a message that fits unfragmented on one hop may not fit on the other — and unreliable and sequenced traffic cannot be fragmented, so it would be silently lost.

SCPxy therefore **pins the MTU at 1024 on both hops** by declining MTU discovery: it never sends `MtuCheck` and never answers one with `MtuOk`. Both peers give up after four attempts and stay at the initial value, keeping the two hops symmetric.

## What SCPxy does not do

**It does not inspect or modify Mirror messages.** Once a session is established, payloads are relayed opaquely, preserving channel and delivery method.

This is deliberate and also a correctness requirement: LiteNetLib's reliable ordered channels deliver a continuous stream, and dropping a single message mid-stream corrupts it in ways that surface much later. So when the proxy hits a condition it cannot forward safely, **it closes the whole session** rather than dropping the message.

## Central listing

Listing information (name, MOTD, player count, version) is not published over a UDP query protocol: it is sent by HTTP POST to Northwood's API every few seconds. SCPxy does not implement this yet, so it does not appear in the in-game server browser.
