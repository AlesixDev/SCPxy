package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	defaultBind              = "0.0.0.0:7777"
	defaultConnectRate       = 5
	defaultConnectBurst      = 10
	defaultBlockDuration     = 30 * time.Second
	defaultMaxPerIP          = 4
	defaultHandshakeTimeout  = 10 * time.Second
	defaultHealthInterval    = 10 * time.Second
	defaultHealthFailures    = 3
	defaultMaxPlayers        = 0
	defaultLogLevel          = "info"
	minConnectRate           = 0.1
	minHandshakeTimeoutValue = time.Second
)

var validLogLevels = map[string]struct{}{"debug": {}, "info": {}, "warn": {}, "error": {}}

type Config struct {
	Proxy    Proxy     `toml:"proxy"`
	Security Security  `toml:"security"`
	Backends []Backend `toml:"backends"`
}

type Proxy struct {
	Bind             string   `toml:"bind"`
	PublicAddress    string   `toml:"public_address"`
	IPPassthrough    bool     `toml:"ip_passthrough"`
	MaxPlayers       int      `toml:"max_players"`
	LogLevel         string   `toml:"log_level"`
	HandshakeTimeout Duration `toml:"handshake_timeout"`
	DebugRelay       bool     `toml:"debug_relay"`
	Headless         bool     `toml:"headless"`
}

type Security struct {
	ConnectRatePerIP  float64  `toml:"connect_rate_per_ip"`
	ConnectBurstPerIP int      `toml:"connect_burst_per_ip"`
	BlockDuration     Duration `toml:"block_duration"`
	MaxSessionsPerIP  int      `toml:"max_sessions_per_ip"`
	Banned            []string `toml:"banned_ips"`
}

type Backend struct {
	Name           string   `toml:"name"`
	Address        string   `toml:"address"`
	Priority       int      `toml:"priority"`
	Enabled        *bool    `toml:"enabled"`
	HealthInterval Duration `toml:"health_interval"`
	HealthFailures int      `toml:"health_failures"`
	resolved       *net.UDPAddr
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))

	if err != nil {
		return fmt.Errorf("invalid duration %q", string(text))
	}

	d.Duration = parsed

	return nil
}

func (b *Backend) Resolved() *net.UDPAddr {
	return b.resolved
}

func (b *Backend) IsEnabled() bool {
	return b.Enabled == nil || *b.Enabled
}

func Default() *Config {
	return &Config{
		Proxy: Proxy{
			Bind:             defaultBind,
			IPPassthrough:    true,
			MaxPlayers:       defaultMaxPlayers,
			LogLevel:         defaultLogLevel,
			HandshakeTimeout: Duration{defaultHandshakeTimeout},
		},
		Security: Security{
			ConnectRatePerIP:  defaultConnectRate,
			ConnectBurstPerIP: defaultConnectBurst,
			BlockDuration:     Duration{defaultBlockDuration},
			MaxSessionsPerIP:  defaultMaxPerIP,
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()

	raw, err := os.ReadFile(path)

	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("configuration file %q not found", path)
	}

	if err != nil {
		return nil, fmt.Errorf("cannot read %q: %w", path, err)
	}

	meta, err := toml.Decode(string(raw), cfg)

	if err != nil {
		return nil, fmt.Errorf("invalid configuration in %q: %w", path, err)
	}

	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))

		for _, key := range undecoded {
			keys = append(keys, key.String())
		}

		return nil, fmt.Errorf("unknown keys in %q: %s", path, strings.Join(keys, ", "))
	}

	if err := cfg.Normalize(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) Normalize() error {
	if c.Proxy.Bind == "" {
		c.Proxy.Bind = defaultBind
	}

	if _, err := net.ResolveUDPAddr("udp", c.Proxy.Bind); err != nil {
		return fmt.Errorf("proxy.bind %q is not a valid address", c.Proxy.Bind)
	}

	if _, ok := validLogLevels[c.Proxy.LogLevel]; !ok {
		return fmt.Errorf("proxy.log_level %q is invalid (debug, info, warn, error)", c.Proxy.LogLevel)
	}

	if c.Proxy.HandshakeTimeout.Duration < minHandshakeTimeoutValue {
		return errors.New("proxy.handshake_timeout must be at least 1s")
	}

	if c.Security.ConnectRatePerIP < minConnectRate {
		return errors.New("security.connect_rate_per_ip must be greater than 0.1")
	}

	if c.Security.ConnectBurstPerIP < 1 {
		return errors.New("security.connect_burst_per_ip must be at least 1")
	}

	for _, raw := range c.Security.Banned {
		if net.ParseIP(raw) == nil {
			return fmt.Errorf("security.banned_ips contains an invalid IP: %q", raw)
		}
	}

	if len(c.Backends) == 0 {
		return errors.New("at least one [[backends]] entry is required")
	}

	seen := make(map[string]struct{}, len(c.Backends))

	for i := range c.Backends {
		backend := &c.Backends[i]

		if backend.Name == "" {
			return fmt.Errorf("backend #%d has no name", i+1)
		}

		if _, ok := seen[backend.Name]; ok {
			return fmt.Errorf("duplicate backend name: %q", backend.Name)
		}

		seen[backend.Name] = struct{}{}

		addr, err := net.ResolveUDPAddr("udp", backend.Address)

		if err != nil {
			return fmt.Errorf("backend %q: address %q is not valid", backend.Name, backend.Address)
		}

		backend.resolved = addr

		if backend.HealthInterval.Duration == 0 {
			backend.HealthInterval = Duration{defaultHealthInterval}
		}

		if backend.HealthFailures == 0 {
			backend.HealthFailures = defaultHealthFailures
		}
	}

	return nil
}

func (c *Config) PublicAddress() string {
	if c.Proxy.PublicAddress != "" {
		return c.Proxy.PublicAddress
	}

	return c.Proxy.Bind
}
