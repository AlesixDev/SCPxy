package proxy

import (
	"net"
	"sort"
	"sync"
	"time"

	"github.com/AlesixDev/scpxy/internal/config"
)

const backendRecoveryProbe = 30 * time.Second

type BackendState struct {
	Name      string
	Address   string
	Enabled   bool
	Healthy   bool
	Players   int
	Failures  int
	LastError string
	DownSince time.Time
}

type backend struct {
	cfg       *config.Backend
	enabled   bool
	healthy   bool
	failures  int
	players   int
	lastError string
	downSince time.Time
	nextProbe time.Time
}

type backendPool struct {
	mu    sync.Mutex
	items []*backend
}

func newBackendPool(cfgs []config.Backend) *backendPool {
	pool := &backendPool{items: make([]*backend, 0, len(cfgs))}

	for i := range cfgs {
		pool.items = append(pool.items, &backend{
			cfg:     &cfgs[i],
			enabled: cfgs[i].IsEnabled(),
			healthy: true,
		})
	}

	sort.SliceStable(pool.items, func(i, j int) bool {
		return pool.items[i].cfg.Priority < pool.items[j].cfg.Priority
	})

	return pool
}

func (p *backendPool) pick(preferred string) (*backend, *net.UDPAddr, bool) {
	now := time.Now()

	p.mu.Lock()
	defer p.mu.Unlock()

	if preferred != "" {
		for _, item := range p.items {
			if item.cfg.Name != preferred {
				continue
			}

			if !item.enabled {
				return nil, nil, false
			}

			return item, item.cfg.Resolved(), true
		}

		return nil, nil, false
	}

	for _, item := range p.items {
		if !item.enabled {
			continue
		}

		if !item.healthy && now.Before(item.nextProbe) {
			continue
		}

		return item, item.cfg.Resolved(), true
	}

	return nil, nil, false
}

func (p *backendPool) byName(name string) *backend {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, item := range p.items {
		if item.cfg.Name == name {
			return item
		}
	}

	return nil
}

func (p *backendPool) markSuccess(b *backend) {
	p.mu.Lock()
	defer p.mu.Unlock()

	b.failures = 0
	b.lastError = ""

	if b.healthy {
		return
	}

	b.healthy = true
	b.downSince = time.Time{}
}

func (p *backendPool) markFailure(b *backend, reason string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	b.failures++
	b.lastError = reason
	b.nextProbe = time.Now().Add(backendRecoveryProbe)

	if !b.healthy {
		return false
	}

	if b.failures < b.cfg.HealthFailures {
		return false
	}

	b.healthy = false
	b.downSince = time.Now()

	return true
}

func (p *backendPool) addPlayer(b *backend, delta int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	b.players += delta

	if b.players < 0 {
		b.players = 0
	}
}

func (p *backendPool) setEnabled(name string, enabled bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, item := range p.items {
		if item.cfg.Name != name {
			continue
		}

		item.enabled = enabled

		return true
	}

	return false
}

func (p *backendPool) snapshot() []BackendState {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]BackendState, 0, len(p.items))

	for _, item := range p.items {
		out = append(out, BackendState{
			Name:      item.cfg.Name,
			Address:   item.cfg.Address,
			Enabled:   item.enabled,
			Healthy:   item.healthy,
			Players:   item.players,
			Failures:  item.failures,
			LastError: item.lastError,
			DownSince: item.downSince,
		})
	}

	return out
}
