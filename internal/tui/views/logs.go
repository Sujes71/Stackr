package views

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/docker"
	"github.com/stackr/stackr/internal/tui/styles"
)

type LogsModel struct {
	profile    *config.Profile
	envName    string
	svcName    string // empty = all services
	viewport   viewport.Model
	lines      []string
	mu         sync.Mutex
	follow     bool
	stream     chan string
	done       chan struct{}
	doneOnce   sync.Once
	ready      bool
	statusText string
}

func NewLogs(p *config.Profile, envName, svcName string) *LogsModel {
	vp := viewport.New(80, 20)
	vp.YPosition = 0
	return &LogsModel{
		profile:  p,
		envName:  envName,
		svcName:  svcName,
		viewport: vp,
		follow:   true,
		done:     make(chan struct{}),
		lines:    []string{"── Logs ──"},
	}
}

type logLineMsg string

type logsDoneMsg struct{}

func (m *LogsModel) ProfileName() string { return m.profile.Name }

func (m *LogsModel) Init() tea.Cmd {
	if m.svcName != "" {
		m.statusText = "Live stream: " + m.svcName
	} else {
		m.statusText = "Live stream: " + m.envName
	}
	m.stream = make(chan string, 100)
	go m.startLogStream()
	return m.waitForLogLine()
}

func (m *LogsModel) startLogStream() {
	if m.svcName != "" {
		docker.Logs(m.profile.Name, m.envName, true, m.stream, m.done, m.svcName)
		return
	}
	docker.Logs(m.profile.Name, m.envName, true, m.stream, m.done)
}

func (m *LogsModel) waitForLogLine() tea.Cmd {
	return func() tea.Msg {
		line, ok := <-m.stream
		if !ok {
			return logsDoneMsg{}
		}
		return logLineMsg(line)
	}
}

func (m *LogsModel) closeDone() {
	m.doneOnce.Do(func() {
		if m.done != nil {
			close(m.done)
		}
	})
}

var logsKeys = struct {
	back, up, down, pgUp, pgDown, toggleFollow, top, bottom key.Binding
}{
	back:         key.NewBinding(key.WithKeys("esc", "backspace")),
	up:           key.NewBinding(key.WithKeys("up", "k")),
	down:         key.NewBinding(key.WithKeys("down", "j")),
	pgUp:         key.NewBinding(key.WithKeys("pgup")),
	pgDown:       key.NewBinding(key.WithKeys("pgdown")),
	toggleFollow: key.NewBinding(key.WithKeys("f")),
	top:          key.NewBinding(key.WithKeys("g", "home")),
	bottom:       key.NewBinding(key.WithKeys("G", "end")),
}

func (m *LogsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if !m.ready {
			m.viewport.Width = msg.Width - 4
			m.viewport.Height = msg.Height - 8
			m.ready = true
		} else {
			m.viewport.Width = msg.Width - 4
			m.viewport.Height = msg.Height - 8
		}
		m.renderViewport()
		return m, nil

	case logsDoneMsg:
		m.statusText = "Log stream ended"
		return m, nil

	case logLineMsg:
		m.mu.Lock()
		m.lines = append(m.lines, string(msg))
		if len(m.lines) > 500 {
			m.lines = m.lines[len(m.lines)-250:]
		}
		m.mu.Unlock()
		m.renderViewport()
		return m, m.waitForLogLine()

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, logsKeys.back):
			m.closeDone()
			return m, func() tea.Msg {
				return NavigateTo{
					Screen:      NavProfileView,
					ProfileName: m.profile.Name,
					EnvName:     m.envName,
					ServiceName: m.svcName,
				}
			}

		case key.Matches(msg, logsKeys.up):
			m.follow = false
			m.viewport.LineUp(1)

		case key.Matches(msg, logsKeys.down):
			m.follow = false
			m.viewport.LineDown(1)

		case key.Matches(msg, logsKeys.pgUp):
			m.follow = false
			m.viewport.HalfViewUp()

		case key.Matches(msg, logsKeys.pgDown):
			m.follow = false
			m.viewport.HalfViewDown()

		case key.Matches(msg, logsKeys.toggleFollow):
			m.follow = !m.follow
			if m.follow {
				m.viewport.GotoBottom()
			}

		case key.Matches(msg, logsKeys.top):
			m.follow = false
			m.viewport.GotoTop()

		case key.Matches(msg, logsKeys.bottom):
			m.follow = false
			m.viewport.GotoBottom()
		}
	}

	return m, nil
}

func (m *LogsModel) renderViewport() {
	m.mu.Lock()
	defer m.mu.Unlock()
	styled := make([]string, len(m.lines))
	for i, line := range m.lines {
		// Colorise error/warn lines
		style := lipgloss.NewStyle().Foreground(styles.ColorFg)
		if strings.Contains(line, "error") || strings.Contains(line, "Error") {
			style = lipgloss.NewStyle().Foreground(styles.ColorError)
		} else if strings.Contains(line, "warn") || strings.Contains(line, "WARN") {
			style = lipgloss.NewStyle().Foreground(styles.ColorAccent)
		}
		styled[i] = style.Render(line)
	}
	m.viewport.SetContent(strings.Join(styled, "\n"))
	if m.follow {
		m.viewport.GotoBottom()
	}
}

func (m *LogsModel) View() string {
	if !m.ready {
		return "\n\n  " + styles.StyleSubtitle.Render("Loading logs...")
	}

	m.renderViewport()

	var b strings.Builder
	b.WriteString("\n")
	if m.svcName != "" {
		b.WriteString(styles.StyleTitle.Render(
			fmt.Sprintf(" Logs — %s / %s / %s ", m.profile.Name, m.envName, m.svcName)))
	} else {
		b.WriteString(styles.StyleTitle.Render(
			fmt.Sprintf(" Logs — %s / %s ", m.profile.Name, m.envName)))
	}
	b.WriteString("\n")

	if m.statusText != "" {
		b.WriteString("  " + styles.StyleSubtitle.Render(m.statusText))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(m.viewport.View())
	b.WriteString("\n")

	followLabel := "follow: ON"
	if !m.follow {
		followLabel = styles.StyleSubtitle.Render("follow: OFF")
	}
	b.WriteString("\n")
	b.WriteString(styles.HelpBar(
		styles.HelpGroup{Label: "Scroll", Items: []styles.HelpItemResult{
			styles.HelpItem("↑ ↓", "line"),
			styles.HelpItem("pgup/dn", "page"),
			styles.HelpItem("g/G", "top/bottom"),
		}},
		styles.HelpGroup{Label: "View", Items: []styles.HelpItemResult{
			styles.HelpItem("f", strings.ReplaceAll(followLabel, "follow:", "follow tail:")),
		}},
	))
	b.WriteString("\n")
	b.WriteString(styles.BackHelp())

	return b.String()
}
