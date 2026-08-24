package proxy

import (
	"sync"
	"time"

	"github.com/AlesixDev/scpxy/internal/litenetlib"
)

const (
	linkLeaseTTL     = 45 * time.Second
	linkSweepPeriod  = 15 * time.Second
	linkEphemeralAny = ":0"
)

type upstreamLink struct {
	mu      sync.Mutex
	mgr     *litenetlib.Manager
	session *session
	expires time.Time
}

func (l *upstreamLink) current() *session {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.session
}

func (l *upstreamLink) bind(s *session) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.session = s
	l.expires = time.Time{}
}

func (l *upstreamLink) release() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.session = nil
	l.expires = time.Now().Add(linkLeaseTTL)
}

func (l *upstreamLink) reusable(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.session == nil && !l.expires.IsZero() && now.Before(l.expires)
}

func (l *upstreamLink) expired(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.session == nil && !l.expires.IsZero() && now.After(l.expires)
}

func (p *Proxy) acquireLink(key string) (*upstreamLink, error) {
	now := time.Now()

	p.mu.Lock()
	existing, ok := p.links[key]
	p.mu.Unlock()

	if ok && existing.reusable(now) {
		return existing, nil
	}

	if ok {
		p.mu.Lock()
		delete(p.links, key)
		p.mu.Unlock()

		go existing.mgr.Stop()
	}

	link := &upstreamLink{}

	mgr, err := litenetlib.Listen(linkEphemeralAny, litenetlib.Config{
		DisableMtuDiscovery: true,
		Trace:               p.wireTracer("backend"),
		Handler: litenetlib.Handler{
			OnPeerConnected: func(peer *litenetlib.Peer) {
				if s := link.current(); s != nil {
					p.onUpstreamConnected(s, peer)
				}
			},
			OnPeerDisconnected: func(peer *litenetlib.Peer, reason litenetlib.DisconnectReason, data []byte) {
				if s := link.current(); s != nil {
					p.onUpstreamDisconnected(s, reason, data)
				}
			},
			OnReceive: func(peer *litenetlib.Peer, data []byte, channel byte, delivery litenetlib.Delivery) {
				if s := link.current(); s != nil {
					p.onUpstreamReceive(s, data, channel, delivery)
				}
			},
		},
	})

	if err != nil {
		return nil, err
	}

	link.mgr = mgr

	p.mu.Lock()
	p.links[key] = link
	p.mu.Unlock()

	return link, nil
}

func (p *Proxy) sweepLinks() {
	ticker := time.NewTicker(linkSweepPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-p.done:
			return
		case now := <-ticker.C:
			p.mu.Lock()
			stale := make([]*upstreamLink, 0)

			for key, link := range p.links {
				if !link.expired(now) {
					continue
				}

				stale = append(stale, link)
				delete(p.links, key)
			}

			p.mu.Unlock()

			for _, link := range stale {
				link.mgr.Stop()
			}
		}
	}
}

func (p *Proxy) stopAllLinks() {
	p.mu.Lock()
	links := make([]*upstreamLink, 0, len(p.links))

	for key, link := range p.links {
		links = append(links, link)
		delete(p.links, key)
	}

	p.mu.Unlock()

	for _, link := range links {
		link.mgr.Stop()
	}
}
