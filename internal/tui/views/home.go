package views

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/tui/styles"
	"gopkg.in/yaml.v3"
)

type HomeModel struct {
	cfg          *config.Config
	cursor       int
	profiles     []config.Profile
	deleteArmed  bool
	deleting     string         // non-empty → typed confirmation for deletion of this profile
	confirmInput textinput.Model
	height       int // terminal height, set via WindowSizeMsg

	// ── Export / Import ──
	showExport  bool
	exportYAML  string
	showImport  bool
	importInput textinput.Model
	importPath  string
	importErr   string
	importing   bool
}

type importResultMsg struct {
	profiles []config.Profile
	err      string
}

func NewHome(cfg *config.Config) *HomeModel {
	ti := textinput.New()
	ti.Prompt = ""

	importTi := textinput.New()
	importTi.Prompt = ""
	importTi.Placeholder = "~/path/to/profile.yaml"
	importTi.CharLimit = 100
	importTi.Width = 40

	return &HomeModel{
		cfg:          cfg,
		profiles:     cfg.Profiles,
		confirmInput: ti,
		importInput:  importTi,
	}
}

var homeKeys = struct {
	up, down, enter, new, delete, export, import_ key.Binding
}{
	up:      key.NewBinding(key.WithKeys("up", "k")),
	down:    key.NewBinding(key.WithKeys("down", "j")),
	enter:   key.NewBinding(key.WithKeys("enter")),
	new:     key.NewBinding(key.WithKeys("n")),
	delete:  key.NewBinding(key.WithKeys("d")),
	export:  key.NewBinding(key.WithKeys("x")),
	import_: key.NewBinding(key.WithKeys("i")),
}

func (m *HomeModel) Init() tea.Cmd { return nil }

func (m *HomeModel) Cursor() int     { return m.cursor }
func (m *HomeModel) SetCursor(c int) { m.cursor = c }

// SetSize propagates terminal dimensions before the first WindowSizeMsg.
// h is the full terminal height; we subtract 5 for App chrome.
func (m *HomeModel) SetSize(w, h int) {
	m.height = max(0, h-5)
}

func (m *HomeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = max(0, msg.Height-5) // App chrome

	case importResultMsg:
		m.importing = false
		if msg.err != "" {
			m.importErr = msg.err
			return m, nil
		}
		m.profiles = msg.profiles
		m.showImport = false
		m.importInput.Reset()
		m.importInput.Blur()
		m.importErr = ""
		return m, nil

	case tea.KeyMsg:
		// ── Export overlay: any key dismisses ──
		if m.showExport {
			m.showExport = false
			m.exportYAML = ""
			return m, nil
		}

		// ── Import input mode ──
		if m.showImport {
			switch {
			case msg.String() == "esc":
				m.showImport = false
				m.importInput.Reset()
				m.importInput.Blur()
				m.importErr = ""
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
				path := strings.TrimSpace(m.importInput.Value())
				if path == "" {
					m.showImport = false
					m.importInput.Reset()
					m.importInput.Blur()
					return m, nil
				}
				if m.importing {
					return m, nil
				}
				// Expand ~
				if strings.HasPrefix(path, "~/") {
					home, _ := os.UserHomeDir()
					path = filepath.Join(home, path[2:])
				}
				m.importing = true
				m.importErr = ""
				return m, func() tea.Msg {
					data, err := os.ReadFile(path)
					if err != nil {
						return importResultMsg{err: err.Error()}
					}
					var p config.Profile
					if err := yaml.Unmarshal(data, &p); err != nil {
						return importResultMsg{err: "Invalid profile YAML: " + err.Error()}
					}
					if err := m.cfg.AddProfile(p); err != nil {
						return importResultMsg{err: err.Error()}
					}
					return importResultMsg{profiles: m.cfg.Profiles}
				}
			default:
				var cmd tea.Cmd
				m.importInput, cmd = m.importInput.Update(msg)
				return m, cmd
			}
		}
		// If typed confirmation is active, handle in its own mode.
		if m.deleting != "" {
			switch {
			case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
				m.deleting = ""
				m.confirmInput.Reset()
				m.confirmInput.Blur()
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
				if m.confirmInput.Value() == m.deleting {
					name := m.deleting
					m.deleting = ""
					m.confirmInput.Reset()
					m.confirmInput.Blur()
					_ = m.cfg.DeleteProfile(name)
					m.profiles = m.cfg.Profiles
					if m.cursor >= len(m.profiles) && m.cursor > 0 {
						m.cursor--
					}
				} else {
					m.confirmInput.Reset()
				}
				return m, nil
			default:
				var cmd tea.Cmd
				m.confirmInput, cmd = m.confirmInput.Update(msg)
				return m, cmd
			}
		}

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
			m.deleting = name
			m.confirmInput.Reset()
			m.confirmInput.Focus()
			return m, textinput.Blink

		case key.Matches(msg, homeKeys.export):
			if len(m.profiles) == 0 || m.cursor >= len(m.profiles) {
				break
			}
			p := m.profiles[m.cursor]
			data, err := yaml.Marshal(p)
			if err != nil {
				m.exportYAML = "Error: " + err.Error()
			} else {
				m.exportYAML = string(data)
			}
			m.showExport = true
			return m, nil

		case key.Matches(msg, homeKeys.import_):
			m.showImport = true
			m.importInput.Reset()
			m.importInput.Focus()
			m.importErr = ""
			return m, textinput.Blink
		default:
			m.deleteArmed = false
			m.deleting = ""
		}
	}
	return m, nil
}

func (m *HomeModel) View() string {
	var b strings.Builder

	b.WriteString("\n")

	// ── Export overlay ──
	if m.showExport {
		b.WriteString(styles.StylePanelFocused.Render(
			styles.StyleHeader.Render(m.profiles[m.cursor].Name) + "\n\n" +
				styles.StyleSubtitle.Render(m.exportYAML),
		))
		b.WriteString("\n\n")
		b.WriteString(styles.InlineHelp(
			styles.HelpItem("esc", "dismiss"),
		))
		b.WriteString("\n")
		return b.String()
	}

	// ── Import overlay ──
	if m.showImport {
		b.WriteString(styles.StyleInputLabel.Render("  Import profile from path:"))
		b.WriteString("\n\n")
		b.WriteString("  " + m.importInput.View())
		b.WriteString("\n\n")
		if m.importing {
			b.WriteString(styles.StyleSubtitle.Render("  Importing…"))
			b.WriteString("\n\n")
		}
		if m.importErr != "" {
			b.WriteString(styles.StyleError.Render("  " + m.importErr))
			b.WriteString("\n\n")
		}
		b.WriteString(styles.InlineHelp(
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("↵", "import"),
		))
		b.WriteString("\n")
		return b.String()
	}

	// Typed confirmation for profile deletion
	if m.deleting != "" {
		b.WriteString(styles.StylePanelError.Render(
			styles.StyleError.Render("  ⚠ This will permanently delete the profile and ALL its data"),
		))
		b.WriteString("\n\n")
		nameStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		b.WriteString(styles.StyleSubtitle.Render(
			fmt.Sprintf("  Type %s to confirm deletion:", nameStyle.Render(fmt.Sprintf("%q", m.deleting))),
		))
		b.WriteString("\n\n")
		b.WriteString("  " + m.confirmInput.View())
		b.WriteString("\n\n")
		b.WriteString(styles.InlineHelp(
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("↵", "confirm"),
		))
		b.WriteString("\n")
		return b.String()
	}

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
		mgmtItems = append(mgmtItems, styles.HelpItem("x", "export"))
		mgmtItems = append(mgmtItems, styles.HelpItem("i", "import"))
	}
	b.WriteString("\n")
	b.WriteString(styles.HelpBar(
		styles.HelpGroup{Label: "Manage", Items: mgmtItems},
	))
	b.WriteString("\n\n")
	if len(m.profiles) > 0 {
		b.WriteString("  " + styles.InlineHelp(
			styles.HelpItem("↑ ↓", "navigate"),
			styles.HelpItem("↵", "open"),
		))
	}

	return b.String()
}

// homeWindow calculates the visible profile range given terminal height.
func (m *HomeModel) homeWindow(total int) itemWindow {
	w := itemWindow{start: 0, end: total}
	if total == 0 || m.height <= 0 {
		return w
	}
	// Overhead: \n + blank line + help bar + \n + hints = ~10 lines
	oh := 10
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
