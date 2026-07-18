package views

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/tui/styles"
)

type newProfileStep int

const (
	npStepName newProfileStep = iota
	npStepDesc
	npStepEnvs // add environments
	npStepDone
)

type NewProfileModel struct {
	cfg           *config.Config
	step          newProfileStep
	name          textinput.Model
	desc          textinput.Model
	envs          []string // environment names being built
	envInput      textinput.Model
	err           string
	confirmRemove bool // double-tap d protection
	height        int  // terminal height, set via WindowSizeMsg
}

func NewNewProfile(cfg *config.Config) *NewProfileModel {
	name := textinput.New()
	name.Placeholder = "my-project"
	name.Focus()
	name.CharLimit = 30

	desc := textinput.New()
	desc.Placeholder = "Optional description"
	desc.CharLimit = 60

	envInput := textinput.New()
	envInput.Placeholder = "dev, staging, qa..."
	envInput.CharLimit = 12

	return &NewProfileModel{
		cfg:      cfg,
		step:     npStepName,
		name:     name,
		desc:     desc,
		envInput: envInput,
		envs:     []string{"local"}, // sensible default — always present
	}
}

var npKeys = struct {
	next, back, removeEnv key.Binding
}{
	// XC-008: in this wizard, tab doubles as "next step" (same as enter). This is
	// intentional for a linear wizard flow, unlike AddService where tab switches
	// fields within a step. Keeping tab=next lets users advance without reaching
	// for enter on every step.
	next: key.NewBinding(key.WithKeys("enter", "tab")),
	back: key.NewBinding(key.WithKeys("esc")),
	// XC-004: ctrl+d (not bare 'd') is used because this view has active
	// textinput fields where a bare 'd' must be passed through as typed input.
	removeEnv: key.NewBinding(key.WithKeys("ctrl+d")),
}

func (m *NewProfileModel) Init() tea.Cmd {
	return textinput.Blink
}

// SetSize stores terminal dimensions. h is the FULL terminal height;
// we subtract 5 for App chrome (header + separator + footer) so all
// views share the same contract: app.go passes full height, each view
// subtracts its own chrome internally (AUDIT-019).
func (m *NewProfileModel) SetSize(w, h int) {
	m.height = max(0, h-5)
}

func (m *NewProfileModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		// Reset double-tap confirmation on ANY key press
		resetConfirm := m.confirmRemove
		m.confirmRemove = false

		switch {
		case key.Matches(msg, npKeys.back):
			if m.step == npStepName {
				return m, func() tea.Msg { return NavigateTo{Screen: NavHome} }
			}
			m.step--
			m.confirmRemove = false
			return m, nil

		case key.Matches(msg, npKeys.next):
			return m.advance()

		case key.Matches(msg, npKeys.removeEnv):
			if m.step == npStepEnvs && m.envInput.Value() == "" && len(m.envs) > 1 {
				if resetConfirm {
					// second consecutive d → actually remove
					m.envs = m.envs[:len(m.envs)-1]
					return m, nil
				}
				// first d → arm confirmation
				m.confirmRemove = true
				return m, nil
			}
		}
		return m.updateInputs(msg)
	}
	return m, nil
}

func (m *NewProfileModel) advance() (tea.Model, tea.Cmd) {
	m.err = ""
	switch m.step {
	case npStepName:
		name := strings.TrimSpace(m.name.Value())
		if name == "" {
			return m, nil
		}
		for _, p := range m.cfg.Profiles {
			if p.Name == name {
				m.err = "a profile with that name already exists"
				return m, nil
			}
		}
		m.step = npStepDesc
		m.name.Blur()
		m.desc.Focus()
		return m, textinput.Blink

	case npStepDesc:
		m.step = npStepEnvs
		m.desc.Blur()
		m.envInput.Focus()
		return m, textinput.Blink

	case npStepEnvs:
		val := strings.TrimSpace(m.envInput.Value())
		if val != "" {
			// Text in input → add environment and stay
			if !contains(m.envs, val) {
				if len(m.envs) >= 5 {
					m.err = "maximum 5 environments allowed"
					m.envInput.Reset()
					return m, nil
				}
				// AUDIT-004: reject names with characters invalid for compose
				// identifiers, matching the validation profile_view enforces.
				if config.SanitizeComposeName(val) != val {
					m.err = "environment name may only contain letters, numbers, underscores, and hyphens"
					return m, nil
				}
				m.envs = append(m.envs, val)
			}
			m.envInput.Reset()
			m.err = ""
			return m, nil
		}
		// Empty input → proceed to save
		return m, m.saveProfile()
	}
	return m, nil
}

func (m *NewProfileModel) saveProfile() tea.Cmd {
	return func() tea.Msg {
		envs := make([]config.Environment, len(m.envs))
		for i, name := range m.envs {
			envs[i] = config.Environment{Name: name}
		}
		profile := config.Profile{
			Name:         strings.TrimSpace(m.name.Value()),
			Description:  strings.TrimSpace(m.desc.Value()),
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
			Environments: envs,
			ActiveEnv:    m.envs[0],
		}
		if err := m.cfg.AddProfile(profile); err != nil {
			return ErrorMsg{Context: "new_profile", Message: err.Error()}
		}
		return ProfileSavedMsg{Config: m.cfg}
	}
}

func (m *NewProfileModel) updateInputs(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.step {
	case npStepName:
		m.name, cmd = m.name.Update(msg)
	case npStepDesc:
		m.desc, cmd = m.desc.Update(msg)
	case npStepEnvs:
		m.envInput, cmd = m.envInput.Update(msg)
	}
	return m, cmd
}

func (m *NewProfileModel) View() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.StyleTitle.Render(" New Profile "))
	b.WriteString("\n\n")

	switch m.step {
	case npStepName:
		focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		b.WriteString("  " + focusStyle.Render("Profile name: ") + m.name.View() + "\n")
		b.WriteString(styles.StyleSubtitle.Render("  Will be used as folder name: ~/.stackr/<name>/"))

	case npStepDesc:
		focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		b.WriteString("  " + focusStyle.Render("Description: ") + styles.StyleSubtitle.Render("(optional) ") + m.desc.View() + "\n")
		b.WriteString(styles.StyleSubtitle.Render("  Optional — helps identify the profile at a glance"))

	case npStepEnvs:
		b.WriteString(styles.StyleSubtitle.Render("  Each profile can have multiple environments (local, dev, staging, ...)"))
		b.WriteString("\n")
		b.WriteString(styles.StyleSubtitle.Render("  Each environment has its own services, ports, and DB config."))
		b.WriteString("\n\n")

		b.WriteString(styles.StylePanel.Render(
			lipgloss.JoinVertical(lipgloss.Left,
				styles.StyleSubtitle.Render("Default environment"),
				"", // lipgloss join needs non-empty
				styles.EnvBadge("local"),
			),
		))
		b.WriteString("\n\n")

		if len(m.envs) > 1 {
			b.WriteString(styles.StyleInputLabel.Render("Extra environments"))
			b.WriteString("\n")
			var badges []string
			for _, env := range m.envs {
				if env == "local" {
					continue
				}
				badges = append(badges, styles.EnvBadge(env))
			}
			b.WriteString("    " + strings.Join(badges, " ") + "\n")
			b.WriteString("\n")
		}

		b.WriteString(styles.StyleSubtitle.Render("  Add more?"))
		b.WriteString("\n")
		b.WriteString("  " + m.envInput.View())
		b.WriteString("\n")
		b.WriteString(styles.StyleSubtitle.Render("  Type a name and press ↵ to add it  |  ↵ empty to continue"))
		if m.canRemoveEnv() {
			b.WriteString("\n")
			if m.confirmRemove {
				b.WriteString(styles.StyleError.Render("  Press ctrl+d again to confirm removal"))
			} else {
				b.WriteString(styles.StyleSubtitle.Render("  Press ctrl+d twice to remove the last environment"))
			}
		}
	}

	if m.err != "" {
		b.WriteString("\n")
		b.WriteString(styles.ErrorLine(m.err))
	}
	b.WriteString("\n\n")
	if m.step == npStepEnvs {
		// Line 1: contextual actions (affect env fields)
		if m.canRemoveEnv() {
			b.WriteString("      " + styles.InlineHelp(styles.HelpItem("ctrl+d ctrl+d", "remove")))
			b.WriteString("\n\n")
		}
		// Line 2: global navigation
		b.WriteString(styles.BackHelp(styles.HelpItem("↵", "next")))
	} else {
		b.WriteString(styles.BackHelp(styles.HelpItem("↵", "next")))
	}

	return b.String()
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func (m *NewProfileModel) canRemoveEnv() bool {
	return m.step == npStepEnvs && m.envInput.Value() == "" && len(m.envs) > 1
}
