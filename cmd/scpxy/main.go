package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/AlesixDev/scpxy/internal/cli"
	"github.com/AlesixDev/scpxy/internal/config"
	"github.com/AlesixDev/scpxy/internal/events"
	"github.com/AlesixDev/scpxy/internal/proxy"
)

const (
	version        = "0.2.0"
	defaultCfgPath = "config.toml"
	usageText      = `SCPxy %s — a proxy for SCP: Secret Laboratory

usage:
  scpxy run [--config path] [--headless]   start the proxy
  scpxy check [--config path]              validate the configuration and exit
  scpxy version                            print the version

options:
  --config path    configuration file (default %s)
  --headless       no interactive dashboard, logs to stdout (systemd, docker)
`
	exitUsage  = 2
	exitFailed = 1
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitFailed)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		os.Exit(exitUsage)
	}

	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	cfgPath := flags.String("config", defaultCfgPath, "path to the configuration file")
	headless := flags.Bool("headless", false, "no interactive dashboard")

	switch command {
	case "version", "--version", "-v":
		fmt.Println("scpxy " + version)
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	case "run", "check":
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
	default:
		usage()
		os.Exit(exitUsage)
	}

	cfg, err := config.Load(*cfgPath)

	if err != nil {
		return err
	}

	if command == "check" {
		return check(cfg, *cfgPath)
	}

	return start(cfg, *headless)
}

func check(cfg *config.Config, path string) error {
	fmt.Printf("configuration %s: ok\n", path)
	fmt.Printf("  listening on    %s\n", cfg.Proxy.Bind)
	fmt.Printf("  ip passthrough  %t\n", cfg.Proxy.IPPassthrough)
	fmt.Printf("  backends        %d\n", len(cfg.Backends))

	for i := range cfg.Backends {
		backend := &cfg.Backends[i]
		fmt.Printf("    - %-14s %s\n", backend.Name, backend.Resolved())
	}

	for _, warning := range publicAddressWarnings(cfg) {
		fmt.Printf("  warning: %s\n", warning)
	}

	return nil
}

func publicAddressWarnings(cfg *config.Config) []string {
	if cfg.Proxy.PublicAddress == "" {
		return nil
	}

	host, _, err := net.SplitHostPort(cfg.Proxy.PublicAddress)

	if err != nil {
		return []string{fmt.Sprintf("proxy.public_address %q should look like host:port", cfg.Proxy.PublicAddress)}
	}

	if net.ParseIP(host) != nil {
		return nil
	}

	resolved, err := net.LookupHost(host)

	if err != nil {
		return []string{fmt.Sprintf("cannot resolve %q: %v", host, err)}
	}

	local, err := localAddresses()

	if err != nil {
		return nil
	}

	for _, candidate := range resolved {
		if _, ok := local[candidate]; ok {
			return nil
		}
	}

	return []string{fmt.Sprintf("%s resolves to %s, which is not an address on this machine", host, strings.Join(resolved, ", "))}
}

func localAddresses() (map[string]struct{}, error) {
	addrs, err := net.InterfaceAddrs()

	if err != nil {
		return nil, err
	}

	out := make(map[string]struct{}, len(addrs))

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)

		if !ok {
			continue
		}

		out[ipNet.IP.String()] = struct{}{}
	}

	return out, nil
}

func start(cfg *config.Config, headless bool) error {
	level, _ := events.ParseLevel(cfg.Proxy.LogLevel)
	bus := events.NewBus(level)

	instance, err := proxy.New(cfg, bus)

	if err != nil {
		return err
	}

	bus.Infof("proxy", "SCPxy %s listening on %s", version, instance.LocalAddr())

	for i := range cfg.Backends {
		bus.Infof("backend", "%s → %s", cfg.Backends[i].Name, cfg.Backends[i].Resolved())
	}

	if !cfg.Proxy.IPPassthrough {
		bus.Warnf("proxy", "ip passthrough disabled: backends will see the proxy address")
	}

	headlessMode := headless || cfg.Proxy.Headless

	if !headlessMode && !isTerminal() {
		bus.Warnf("proxy", "output is not a terminal, falling back to headless mode")
		headlessMode = true
	}

	return cli.Run(cfg, bus, instance, headlessMode)
}

func isTerminal() bool {
	info, err := os.Stdout.Stat()

	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}

func usage() {
	fmt.Fprintf(os.Stderr, usageText, version, defaultCfgPath)
}
