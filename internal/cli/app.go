package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/AlesixDev/scpxy/internal/config"
	"github.com/AlesixDev/scpxy/internal/events"
	"github.com/AlesixDev/scpxy/internal/proxy"
)

const shutdownGrace = 3 * time.Second

func Run(cfg *config.Config, bus *events.Bus, p *proxy.Proxy, headless bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	quit := make(chan struct{})
	closeOnce := func() {
		select {
		case <-quit:
		default:
			close(quit)
		}
	}

	console := NewConsole(p, bus, closeOnce)

	go func() {
		select {
		case <-ctx.Done():
			closeOnce()
		case <-quit:
		}
	}()

	if headless {
		return runHeadless(bus, console, p, cfg, quit)
	}

	return runTUI(bus, console, p, cfg, quit)
}

func runTUI(bus *events.Bus, console *Console, p *proxy.Proxy, cfg *config.Config, quit chan struct{}) error {
	m := newModel(p, bus, console, cfg.PublicAddress(), func() {
		select {
		case <-quit:
		default:
			close(quit)
		}
	})

	program := tea.NewProgram(m, tea.WithAltScreen())

	go func() {
		<-quit
		program.Quit()
	}()

	if _, err := program.Run(); err != nil {
		return fmt.Errorf("cannot start the dashboard: %w", err)
	}

	bus.Unsubscribe(m.subID)
	shutdown(bus, p)

	return nil
}

func runHeadless(bus *events.Bus, console *Console, p *proxy.Proxy, cfg *config.Config, quit chan struct{}) error {
	id, ch := bus.Subscribe()

	go func() {
		for entry := range ch {
			fmt.Fprintln(os.Stdout, renderPlain(entry))
		}
	}()

	for _, entry := range bus.History() {
		fmt.Fprintln(os.Stdout, renderPlain(entry))
	}

	bus.Infof("proxy", "listening on %s", cfg.PublicAddress())

	go readStdin(console, quit)

	<-quit
	bus.Unsubscribe(id)
	shutdown(bus, p)

	return nil
}

func readStdin(console *Console, quit chan struct{}) {
	scanner := bufio.NewScanner(os.Stdin)

	for scanner.Scan() {
		select {
		case <-quit:
			return
		default:
		}

		for _, line := range console.Execute(scanner.Text()) {
			fmt.Fprintln(os.Stdout, line)
		}
	}
}

func shutdown(bus *events.Bus, p *proxy.Proxy) {
	done := make(chan struct{})

	go func() {
		p.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(shutdownGrace):
		bus.Warnf("proxy", "forced shutdown after %s", shutdownGrace)
	}
}

func renderPlain(entry events.Entry) string {
	level := entry.Level.String()

	if lipgloss.ColorProfile() != 0 {
		switch entry.Level {
		case events.Warn:
			level = lipgloss.NewStyle().Foreground(colorWarn).Render(level)
		case events.Error:
			level = lipgloss.NewStyle().Foreground(colorErr).Render(level)
		case events.Info:
			level = lipgloss.NewStyle().Foreground(colorAccent).Render(level)
		}
	}

	return fmt.Sprintf("%s %-5s %-8s %s", entry.Time.Format("15:04:05"), level, entry.Scope, entry.Message)
}
