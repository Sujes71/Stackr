package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/tui/styles"
)

type HomeModel struct {
	cfg         *config.Config
	cursor      int
	profiles    []config.Profile
	deleteArmed bool
}

func NewHome(cfg *config.Config) *HomeModel {
	return &HomeModel{
		cfg:      cfg,
		profiles: cfg.Profiles,
	}
}

var homeKeys = struct {
	up, down, enter, new, delete key.Binding
}{
	up:     key.NewBinding(key.WithKeys("up", "k")),
	down:   key.NewBinding(key.WithKeys("down", "j")),
	enter:  key.NewBinding(key.WithKeys("enter")),
	new:    key.NewBinding(key.WithKeys("n")),
	delete: key.NewBinding(key.WithKeys("d")),
}

func (m *HomeModel) Init() tea.Cmd { return nil }

func (m *HomeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, homeKeys.up):
			if m.cursor > 0 {
				m.cursor--
			}
		case key.Matches(msg, homeKeys.down):
			if m.cursor < len(m.profiles)-1 {
				m.cursor++
			}
		case key.Matches(msg, homeKeys.enter):
			if len(m.profiles) > 0 {
				return m, func() tea.Msg {
					return NavigateTo{
						Screen:      NavProfileView,
						ProfileName: m.profiles[m.cursor].Name,
					}
				}
			}
		case key.Matches(msg, homeKeys.new):
			return m, func() tea.Msg {
				return NavigateTo{Screen: NavNewProfile}
			}
		case key.Matches(msg, homeKeys.delete):
			if len(m.profiles) == 0 {
				break
			}
			if !m.deleteArmed {
				m.deleteArmed = true
				break
			}
			m.deleteArmed = false
			name := m.profiles[m.cursor].Name
			_ = m.cfg.DeleteProfile(name)
			m.profiles = m.cfg.Profiles
			if m.cursor >= len(m.profiles) && m.cursor > 0 {
				m.cursor--
			}
		default:
			m.deleteArmed = false
		}
	}
	return m, nil
}

func (m *HomeModel) View() string {
	var b strings.Builder

	b.WriteString("\n")

	if len(m.profiles) == 0 {
		empty := styles.StylePanel.Render(
			styles.StyleSubtitle.Render("No profiles yet. Press n to create one."),
		)
		b.WriteString(lipgloss.PlaceHorizontal(60, lipgloss.Center, empty))
		b.WriteString("\n")
	} else {
		for i, p := range m.profiles {
			b.WriteString(m.renderProfileRow(i, p))
			b.WriteString("\n")
		}
	}

	if m.deleteArmed {
		b.WriteString(styles.StyleError.Render("  Press d again to confirm deletion"))
		b.WriteString("\n")
	}
	var mgmtItems []styles.HelpItemResult
	mgmtItems = append(mgmtItems, styles.HelpItem("n", "new profile"))
	if len(m.profiles) > 0 {
		mgmtItems = append(mgmtItems, styles.HelpItem("d d", "delete"))
	}
	b.WriteString(styles.HelpBar(
		styles.HelpGroup{Label: "Manage", Items: mgmtItems},
	))
	b.WriteString("\n")
	b.WriteString(styles.StyleHelp.Render(styles.HelpItem("↑ ↓", "navigate").Render() + "  " + styles.HelpItem("↵", "next").Render()))

	return b.String()
}

func (m *HomeModel) renderProfileRow(idx int, p config.Profile) string {
	active := idx == m.cursor

	// count services across all environments
	svcCount := 0
	for _, env := range p.Environments {
		svcCount += len(env.Services)
	}

	envNames := make([]string, 0, len(p.Environments))
	for _, env := range p.Environments {
		envNames = append(envNames, styles.EnvBadge(env.Name))
	}
	envList := strings.Join(envNames, " ")

	title := styles.StyleHeader.Render(p.Name)
	if p.Description != "" {
		title += styles.StyleSubtitle.Render("  " + p.Description)
	}
	meta := styles.StyleSubtitle.Render(fmt.Sprintf("  %d service(s)  ", svcCount)) + envList

	// Measure visible width so the panel is wide enough
	contentW := lipgloss.Width(title)
	if w := lipgloss.Width(meta); w > contentW {
		contentW = w
	}
	panelW := contentW + 4 // 2 border + 2 padding
	if panelW < 64 {
		panelW = 64
	}

	row := lipgloss.JoinVertical(lipgloss.Left, title, meta)
	if active {
		return styles.StylePanelFocused.Width(panelW).Render(row)
	}
	return styles.StylePanel.Width(panelW).Render(row)
}
