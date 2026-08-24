package security

import (
	"net"
	"testing"
	"time"
)

func testOptions() Options {
	return Options{
		RatePerSecond:    1,
		Burst:            3,
		BlockDuration:    time.Minute,
		MaxSessionsPerIP: 2,
	}
}

func TestBurstIsConsumedThenBlocked(t *testing.T) {
	gate := NewGate(testOptions())
	ip := net.ParseIP("203.0.113.5")

	for i := 0; i < 3; i++ {
		if got := gate.Allow(ip); got != Allowed {
			t.Fatalf("attempt %d refused: %s", i+1, got)
		}
	}

	if got := gate.Allow(ip); got != DeniedRateLimited {
		t.Fatalf("the fourth attempt should be rate limited, got %s", got)
	}

	if got := gate.Allow(ip); got != DeniedRateLimited {
		t.Fatalf("the IP should still be blocked, got %s", got)
	}
}

func TestRateLimitIsPerIP(t *testing.T) {
	gate := NewGate(testOptions())
	noisy := net.ParseIP("203.0.113.5")
	quiet := net.ParseIP("203.0.113.6")

	for i := 0; i < 4; i++ {
		gate.Allow(noisy)
	}

	if got := gate.Allow(quiet); got != Allowed {
		t.Fatalf("a different IP should be unaffected, got %s", got)
	}
}

func TestMaxSessionsPerIP(t *testing.T) {
	gate := NewGate(testOptions())
	ip := net.ParseIP("203.0.113.7")

	gate.Allow(ip)
	gate.Acquire(ip)
	gate.Allow(ip)
	gate.Acquire(ip)

	if got := gate.Allow(ip); got != DeniedTooManySessions {
		t.Fatalf("expected the session limit, got %s", got)
	}

	gate.Release(ip)

	if got := gate.Allow(ip); got != Allowed {
		t.Fatalf("should allow after releasing a session, got %s", got)
	}
}

func TestBannedIPIsAlwaysDenied(t *testing.T) {
	opts := testOptions()
	opts.Banned = []string{"203.0.113.8"}
	gate := NewGate(opts)
	ip := net.ParseIP("203.0.113.8")

	if got := gate.Allow(ip); got != DeniedBanned {
		t.Fatalf("expected a ban, got %s", got)
	}

	if !gate.Unban(ip) {
		t.Fatal("Unban returned false for a banned IP")
	}

	if got := gate.Allow(ip); got != Allowed {
		t.Fatalf("should allow after unbanning, got %s", got)
	}
}

func TestGlobalCapacityLimit(t *testing.T) {
	opts := testOptions()
	opts.MaxSessions = 1
	gate := NewGate(opts)

	first := net.ParseIP("203.0.113.9")
	second := net.ParseIP("203.0.113.10")

	gate.Allow(first)
	gate.Acquire(first)

	if got := gate.Allow(second); got != DeniedFull {
		t.Fatalf("expected server full, got %s", got)
	}
}
