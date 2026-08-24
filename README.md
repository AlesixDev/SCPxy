<div align="center">
  <h1>SCPxy</h1>
  <p><b>A lightweight, high-performance proxy for SCP: Secret Laboratory servers.</b></p>
  <p>Relay players to your real server, keep their IP intact through Northwood's passthrough, and rate-limit abuse before it ever touches the game.</p>
  <sub>Go · LiteNetLib · zero runtime dependencies · MIT licensed</sub>
</div>

## Features

**Transport**
- Own implementation of the LiteNetLib protocol — handshake, reliable channels, fragmentation, merged packets
- One upstream UDP socket per player, so the backend tells peers apart correctly
- MTU pinning and lossless ordered relay, verified against the real game client

**Passthrough**
- The player's real IP appended to the PreAuth, no plugin or patch on the backend
- The PreAuth block forwarded **raw, byte for byte** — game updates do not break the relay
- Works over loopback or across machines

**Security**
- Per-IP connection rate limiting **before** any signature is touched
- Configurable burst, block duration and concurrent sessions per IP
- Proxy-level block list, manageable at runtime
- Maintenance mode to stop accepting new connections

**Operation**
- Terminal dashboard with a built-in admin console
- Headless mode for systemd and Docker
- Multiple backends with automatic fallback by priority
- `scpxy check` validates the configuration before you start anything
- Graceful shutdown: stops accepting, notifies live sessions, closes cleanly

## Getting started

Requires **Go 1.24+** to build. The result is a single static binary with no runtime to install.

```sh
git clone https://github.com/AlesixDev/scpxy
cd scpxy
make build
```

```sh
cp config.example.toml config.toml
$EDITOR config.toml
./scpxy check       # validate before starting anything
./scpxy run         # interactive dashboard
```

### Build

```sh
make build          # binary for the current platform
make dist           # linux/amd64, linux/arm64 and windows/amd64 into dist/
```

Or by hand:

```sh
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o scpxy-linux-amd64 ./cmd/scpxy
```

### Make targets

| Target | What it does |
|--------|--------------|
| `build` | Build `scpxy` for the current platform |
| `dist` | Cross-compile linux/amd64, linux/arm64 and windows/amd64 |
| `test` | Everything |
| `test-unit` | `internal/...` — fast, ~2s, no sockets bound |
| `test-integration` | `test/...` — end-to-end over loopback, ~9s |
| `vet` | `go vet ./...` |
| `fmt` | `go fmt ./...` |
| `clean` | Remove the binary and `dist/` |

## Usage

```sh
./scpxy run                    # interactive dashboard
./scpxy run --headless         # logs to stdout, for systemd or docker
./scpxy run --config /etc/scpxy/config.toml
./scpxy check                  # validate the configuration and exit
./scpxy version
```

### Console

The dashboard has a built-in console. In headless mode the same commands are read from stdin.

| Command | Effect |
|---|---|
| `players [backend]` | list connected players |
| `player <id>` | session details |
| `kick <id> [reason]` | disconnect a player |
| `move <id> <backend>` | send them to another backend on reconnect |
| `backends` | backend status |
| `backend enable\|disable <name>` | add or remove a backend from rotation |
| `maintenance on\|off` | stop or resume accepting new connections |
| `ban <ip>` / `unban <ip>` / `bans` | proxy-level block list |
| `ratelimit` | limiter status |
| `stats` | proxy summary |
| `log level <level>` | change log level at runtime |
| `quit` | graceful shutdown |

`Ctrl+C` starts a graceful shutdown: it stops accepting connections, notifies live sessions, closes the backend connections and releases the sockets.

About `move`: SCP:SL cannot move a player between servers without reconnecting, because Mirror state is not transferable. The command records the destination for that IP and disconnects the player with a message; they land on the chosen backend when they reconnect.

## Architecture

```
.
├── cmd/scpxy/               # Entry point: run and check
├── docs/
│   ├── installation.md      # Deployment walkthrough
│   ├── protocol-notes.md    # What is known about the wire format
│   └── systemd/             # scpxy.service
├── internal/
│   ├── cli/                 # Dashboard, admin console, headless mode
│   ├── config/              # TOML loading and strict validation
│   ├── events/              # Levelled event bus with subscribers
│   ├── litenetlib/          # The transport: packets, channels, peers, manager
│   ├── preauth/             # PreAuth codec, masking and real IP appending
│   ├── proxy/               # Session relay, backends, links
│   └── security/            # Per-IP gate: rate limiting, bans, session caps
└── test/integration/        # End-to-end client, proxy and backend
```

| Package | Responsibility |
|---|---|
| `litenetlib` | Packet layout, handshake, reliable channels, fragmentation, `ReliableMerged` |
| `preauth` | Parsing, masking and appending the real IP to the PreAuth block |
| `proxy` | One session per player: upstream socket, backend selection, bidirectional relay |
| `security` | The first line of defence, evaluated before any signature is touched |
| `config` | Strict TOML validation, backend resolution, `public_address` sanity check |
| `events` | Decouples the proxy from whatever is displaying it |
| `cli` | Dashboard and console on top of the event bus |

### Configuration

`config.example.toml` documents every key. The essentials:

| Section | Holds |
|---|---|
| `[proxy]` | `bind`, `public_address`, `ip_passthrough`, `max_players`, `log_level`, `handshake_timeout`, `headless`, `debug_relay` |
| `[security]` | `connect_rate_per_ip`, `connect_burst_per_ip`, `block_duration`, `max_sessions_per_ip`, `banned_ips` |
| `[[backends]]` | `name`, `address`, `priority`, `enabled`, `health_failures` — one block per real server |

`scpxy check` validates the file, resolves backend addresses and warns you if `public_address` does not point at this machine. Run it before starting.

### On the backend

The backend needs no plugin and no modification, only IP passthrough enabled and the proxy's address trusted. See [`docs/installation.md`](docs/installation.md#3-enable-passthrough-on-the-backend) for the exact settings.

> Passthrough is a trust mechanism, not a cryptographic one: any host listed as a trusted proxy can claim whatever IP it likes. Keep that list short and exact, and treat the proxy as a trusted component of your infrastructure.

## Deployment

See [`docs/installation.md`](docs/installation.md) for the full walkthrough — binary, config, systemd and firewall — and [`docs/systemd/scpxy.service`](docs/systemd/scpxy.service) for the unit file.

### File descriptor limit

The proxy opens **one outgoing UDP socket per connected player**. This is mandatory, not an optimisation choice: LiteNetLib identifies peers by their source IP:port pair, so sharing a socket would make the backend unable to tell players apart.

With hundreds of players you need a raised descriptor limit. The bundled systemd unit already sets `LimitNOFILE=65535`. If you start it by hand:

```sh
ulimit -n 65535
```

## Using your own domain

An `A` record pointing at the proxy works — the client resolves hostnames in the direct connect field.

```
connect.example.com.   A   203.0.113.10
```

Three things worth knowing:

- **No SRV records.** The port is always explicit: `connect.example.com:7777`.
- **You cannot route by domain.** UDP carries nothing equivalent to an HTTP `Host` header, so the name never reaches the proxy. Distinguishing several entries needs separate ports or separate IPs.
- **No orange cloud on Cloudflare.** The record must be "DNS only"; Cloudflare's HTTP proxy does not forward UDP.

## Limitations

**No registration in Northwood's central listing.** The proxy does not publish its own entry to `api.scpslgame.com` yet, so it does not appear in the in-game server browser and does not manage its own verkey. Players join by direct connect. This is the next planned phase; it needs the exact POST format validated against a real capture.

**The PreAuth codec is interpretive.** The proxy always forwards the PreAuth **raw, byte for byte**, appending only the real IP, so the relay works regardless of the internal format. But the metadata shown on the dashboard (user, version) comes from parsing that block with a field order that does not currently match the live client — so the dashboard falls back to showing the IP. It has no effect on connectivity. See [`docs/protocol-notes.md`](docs/protocol-notes.md).

**The admin console does not work under systemd.** In headless mode it reads commands from stdin and systemd provides none. To use the console, run `scpxy run` inside tmux. A control socket is the obvious fix and is not implemented yet.

## Debugging protocol issues

If a game update breaks the proxy, set `log_level = "debug"` and `debug_relay = true` in the config. That enables two extra traces:

```
wire   backend rx Channeled from 127.0.0.1:7778 ch=2 seq=14 frag=false 87 B
relay  ***4567@steam backend→client ch=0 ReliableOrdered 42 B 0a1b2c…
```

`wire` shows every packet as it arrives, before any processing; `relay` shows every message actually forwarded. A `rx REJECTED … prop=N` line with an unknown property number means Northwood added a new packet type — which has happened before, see the protocol notes.

## Development

```sh
make test-unit          # fast, ~2s, no sockets bound
make test-integration   # end-to-end over loopback, ~9s
make test               # both
make vet
```

Tests are split by what they need:

- **Unit tests live next to their package** (`internal/*/`) because they reach into unexported internals — packet layout, sequence arithmetic, the reliable channel, the rate limiter. Go cannot test unexported identifiers from another directory, so these cannot move.
- **Integration tests live in `test/integration/`**. They only use the public API, they bind real sockets and they take seconds rather than milliseconds, so keeping them separate means the fast feedback loop stays fast.

`internal/litenetlib` covers the packet format, handshake, reliable relay, fragmentation and `ReliableMerged` splitting, including a byte-exact reproduction of a real captured packet. `test/integration` spins up a real backend and client over loopback and verifies the appended IP, the challenge/rejection flow, upstream socket reuse, MTU pinning and lossless ordered relay of 2000 messages per direction.

## Tech stack

| Layer | Technology |
|---|---|
| Language | Go 1.24 |
| Transport | LiteNetLib over UDP, own implementation |
| Configuration | TOML |
| Interface | Terminal dashboard, or headless for systemd and Docker |
| Dependencies at runtime | None — single static binary |

## License

MIT — see [LICENSE](./LICENSE).

The transport is built on the LiteNetLib protocol (MIT), reimplemented in Go from the public specification and from observing real traffic. What is known about the wire format is written up in [`docs/protocol-notes.md`](docs/protocol-notes.md).
