package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/docker"
	"github.com/stackr/stackr/internal/tui/styles"
)

type ProfileViewModel struct {
	cfg         *config.Config
	profile     *config.Profile
	activeEnv   int
	cursor      int
	statuses    []docker.ServiceStatus
	statusErr   string
	loading     bool
	lastAction  string
	deleteArmed bool
}

func NewProfileView(cfg *config.Config, p *config.Profile) *ProfileViewModel {
	m := &ProfileViewModel{
		cfg:     cfg,
		profile: p,
	}
	// set activeEnv to the profile's saved active env
	for i, env := range p.Environments {
		if env.Name == p.ActiveEnv {
			m.activeEnv = i
			break
		}
	}
	return m
}

func (m *ProfileViewModel) ProfileName() string {
	if m.profile != nil {
		return m.profile.Name
	}
	return ""
}

type statusLoadedMsg struct {
	statuses []docker.ServiceStatus
	err      string
}

type actionDoneMsg struct {
	label string
	err   error
}

type clearActionMsg struct{}

func clearActionAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return clearActionMsg{}
	})
}

func (m *ProfileViewModel) loadStatus() tea.Cmd {
	return func() tea.Msg {
		if len(m.profile.Environments) == 0 {
			return statusLoadedMsg{}
		}
		env := m.profile.Environments[m.activeEnv]
		ss, err := docker.Status(m.profile.Name, env.Name)
		errStr := ""
		if err != nil {
			errStr = err.Error()
		}
		return statusLoadedMsg{statuses: ss, err: errStr}
	}
}

var profileKeys = struct {
	back, up, down, enter, start, stop, logs, addSvc, svcDelete, dbOps, switchEnv key.Binding
}{
	back:      key.NewBinding(key.WithKeys("esc", "backspace")),
	up:        key.NewBinding(key.WithKeys("up", "k")),
	down:      key.NewBinding(key.WithKeys("down", "j")),
	enter:     key.NewBinding(key.WithKeys("enter")),
	start:     key.NewBinding(key.WithKeys("s")),
	stop:      key.NewBinding(key.WithKeys("x")),
	logs:      key.NewBinding(key.WithKeys("l")),
	addSvc:    key.NewBinding(key.WithKeys("a")),
	svcDelete: key.NewBinding(key.WithKeys("d")),
	dbOps:     key.NewBinding(key.WithKeys("o")),
	switchEnv: key.NewBinding(key.WithKeys("tab")),
}

func (m *ProfileViewModel) Init() tea.Cmd {
	return m.loadStatus()
}

func (m *ProfileViewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case statusLoadedMsg:
		m.loading = false
		m.statuses = msg.statuses
		m.statusErr = msg.err

	case actionDoneMsg:
		m.loading = false
		if msg.err != nil {
			m.lastAction = "✗ " + msg.err.Error()
		} else {
			m.lastAction = "✓ " + msg.label
		}
		// Auto-clear action message after 3 seconds
		cmds := []tea.Cmd{m.loadStatus(), clearActionAfter(3 * time.Second)}
		return m, tea.Batch(cmds...)

	case clearActionMsg:
		m.lastAction = ""

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, profileKeys.back):
			return m, func() tea.Msg { return NavigateTo{Screen: NavHome} }

		case key.Matches(msg, profileKeys.switchEnv):
			if len(m.profile.Environments) > 1 {
				m.activeEnv = (m.activeEnv + 1) % len(m.profile.Environments)
				m.statuses = nil
				return m, m.loadStatus()
			}

		case key.Matches(msg, profileKeys.up):
			if m.cursor > 0 {
				m.cursor--
			}

		case key.Matches(msg, profileKeys.down):
			envSvcs := m.currentEnvServices()
			if m.cursor < len(envSvcs)-1 {
				m.cursor++
			}

		case key.Matches(msg, profileKeys.enter):
			svcs := m.currentEnvServices()
			if len(svcs) > 0 && m.cursor < len(svcs) {
				svc := svcs[m.cursor]
				envName := m.profile.Environments[m.activeEnv].Name
				containerName := fmt.Sprintf("stackr-%s-%s-%s", m.profile.Name, envName, svc.Name)
				isRunning := false
				for _, st := range m.statuses {
					if st.Name == containerName && st.State == "running" {
						isRunning = true
						break
					}
				}
				m.loading = true
				if isRunning {
					m.lastAction = "Stopping " + svc.Name + "..."
					return m, func() tea.Msg {
						err := docker.DownService(m.profile.Name, envName, svc.Name)
						return actionDoneMsg{label: svc.Name + " stopped", err: err}
					}
				}
				m.lastAction = "Starting " + svc.Name + "..."
				return m, func() tea.Msg {
					err := docker.UpService(m.profile.Name, envName, svc.Name)
					return actionDoneMsg{label: svc.Name + " started", err: err}
				}
			}

		case key.Matches(msg, profileKeys.start):
			env := m.currentEnv()
			if env != nil {
				m.loading = true
				m.lastAction = "Starting " + env.Name + "..."
				return m, func() tea.Msg {
					err := docker.Up(m.profile.Name, env.Name)
					return actionDoneMsg{label: "Stack started", err: err}
				}
			}

		case key.Matches(msg, profileKeys.stop):
			env := m.currentEnv()
			if env != nil {
				m.loading = true
				m.lastAction = "Stopping " + env.Name + "..."
				return m, func() tea.Msg {
					err := docker.Down(m.profile.Name, env.Name)
					return actionDoneMsg{label: "Stack stopped", err: err}
				}
			}

		case key.Matches(msg, profileKeys.logs):
			env := m.currentEnv()
			if env != nil {
				return m, func() tea.Msg {
					return NavigateTo{
						Screen:      NavLogs,
						ProfileName: m.profile.Name,
						EnvName:     env.Name,
					}
				}
			}

		case key.Matches(msg, profileKeys.addSvc):
			env := m.currentEnv()
			envName := "local"
			if env != nil {
				envName = env.Name
			}
			return m, func() tea.Msg {
				return NavigateTo{
					Screen:      NavAddService,
					ProfileName: m.profile.Name,
					EnvName:     envName,
				}
			}

		case key.Matches(msg, profileKeys.svcDelete):
			env := m.currentEnv()
			svcs := m.currentEnvServices()
			if env == nil || len(svcs) <= 1 || m.cursor >= len(svcs) {
				break
			}
			if !m.deleteArmed {
				m.deleteArmed = true
				return m, nil // return early so disarm at bottom doesn't fire
			}
			// Second d: delete service at cursor
			m.deleteArmed = false
			svc := svcs[m.cursor]
			m.lastAction = "Deleting " + svc.Name + "..."
			go func() {
				_ = docker.Down(m.profile.Name, env.Name)
			}()
			updated, err := m.cfg.DeleteService(m.profile.Name, env.Name, svc.Name)
			if err != nil {
				m.lastAction = "✗ " + err.Error()
			} else {
				m.profile = updated
				if _, err := config.GenerateCompose(*m.profile, env.Name); err != nil {
					m.lastAction = "✗ compose gen: " + err.Error()
				} else {
					m.lastAction = "✓ Deleted " + svc.Name
				}
				remaining := m.currentEnvServices()
				if m.cursor >= len(remaining) && m.cursor > 0 {
					m.cursor--
				}
			}
			return m, m.loadStatus()

		case key.Matches(msg, profileKeys.dbOps):
			svcs := m.currentEnvServices()
			if m.cursor < len(svcs) && m.selectedDBType() != "" {
				env := m.currentEnv()
				svc := svcs[m.cursor]
				return m, func() tea.Msg {
					return NavigateTo{
						Screen:      NavDBOps,
						ProfileName: m.profile.Name,
						EnvName:     env.Name,
						ServiceName: svc.Name,
					}
				}
			}
		}
	}
	m.deleteArmed = false
	return m, nil
}

func (m *ProfileViewModel) selectedDBType() string {
	svcs := m.currentEnvServices()
	if m.cursor >= len(svcs) {
		return ""
	}
	return svcs[m.cursor].DBType()
}

func (m *ProfileViewModel) currentEnv() *config.Environment {
	if len(m.profile.Environments) == 0 {
		return nil
	}
	return &m.profile.Environments[m.activeEnv]
}

func (m *ProfileViewModel) currentEnvServices() []config.Service {
	env := m.currentEnv()
	if env == nil {
		return nil
	}
	return env.Services
}

func (m *ProfileViewModel) View() string {
	var b strings.Builder
	b.WriteString("\n")

	// Env tabs
	b.WriteString(m.renderEnvTabs())
	b.WriteString("\n\n")

	env := m.currentEnv()
	if env == nil {
		b.WriteString(styles.StyleSubtitle.Render("  No environments configured. Press 'a' to add a service."))
		b.WriteString("\n")
	} else {
		b.WriteString(m.renderServices(env))
	}

	if m.lastAction != "" {
		b.WriteString("\n")
		if strings.HasPrefix(m.lastAction, "✗") {
			b.WriteString(styles.StyleError.Render("  " + m.lastAction))
		} else {
			b.WriteString(styles.StyleSuccess.Render("  " + m.lastAction))
		}
		b.WriteString("\n")
	}

	if m.deleteArmed {
		if m.lastAction == "" {
			b.WriteString("\n")
		}
		b.WriteString(styles.StyleError.Render("  Press d again to confirm removal"))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	var helpItems []string
	helpItems = append(helpItems, styles.HelpItem("tab", "switch env"))
	svcs := m.currentEnvServices()
	if len(svcs) > 0 {
		helpItems = append(helpItems, styles.HelpItem("↵", "start/stop"))
		helpItems = append(helpItems, styles.HelpItem("s", "stack"))
		hasRunning := false
		for _, st := range m.statuses {
			if st.State == "running" {
				hasRunning = true
				break
			}
		}
		if hasRunning {
			helpItems = append(helpItems, styles.HelpItem("x", "stop"))
		}
		helpItems = append(helpItems, styles.HelpItem("l", "logs"))
		if len(svcs) > 1 {
			helpItems = append(helpItems, styles.HelpItem("d d", "delete"))
		}
	}
	helpItems = append(helpItems, styles.HelpItem("a", "add service"))
	if m.selectedDBType() != "" {
		helpItems = append(helpItems, styles.HelpItem("o", "db ops"))
	}
	helpItems = append(helpItems, styles.HelpItem("esc", "back"))
	b.WriteString(styles.StyleHelp.Render(strings.Join(helpItems, "   ")))

	return b.String()
}

func (m *ProfileViewModel) renderEnvTabs() string {
	tabs := make([]string, len(m.profile.Environments))
	for i, env := range m.profile.Environments {
		if i == m.activeEnv {
			tabs[i] = styles.StyleBadgeEnv.Render(" " + env.Name + " ")
		} else {
			tabs[i] = styles.StyleBadgeEnvInactive.Render(" " + env.Name + " ")
		}
	}
	return "  " + strings.Join(tabs, "  ")
}

func (m *ProfileViewModel) renderServices(env *config.Environment) string {
	if len(env.Services) == 0 {
		return styles.StyleSubtitle.Render("  No services yet.\n")
	}

	// build status map
	statusMap := map[string]docker.ServiceStatus{}
	for _, s := range m.statuses {
		statusMap[s.Name] = s
	}

	var rows []string
	for i, svc := range env.Services {
		containerName := fmt.Sprintf("stackr-%s-%s-%s", m.profile.Name, env.Name, svc.Name)
		st := statusMap[containerName]

		dot := styles.Dot(st.State)
		if st.State == "" {
			dot = styles.Dot("stopped")
		}

		name := styles.StyleHeader.Render(svc.Name)
		image := styles.StyleCode.Render(svc.FullImage())

		ports := ""
		if len(svc.Ports) > 0 {
			ps := make([]string, len(svc.Ports))
			for j, p := range svc.Ports {
				ps[j] = p.String()
			}
			ports = styles.StyleSubtitle.Render("  " + strings.Join(ps, ", "))
		}

		line1 := lipgloss.JoinHorizontal(lipgloss.Center,
			dot, "  ", name, "  ", image, ports,
		)

		state := st.State
		if state == "" {
			state = "not running"
		}
		line2 := styles.StyleSubtitle.Render("     " + state)

		row := lipgloss.JoinVertical(lipgloss.Left, line1, line2)

		if i == m.cursor {
			rows = append(rows, styles.StylePanelFocused.Width(66).Render(row))
		} else {
			rows = append(rows, styles.StylePanel.Width(66).Render(row))
		}
	}
	return strings.Join(rows, "\n")
}
