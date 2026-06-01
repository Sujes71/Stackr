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
	height      int // terminal height, set via WindowSizeMsg
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

// SetSize propagates terminal dimensions before the first WindowSizeMsg.
// h is the full terminal height; we subtract 2 for App header + footer.
func (m *HomeModel) SetSize(w, h int) {
	m.height = max(0, h-2)
}

func (m *HomeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = max(0, msg.Height-2) // App chrome (header + footer)

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
		win := m.homeWindow(len(m.profiles))
		if win.hasAbove {
			b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", win.start)))
			b.WriteString("\n")
		}
		for i := win.start; i < win.end; i++ {
			b.WriteString(m.renderProfileRow(i, m.profiles[i]))
			b.WriteString("\n")
		}
		if win.hasBelow {
			b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↓ %d more", len(m.profiles)-win.end)))
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
	var hints []string
	if len(m.profiles) > 0 {
		hints = append(hints, styles.HelpItem("↑ ↓", "navigate").Render())
		hints = append(hints, styles.HelpItem("↵", "open profile").Render())
	}
	b.WriteString(styles.StyleHelp.Render(strings.Join(hints, "  ")))

	return b.String()
}

// homeWindow calculates the visible profile range given terminal height.
func (m *HomeModel) homeWindow(total int) itemWindow {
	w := itemWindow{start: 0, end: total}
	if total == 0 || m.height <= 0 {
		return w
	}
	// Overhead: \n + help bar (~4) + \n + hints (1) = ~7 lines
	oh := 7
	if m.deleteArmed {
		oh += 1
	}
	avail := m.height - oh
	if avail <= 0 {
		return itemWindow{start: m.cursor, end: min(m.cursor+1, total), hasAbove: m.cursor > 0, hasBelow: m.cursor+1 < total}
	}
	itemHeight := 4 // each profile card is ~4 lines
	fit := avail / itemHeight
	if fit <= 0 {
		fit = 1
	}
	if total <= fit {
		return w
	}
	half := fit / 2
	start := m.cursor - half
	if start < 0 {
		start = 0
	}
	end := start + fit
	if end > total {
		end = total
		start = total - fit
	}
	return itemWindow{
		start:    start,
		end:      end,
		hasAbove: start > 0,
		hasBelow: end < total,
	}
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
