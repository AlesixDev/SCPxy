package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/AlesixDev/scpxy/internal/events"
	"github.com/AlesixDev/scpxy/internal/proxy"
)

const (
	refreshInterval = 500 * time.Millisecond
	maxLogLines     = 400
	minLogRows      = 5
	commandScope    = "console"
)

var (
	colorMuted   = lipgloss.AdaptiveColor{Light: "#6b7280", Dark: "#9aa0a6"}
	colorAccent  = lipgloss.AdaptiveColor{Light: "#7c3aed", Dark: "#c4b5fd"}
	colorOK      = lipgloss.AdaptiveColor{Light: "#15803d", Dark: "#86efac"}
	colorWarn    = lipgloss.AdaptiveColor{Light: "#b45309", Dark: "#fcd34d"}
	colorErr     = lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#fca5a5"}
	colorBorder  = lipgloss.AdaptiveColor{Light: "#d4d4d8", Dark: "#3f3f46"}
	styleTitle   = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleMuted   = lipgloss.NewStyle().Foreground(colorMuted)
	styleHeading = lipgloss.NewStyle().Bold(true).Foreground(colorMuted)
	stylePanel   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorBorder).Padding(0, 1)
)

type logMsg events.Entry

type tickMsg time.Time

type model struct {
	console  *Console
	proxy    *proxy.Proxy
	bus      *events.Bus
	logs     <-chan events.Entry
	subID    int
	input    textinput.Model
	lines    []events.Entry
	width    int
	height   int
	address  string
	quitting bool
	onQuit   func()
}

func newModel(p *proxy.Proxy, bus *events.Bus, console *Console, address string, onQuit func()) *model {
	input := textinput.New()
	input.Prompt = "scpxy> "
	input.Placeholder = "help"
	input.Focus()
	input.CharLimit = 200

	id, ch := bus.Subscribe()

	return &model{
		console: console,
		proxy:   p,
		bus:     bus,
		logs:    ch,
		subID:   id,
		input:   input,
		lines:   bus.History(),
		address: address,
		onQuit:  onQuit,
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.waitForLog(), tickCmd())
}

func (m *model) waitForLog() tea.Cmd {
	return func() tea.Msg {
		entry, ok := <-m.logs

		if !ok {
			return nil
		}

		return logMsg(entry)
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = typed.Width
		m.height = typed.Height

		return m, nil
	case tea.KeyMsg:
		return m.handleKey(typed)
	case logMsg:
		m.appendLine(events.Entry(typed))

		return m, m.waitForLog()
	case tickMsg:
		return m, tickCmd()
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)

	return m, cmd
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		m.quitting = true
		m.onQuit()

		return m, tea.Quit
	case tea.KeyEnter:
		line := m.input.Value()
		m.input.SetValue("")

		if strings.TrimSpace(line) == "" {
			return m, nil
		}

		m.appendLine(events.Entry{Time: time.Now(), Level: events.Info, Scope: commandScope, Message: "> " + line})

		for _, out := range m.console.Execute(line) {
			m.appendLine(events.Entry{Time: time.Now(), Level: events.Info, Scope: commandScope, Message: out})
		}

		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)

	return m, cmd
}

func (m *model) appendLine(entry events.Entry) {
	m.lines = append(m.lines, entry)

	if len(m.lines) <= maxLogLines {
		return
	}

	m.lines = m.lines[len(m.lines)-maxLogLines:]
}

func (m *model) View() string {
	if m.quitting {
		return styleMuted.Render("closing sockets…") + "\n"
	}

	width := m.width

	if width < 60 {
		width = 60
	}

	stats := m.proxy.Stats()
	header := m.renderHeader(stats, width)
	panels := m.renderPanels(stats, width)
	logRows := m.height - lipgloss.Height(header) - lipgloss.Height(panels) - 3

	if logRows < minLogRows {
		logRows = minLogRows
	}

	return strings.Join([]string{
		header,
		panels,
		m.renderLogs(logRows, width),
		m.input.View(),
	}, "\n")
}

func (m *model) renderHeader(stats proxy.Stats, width int) string {
	mode := "running"

	if stats.Maintenance {
		mode = "MAINTENANCE"
	}

	left := styleTitle.Render("SCPxy") + styleMuted.Render(" · "+m.address)
	right := styleMuted.Render(fmt.Sprintf("uptime %s · %s", shortDuration(stats.Uptime), mode))
	gap := width - lipgloss.Width(left) - lipgloss.Width(right) - 2

	if gap < 1 {
		gap = 1
	}

	return left + strings.Repeat(" ", gap) + right
}

func (m *model) renderPanels(stats proxy.Stats, width int) string {
	panelWidth := width/2 - 3

	if panelWidth < 24 {
		panelWidth = 24
	}

	return lipgloss.JoinHorizontal(lipgloss.Top,
		stylePanel.Width(panelWidth).Render(m.renderBackends()),
		stylePanel.Width(panelWidth).Render(m.renderSummary(stats)),
	)
}

func (m *model) renderBackends() string {
	rows := []string{styleHeading.Render("BACKENDS")}

	for _, item := range m.proxy.Backends() {
		marker := lipgloss.NewStyle().Foreground(colorOK).Render("●")

		if !item.Healthy {
			marker = lipgloss.NewStyle().Foreground(colorErr).Render("○")
		}

		if !item.Enabled {
			marker = styleMuted.Render("◌")
		}

		rows = append(rows, fmt.Sprintf("%s %-12s %-21s %3d", marker, truncate(item.Name, 12), truncate(item.Address, 21), item.Players))
	}

	if len(rows) == 1 {
		rows = append(rows, styleMuted.Render("no backends"))
	}

	return strings.Join(rows, "\n")
}

func (m *model) renderSummary(stats proxy.Stats) string {
	rows := []string{
		styleHeading.Render("STATUS"),
		fmt.Sprintf("players       %d active, %d connecting", stats.Players, stats.Pending),
		fmt.Sprintf("traffic       ↑ %s  ↓ %s", humanBytes(stats.BytesToServer), humanBytes(stats.BytesToClient)),
		fmt.Sprintf("refused       %d (%d rate limited)", stats.Security.Rejected, stats.Security.RateLimited),
		fmt.Sprintf("blocked       %d IPs, %d banned", stats.Security.BlockedIPs, stats.Security.BannedCount),
	}

	return strings.Join(rows, "\n")
}

func (m *model) renderLogs(rows, width int) string {
	start := len(m.lines) - rows

	if start < 0 {
		start = 0
	}

	visible := m.lines[start:]
	out := make([]string, 0, rows)

	for _, entry := range visible {
		out = append(out, renderEntry(entry, width))
	}

	for len(out) < rows {
		out = append(out, "")
	}

	return strings.Join(out, "\n")
}

func renderEntry(entry events.Entry, width int) string {
	style := styleMuted

	switch entry.Level {
	case events.Info:
		style = lipgloss.NewStyle().Foreground(colorAccent)
	case events.Warn:
		style = lipgloss.NewStyle().Foreground(colorWarn)
	case events.Error:
		style = lipgloss.NewStyle().Foreground(colorErr)
	}

	line := fmt.Sprintf("%s %s %-8s %s",
		styleMuted.Render(entry.Time.Format("15:04:05")),
		style.Render(fmt.Sprintf("%-5s", entry.Level)),
		truncate(entry.Scope, 8),
		entry.Message)

	if lipgloss.Width(line) <= width {
		return line
	}

	return lipgloss.NewStyle().MaxWidth(width).Render(line)
}
