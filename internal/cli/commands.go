package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AlesixDev/scpxy/internal/events"
	"github.com/AlesixDev/scpxy/internal/proxy"
)

const helpText = `available commands:
  players [backend]        list connected players
  player <id>              details for one player
  kick <id> [reason]       disconnect a player
  move <id> <backend>      send them to another backend on reconnect
  backends                 backend status
  backend enable <name>    put a backend back into rotation
  backend disable <name>   take a backend out of rotation
  maintenance on|off       stop or resume accepting new connections
  ban <ip> / unban <ip>    proxy-level block list
  bans                     show blocked IPs
  ratelimit                connection limiter status
  stats                    proxy summary
  log level <level>        debug, info, warn, error
  help                     this help
  quit                     graceful shutdown`

type Console struct {
	proxy *proxy.Proxy
	bus   *events.Bus
	quit  func()
}

func NewConsole(p *proxy.Proxy, bus *events.Bus, quit func()) *Console {
	return &Console{proxy: p, bus: bus, quit: quit}
}

func (c *Console) Execute(line string) []string {
	fields := strings.Fields(strings.TrimSpace(line))

	if len(fields) == 0 {
		return nil
	}

	switch strings.ToLower(fields[0]) {
	case "help", "?":
		return strings.Split(helpText, "\n")
	case "players":
		return c.players(fields[1:])
	case "player":
		return c.player(fields[1:])
	case "kick":
		return c.kick(fields[1:])
	case "move":
		return c.move(fields[1:])
	case "backends":
		return c.backends()
	case "backend":
		return c.backend(fields[1:])
	case "maintenance":
		return c.maintenance(fields[1:])
	case "ban":
		return c.ban(fields[1:])
	case "unban":
		return c.unban(fields[1:])
	case "bans":
		return c.bans()
	case "ratelimit":
		return c.ratelimit()
	case "stats":
		return c.stats()
	case "log":
		return c.log(fields[1:])
	case "quit", "exit":
		c.quit()
		return []string{"shutting down…"}
	}

	return []string{fmt.Sprintf("unknown command: %s (try help)", fields[0])}
}

func (c *Console) players(args []string) []string {
	list := c.proxy.Players()

	if len(args) > 0 {
		filtered := list[:0]

		for _, item := range list {
			if !strings.EqualFold(item.Backend, args[0]) {
				continue
			}

			filtered = append(filtered, item)
		}

		list = filtered
	}

	if len(list) == 0 {
		return []string{"no players connected"}
	}

	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })

	out := []string{fmt.Sprintf("%-5s %-22s %-16s %-12s %-7s %s", "ID", "USER", "IP", "BACKEND", "PING", "UPTIME")}

	for _, item := range list {
		state := ""

		if !item.Active {
			state = " (connecting)"
		}

		out = append(out, fmt.Sprintf("%-5d %-22s %-16s %-12s %-7s %s%s",
			item.ID, truncate(item.Masked, 22), truncate(item.RealIP, 16), truncate(item.Backend, 12),
			fmt.Sprintf("%dms", item.Latency), shortDuration(item.Connected), state))
	}

	return out
}

func (c *Console) player(args []string) []string {
	if len(args) < 1 {
		return []string{"usage: player <id>"}
	}

	id, err := strconv.Atoi(args[0])

	if err != nil {
		return []string{"the id must be a number"}
	}

	for _, item := range c.proxy.Players() {
		if item.ID != id {
			continue
		}

		return []string{
			fmt.Sprintf("id        %d", item.ID),
			fmt.Sprintf("user      %s", item.Masked),
			fmt.Sprintf("version   %s", item.Version),
			fmt.Sprintf("real ip   %s", item.RealIP),
			fmt.Sprintf("backend   %s", item.Backend),
			fmt.Sprintf("latency   %dms", item.Latency),
			fmt.Sprintf("connected %s", shortDuration(item.Connected)),
		}
	}

	return []string{proxy.ErrPlayerNotFound.Error()}
}

func (c *Console) kick(args []string) []string {
	if len(args) < 1 {
		return []string{"usage: kick <id> [reason]"}
	}

	id, err := strconv.Atoi(args[0])

	if err != nil {
		return []string{"the id must be a number"}
	}

	reason := strings.Join(args[1:], " ")

	if err := c.proxy.Kick(id, reason); err != nil {
		return []string{err.Error()}
	}

	return []string{fmt.Sprintf("player %d disconnected", id)}
}

func (c *Console) move(args []string) []string {
	if len(args) < 2 {
		return []string{"usage: move <id> <backend>"}
	}

	id, err := strconv.Atoi(args[0])

	if err != nil {
		return []string{"the id must be a number"}
	}

	if err := c.proxy.Move(id, args[1]); err != nil {
		return []string{err.Error()}
	}

	return []string{fmt.Sprintf("player %d will join %s on reconnect", id, args[1])}
}

func (c *Console) backends() []string {
	list := c.proxy.Backends()

	if len(list) == 0 {
		return []string{"no backends configured"}
	}

	out := []string{fmt.Sprintf("%-14s %-24s %-10s %-9s %s", "NAME", "ADDRESS", "STATE", "PLAYERS", "LAST ERROR")}

	for _, item := range list {
		out = append(out, fmt.Sprintf("%-14s %-24s %-10s %-9d %s",
			truncate(item.Name, 14), truncate(item.Address, 24), backendStatus(item), item.Players, item.LastError))
	}

	return out
}

func (c *Console) backend(args []string) []string {
	if len(args) < 2 {
		return []string{"usage: backend enable|disable <name>"}
	}

	enabled := strings.EqualFold(args[0], "enable")

	if !enabled && !strings.EqualFold(args[0], "disable") {
		return []string{"usage: backend enable|disable <name>"}
	}

	if err := c.proxy.SetBackendEnabled(args[1], enabled); err != nil {
		return []string{err.Error()}
	}

	action := "disabled"

	if enabled {
		action = "enabled"
	}

	return []string{fmt.Sprintf("backend %s %s", args[1], action)}
}

func (c *Console) maintenance(args []string) []string {
	if len(args) < 1 {
		return []string{"usage: maintenance on|off"}
	}

	on := strings.EqualFold(args[0], "on")

	if !on && !strings.EqualFold(args[0], "off") {
		return []string{"usage: maintenance on|off"}
	}

	c.proxy.SetMaintenance(on)

	if on {
		return []string{"maintenance mode on: new connections are refused"}
	}

	return []string{"maintenance mode off"}
}

func (c *Console) ban(args []string) []string {
	if len(args) < 1 {
		return []string{"usage: ban <ip>"}
	}

	if err := c.proxy.Ban(args[0]); err != nil {
		return []string{err.Error()}
	}

	return []string{fmt.Sprintf("%s blocked", args[0])}
}

func (c *Console) unban(args []string) []string {
	if len(args) < 1 {
		return []string{"usage: unban <ip>"}
	}

	if err := c.proxy.Unban(args[0]); err != nil {
		return []string{err.Error()}
	}

	return []string{fmt.Sprintf("%s unblocked", args[0])}
}

func (c *Console) bans() []string {
	list := c.proxy.BannedList()

	if len(list) == 0 {
		return []string{"no blocked IPs"}
	}

	sort.Strings(list)

	return list
}

func (c *Console) ratelimit() []string {
	stats := c.proxy.Stats().Security

	return []string{
		fmt.Sprintf("refused          %d", stats.Rejected),
		fmt.Sprintf("rate limited     %d", stats.RateLimited),
		fmt.Sprintf("blocked IPs      %d", stats.BlockedIPs),
		fmt.Sprintf("IPs with session %d", stats.ActiveIPs),
		fmt.Sprintf("banned IPs       %d", stats.BannedCount),
	}
}

func (c *Console) stats() []string {
	stats := c.proxy.Stats()

	return []string{
		fmt.Sprintf("uptime       %s", shortDuration(stats.Uptime)),
		fmt.Sprintf("players      %d (%d connecting)", stats.Players, stats.Pending),
		fmt.Sprintf("upstream     %s", humanBytes(stats.BytesToServer)),
		fmt.Sprintf("downstream   %s", humanBytes(stats.BytesToClient)),
		fmt.Sprintf("maintenance  %t", stats.Maintenance),
	}
}

func (c *Console) log(args []string) []string {
	if len(args) < 2 || !strings.EqualFold(args[0], "level") {
		return []string{"usage: log level <debug|info|warn|error>"}
	}

	level, ok := events.ParseLevel(args[1])

	if !ok {
		return []string{"invalid level: use debug, info, warn or error"}
	}

	c.bus.SetLevel(level)

	return []string{fmt.Sprintf("log level: %s", level)}
}

func backendStatus(item proxy.BackendState) string {
	if !item.Enabled {
		return "disabled"
	}

	if !item.Healthy {
		return "down"
	}

	return "healthy"
}

func truncate(v string, max int) string {
	if len(v) <= max {
		return v
	}

	if max <= 1 {
		return v[:max]
	}

	return v[:max-1] + "…"
}

func shortDuration(d time.Duration) string {
	d = d.Round(time.Second)

	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}

	if d < time.Hour {
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}

	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

func humanBytes(v uint64) string {
	const unit = 1024

	if v < unit {
		return fmt.Sprintf("%d B", v)
	}

	div, exp := uint64(unit), 0

	for n := v / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %cB", float64(v)/float64(div), "KMGTPE"[exp])
}
