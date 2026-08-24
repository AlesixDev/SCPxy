# Deploying SCPxy

A guide for a Debian/Ubuntu VPS. It assumes you already have an SCP:SL server running — installing and operating the game server is out of scope here. The result is SCPxy on `7777` facing players, relaying to a backend on `7778`.

```
player ──► :7777  SCPxy  ──► 127.0.0.1:7778  backend
```

SCPxy is a single static binary. It needs no runtime, no .NET and no plugin on the backend.

## 1. Install the binary

```sh
sudo install -m 0755 scpxy-linux-amd64 /usr/local/bin/scpxy
sudo useradd --system --no-create-home --shell /usr/sbin/nologin scpxy
sudo mkdir -p /etc/scpxy
sudo cp config.example.toml /etc/scpxy/config.toml
sudo chown -R scpxy:scpxy /etc/scpxy
```

## 2. Configure

In `/etc/scpxy/config.toml`:

```toml
[proxy]
bind = "0.0.0.0:7777"
ip_passthrough = true
headless = true

[[backends]]
name = "main"
address = "127.0.0.1:7778"
```

Validate it before starting anything:

```sh
sudo -u scpxy /usr/local/bin/scpxy check --config /etc/scpxy/config.toml
```

## 3. Enable passthrough on the backend

In the backend's `config_gameplay.txt`:

```
enable_proxy_ip_passthrough: true
trusted_proxies_ip_addresses: 127.0.0.1
```

**The trusted address is the address the proxy uses to reach the backend.** With both on the same machine that is `127.0.0.1`, not the VPS public IP — the most common mistake in this setup. If you later split the proxy and the backend apart, this value has to change *and* the backend has to restart. Configuration changes only take effect on restart.

## 4. systemd unit

```sh
sudo cp docs/systemd/scpxy.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now scpxy
sudo journalctl -u scpxy -f
```

## 5. Firewall

Players must only be able to reach the proxy. The backend's port stays inside the machine.

```sh
sudo ufw allow 22/tcp
sudo ufw allow 7777/udp
sudo ufw deny 7778/udp
sudo ufw enable
```

Without this, anyone can connect straight to `7778` and bypass the proxy entirely, which defeats the rate limiting, the block list and the passthrough.

If your provider has its own panel firewall, open `7777/udp` there too. It filters before traffic reaches the system, so a port blocked there stays blocked no matter what `ufw` says.

## 6. Verify

```sh
sudo systemctl status scpxy
sudo ss -ulnp | grep -E '7777|7778'
```

Connect from the game client to `YOUR_IP:7777` and watch the proxy:

```sh
sudo journalctl -u scpxy -f
```

You should see a `player … → main` line.

To confirm passthrough actually works, watch the **backend's** log instead: the address it records for the player must be their real IP, not `127.0.0.1`. If it shows `127.0.0.1`, the passthrough is not being applied — usually because `trusted_proxies_ip_addresses` does not match or the backend was not restarted after the edit.

## Notes

- **File descriptors.** SCPxy opens one upstream socket per player. The bundled systemd unit already sets `LimitNOFILE=65535`.
- **Game updates.** SCPxy does not need rebuilding when Northwood ships a version: it forwards the PreAuth untouched whatever the version. It can still break if Northwood changes the transport itself — see [protocol-notes.md](protocol-notes.md).
- **No central listing.** SCPxy does not publish its own entry to Northwood's API yet, so the proxy does not show up in the in-game server browser and players join by direct connect. The backend would show up if its port were reachable, which is another reason to keep it firewalled.
- **Admin console under systemd.** In headless mode the console reads commands from stdin, and systemd provides none. To use `players`, `stats` or `kick`, stop the service and run `scpxy run --config /etc/scpxy/config.toml` inside tmux.
