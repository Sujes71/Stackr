package views

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/tui/styles"
	"github.com/stackr/stackr/internal/tui/whichkey"
	"gopkg.in/yaml.v3"
)

type HomeModel struct {
	cfg           *config.Config
	errorRegistry *ErrorRegistry
	cursor        int
	profiles      []config.Profile
	deleteArmed   bool
	armGeneration int // incremented on each arm; timer only fires if generation matches
	deleting      string // non-empty → typed confirmation for deletion of this profile
	confirmInput  textinput.Model
	lastResult    string
	height        int // terminal height, set via WindowSizeMsg
	width         int // terminal width, set via WindowSizeMsg

	// ── Export / Import ──
	showImport        bool
	importFiles       []string // available export files not yet imported
	importCursor      int
	importDeleteArmed bool
	importing         bool

	// ── Pending Ops / Spinner ──
	pendingOps   map[string]string // key → "importing"
	spinnerFrame int
}

func NewHome(cfg *config.Config, errorRegistry *ErrorRegistry) *HomeModel {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 100

	return &HomeModel{
		cfg:           cfg,
		errorRegistry: errorRegistry,
		profiles:      cfg.Profiles,
		confirmInput:  ti,
		pendingOps:    make(map[string]string),
	}
}

var homeKeys = struct {
	up, down, enter, new, delete, export, import_ key.Binding
}{
	up:      key.NewBinding(key.WithKeys("up")),
	down:    key.NewBinding(key.WithKeys("down")),
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
	m.width = w
	m.height = max(0, h-5)
}

// WhichKeyCommands returns the command catalog for the Home screen.
func (m *HomeModel) WhichKeyCommands() []whichkey.CommandGroup {
	profileItems := []whichkey.CommandKey{
		{Key: "n", Desc: "new profile"},
		{Key: "i", Desc: "import"},
	}
	if len(m.profiles) > 0 {
		profileItems = append(profileItems,
			whichkey.CommandKey{Key: "x", Desc: "export"},
			whichkey.CommandKey{Key: "d", Desc: "delete"},
		)
	}
	return []whichkey.CommandGroup{
		{Items: profileItems},
	}
}

// IsTextInputActive returns true when a text input has focus on this screen.
func (m *HomeModel) IsTextInputActive() bool {
	return m.deleting != "" || m.showImport
}

func (m *HomeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Spinner tick — handle at the top
	if _, ok := msg.(SpinnerTickMsg); ok {
		if len(m.pendingOps) > 0 {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return SpinnerTickMsg{}
			})
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = max(0, msg.Height-5) // App chrome

	case importResultMsg:
		delete(m.pendingOps, "import")
		m.importing = false
		if msg.err != "" {
			return m, func() tea.Msg {
				return ErrorMsg{Context: "import", Message: msg.err}
			}
		}
		m.profiles = msg.profiles
		m.showImport = false
		return m, nil

	case ClearActionMsg:
		m.lastResult = ""

	case armDisarmMsg:
		if msg.generation == m.armGeneration {
			m.deleteArmed = false
		}
		return m, nil

	case tea.KeyMsg:
		// ── Import overlay: select from available exports ──
		if m.showImport {
			switch {
			case msg.String() == "esc":
				m.showImport = false
				m.importDeleteArmed = false
				return m, nil
			case msg.String() == "up":
				if m.importCursor > 0 {
					m.importCursor--
				}
				return m, nil
			case msg.String() == "down":
				if m.importCursor < len(m.importFiles)-1 {
					m.importCursor++
				}
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
				if m.importing || m.importCursor >= len(m.importFiles) {
					return m, nil
				}
				name := m.importFiles[m.importCursor]
				path := filepath.Join(config.StackrDir(), "exports", name+".yaml")
				m.importing = true
				m.pendingOps["import"] = "importing"
				return m, tea.Batch(
					func() tea.Msg {
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
						// Regenerate compose files for imported profile
						for _, env := range p.Environments {
							if len(env.Services) > 0 {
								config.GenerateCompose(p, env.Name)
							}
						}
						return importResultMsg{profiles: m.cfg.Profiles}
					},
					spinnerCmd(),
				)
			case key.Matches(msg, homeKeys.delete):
				if !m.importDeleteArmed {
					m.importDeleteArmed = true
					return m, nil
				}
				m.importDeleteArmed = false
				name := m.importFiles[m.importCursor]
				path := filepath.Join(config.StackrDir(), "exports", name+".yaml")
				if err := os.Remove(path); err != nil {
					return m, func() tea.Msg {
						return ErrorMsg{Context: "import", Message: "failed to delete: " + err.Error()}
					}
				}
				m.importFiles = append(m.importFiles[:m.importCursor], m.importFiles[m.importCursor+1:]...)
				if m.importCursor >= len(m.importFiles) && m.importCursor > 0 {
					m.importCursor--
				}
				if len(m.importFiles) == 0 {
					m.showImport = false
					return m, nil
				}
				return m, nil
			default:
				m.importDeleteArmed = false
			}
		}
		// If typed confirmation is active, handle in its own mode.
		if m.deleting != "" {
			switch {
			case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
				m.deleting = ""
				m.lastResult = ""
				m.confirmInput.Reset()
				m.confirmInput.Blur()
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
				if m.confirmInput.Value() == m.deleting {
					name := m.deleting
					m.deleting = ""
					m.lastResult = ""
					m.confirmInput.Reset()
					m.confirmInput.Blur()
					_ = m.cfg.DeleteProfile(name)
					m.profiles = m.cfg.Profiles
					if m.cursor >= len(m.profiles) && m.cursor > 0 {
						m.cursor--
					}
				} else {
					// AUDIT-034: surface a clear error instead of silently resetting.
					m.lastResult = styles.ErrorLine("name does not match — type " + fmt.Sprintf("%q", m.deleting) + " to confirm")
					m.confirmInput.Reset()
				}
				return m, nil
			default:
				var cmd tea.Cmd
				m.confirmInput, cmd = m.confirmInput.Update(msg)
				return m, cmd
			}
		}

		// ── Block keys while an operation is in progress ──
		if len(m.pendingOps) > 0 {
			break
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
				m.deleteArmed = false
				return m, func() tea.Msg {
					return NavigateTo{
						Screen:      NavProfileView,
						ProfileName: m.profiles[m.cursor].Name,
					}
				}
			}
		case key.Matches(msg, homeKeys.new):
			m.deleteArmed = false
			return m, func() tea.Msg {
				return NavigateTo{Screen: NavNewProfile}
			}
		case key.Matches(msg, homeKeys.delete):
			if len(m.profiles) == 0 {
				break
			}
		if !m.deleteArmed {
			m.armGeneration++
			m.deleteArmed = true
			return m, armDisarmAfter(m.armGeneration)
		}
			m.deleteArmed = false
			name := m.profiles[m.cursor].Name
			m.deleting = name
			m.confirmInput.Reset()
			m.confirmInput.Focus()
			return m, textinput.Blink

		case key.Matches(msg, homeKeys.export):
			m.deleteArmed = false
			m.importDeleteArmed = false
			if len(m.profiles) == 0 || m.cursor >= len(m.profiles) {
				break
			}
			p := m.profiles[m.cursor]
			exportDir := filepath.Join(config.StackrDir(), "exports")
			os.MkdirAll(exportDir, 0755)
			path := filepath.Join(exportDir, p.Name+".yaml")
			data, err := yaml.Marshal(p)
			if err != nil {
				m.lastResult = styles.ErrorLine("export: " + err.Error())
			} else if err := os.WriteFile(path, data, 0644); err != nil {
				m.lastResult = styles.ErrorLine("export: " + err.Error())
			} else {
				m.lastResult = "  " + styles.StyleSuccess.Render("✓") + " Exported to " + path
			}
			return m, ClearActionAfter(3 * time.Second)

		case key.Matches(msg, homeKeys.import_):
			// Scan exports folder for available profiles
			exportDir := filepath.Join(config.StackrDir(), "exports")
			entries, _ := os.ReadDir(exportDir)
			m.importFiles = nil
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
					name := strings.TrimSuffix(e.Name(), ".yaml")
					// Skip if already imported
					if _, exists := m.cfg.GetProfile(name); !exists {
						m.importFiles = append(m.importFiles, name)
					}
				}
			}
			if len(m.importFiles) == 0 {
				m.lastResult = "  " + styles.StyleSubtitle.Render("No profiles available to import. Export one first with 'x'.")
				return m, ClearActionAfter(3 * time.Second)
			}
			m.deleteArmed = false
			m.showImport = true
			m.importCursor = 0
			return m, nil

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

	// ── Import overlay ──
	if m.showImport {
		b.WriteString(styles.StyleInputLabel.Render("  Select profile to import:"))
		b.WriteString("\n\n")
		for i, name := range m.importFiles {
			if i == m.importCursor {
				b.WriteString("  > " + lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true).Render(name) + "\n")
			} else {
				b.WriteString("    " + name + "\n")
			}
		}
		b.WriteString("\n")
		if m.importDeleteArmed {
			b.WriteString(styles.StyleError.Render("  Press d again to confirm deletion"))
			b.WriteString("\n")
		}
		b.WriteString(styles.InlineHelp(
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("d d", "delete"),
			styles.HelpItem("↵", "import"),
		))
		b.WriteString("\n")
		if m.importing {
			b.WriteString(opsRenderSpinnerDot(m.spinnerFrame, "Importing") + "\n")
		}
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
		if m.lastResult != "" {
			b.WriteString("\n\n" + m.lastResult)
		}
		b.WriteString("\n\n")
		b.WriteString(styles.InlineHelp(
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("↵", "confirm"),
		))
		b.WriteString("\n")
		return b.String()
	}

	if len(m.profiles) == 0 {
		b.WriteString(styles.EmptyState("No profiles yet. Press n to create one.", m.width))
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
	if m.lastResult != "" {
		b.WriteString("\n" + m.lastResult + "\n")
	}
	b.WriteString("\n")

	return b.String()
}

// homeWindow calculates the visible profile range given terminal height.
func (m *HomeModel) homeWindow(total int) itemWindow {
	w := itemWindow{start: 0, end: total}
	if total == 0 || m.height <= 0 {
		return w
	}
	// Overhead: \n + line 1 + blank line + line 2 nav = ~5 lines
	oh := 5
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
