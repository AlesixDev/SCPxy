package security

import (
	"net"
	"sync"
	"time"
)

const (
	sweepInterval  = time.Minute
	bucketIdleTime = 5 * time.Minute
)

type Decision byte

const (
	Allowed Decision = iota
	DeniedBanned
	DeniedRateLimited
	DeniedTooManySessions
	DeniedFull
)

func (d Decision) String() string {
	switch d {
	case Allowed:
		return "allowed"
	case DeniedBanned:
		return "banned"
	case DeniedRateLimited:
		return "rate limited"
	case DeniedTooManySessions:
		return "too many sessions"
	case DeniedFull:
		return "server full"
	}

	return "unknown"
}

func (d Decision) Message() string {
	switch d {
	case DeniedBanned:
		return "You are banned from this proxy."
	case DeniedRateLimited:
		return "Too many connection attempts. Try again in a moment."
	case DeniedTooManySessions:
		return "Too many connections from your address."
	case DeniedFull:
		return "The server is full."
	}

	return ""
}

type Options struct {
	RatePerSecond    float64
	Burst            int
	BlockDuration    time.Duration
	MaxSessionsPerIP int
	MaxSessions      int
	Banned           []string
}

type bucket struct {
	tokens       float64
	last         time.Time
	blockedUntil time.Time
}

type Gate struct {
	mu sync.Mutex

	opts     Options
	buckets  map[string]*bucket
	sessions map[string]int
	banned   map[string]struct{}
	total    int
	lastSwep time.Time

	blocked  uint64
	rejected uint64
}

func NewGate(opts Options) *Gate {
	g := &Gate{
		opts:     opts,
		buckets:  make(map[string]*bucket),
		sessions: make(map[string]int),
		banned:   make(map[string]struct{}, len(opts.Banned)),
		lastSwep: time.Now(),
	}

	for _, raw := range opts.Banned {
		parsed := net.ParseIP(raw)

		if parsed == nil {
			continue
		}

		g.banned[parsed.String()] = struct{}{}
	}

	return g
}

func (g *Gate) Allow(ip net.IP) Decision {
	key := ip.String()
	now := time.Now()

	g.mu.Lock()
	defer g.mu.Unlock()

	g.sweep(now)

	if _, ok := g.banned[key]; ok {
		g.rejected++
		return DeniedBanned
	}

	b, ok := g.buckets[key]

	if !ok {
		b = &bucket{tokens: float64(g.opts.Burst), last: now}
		g.buckets[key] = b
	}

	if now.Before(b.blockedUntil) {
		g.blocked++
		g.rejected++

		return DeniedRateLimited
	}

	b.tokens += now.Sub(b.last).Seconds() * g.opts.RatePerSecond
	b.last = now

	if b.tokens > float64(g.opts.Burst) {
		b.tokens = float64(g.opts.Burst)
	}

	if b.tokens < 1 {
		b.blockedUntil = now.Add(g.opts.BlockDuration)
		g.blocked++
		g.rejected++

		return DeniedRateLimited
	}

	if g.opts.MaxSessionsPerIP > 0 && g.sessions[key] >= g.opts.MaxSessionsPerIP {
		g.rejected++
		return DeniedTooManySessions
	}

	if g.opts.MaxSessions > 0 && g.total >= g.opts.MaxSessions {
		g.rejected++
		return DeniedFull
	}

	b.tokens--

	return Allowed
}

func (g *Gate) Acquire(ip net.IP) {
	key := ip.String()

	g.mu.Lock()
	defer g.mu.Unlock()

	g.sessions[key]++
	g.total++
}

func (g *Gate) Release(ip net.IP) {
	key := ip.String()

	g.mu.Lock()
	defer g.mu.Unlock()

	count := g.sessions[key]

	if count <= 1 {
		delete(g.sessions, key)
	}

	if count > 1 {
		g.sessions[key] = count - 1
	}

	if g.total > 0 {
		g.total--
	}
}

func (g *Gate) Ban(ip net.IP) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.banned[ip.String()] = struct{}{}
}

func (g *Gate) Unban(ip net.IP) bool {
	key := ip.String()

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.banned[key]; !ok {
		return false
	}

	delete(g.banned, key)

	return true
}

func (g *Gate) BannedList() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	out := make([]string, 0, len(g.banned))

	for key := range g.banned {
		out = append(out, key)
	}

	return out
}

type Stats struct {
	Rejected    uint64
	RateLimited uint64
	ActiveIPs   int
	BlockedIPs  int
	TotalActive int
	BannedCount int
}

func (g *Gate) Stats() Stats {
	now := time.Now()

	g.mu.Lock()
	defer g.mu.Unlock()

	blocked := 0

	for _, b := range g.buckets {
		if now.Before(b.blockedUntil) {
			blocked++
		}
	}

	return Stats{
		Rejected:    g.rejected,
		RateLimited: g.blocked,
		ActiveIPs:   len(g.sessions),
		BlockedIPs:  blocked,
		TotalActive: g.total,
		BannedCount: len(g.banned),
	}
}

func (g *Gate) sweep(now time.Time) {
	if now.Sub(g.lastSwep) < sweepInterval {
		return
	}

	g.lastSwep = now

	for key, b := range g.buckets {
		if now.Before(b.blockedUntil) {
			continue
		}

		if now.Sub(b.last) < bucketIdleTime {
			continue
		}

		if g.sessions[key] > 0 {
			continue
		}

		delete(g.buckets, key)
	}
}
