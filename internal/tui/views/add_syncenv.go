package views

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/tui/styles"
)

// ── Step enum ─────────────────────────────────────────────────────────────────

type syncEnvStep int

const (
	syncEnvStepName syncEnvStep = iota
	syncEnvStepCredentials
	syncEnvStepReview
)

// Credentials focus order. Each field has a fixed index so we can cycle
// and also skip AuthDB when the DB type is not mongo.
const (
	credFocusHost = iota // 0
	credFocusPort        // 1
	credFocusUser        // 2
	credFocusPass        // 3
	credFocusDB          // 4
	credFocusAuthDB      // 5 — mongo only
)

// ── Messages ──────────────────────────────────────────────────────────────────

// wizardSavedMsg is sent by save() when the user confirms the review step.
type wizardSavedMsg struct {
	env      *config.SyncEnv
	editMode bool
	origName string
}

// ── Model ─────────────────────────────────────────────────────────────────────

type AddSyncEnvModel struct {
	cfg         *config.Config
	profile     *config.Profile
	envName     string
	serviceName string
	serviceIdx  int
	dbType      string // derived from service DBType()

	step syncEnvStep

	// Mode
	editMode bool
	origName string // original name in edit mode (NOT mutable)

	// Step 1: name
	nameInput textinput.Model
	nameErr   string

	// Step 2: credentials
	hostInput   textinput.Model
	portInput   textinput.Model
	userInput   textinput.Model
	passInput   textinput.Model
	dbInput     textinput.Model
	authDBInput textinput.Model // mongo only
	credFocus   int
	credErr     string
	maxFocus    int // 4 (postgres/mysql/mssql) or 5 (mongo)

	// Step 3: review
	confirmed bool

	// Error state (shared)
	err string
}

// ── Key bindings ──────────────────────────────────────────────────────────────

var (
	wizardBack = key.NewBinding(key.WithKeys("esc"))
	wizardNext = key.NewBinding(key.WithKeys("enter"))
	wizardTab  = key.NewBinding(key.WithKeys("tab"))
)

// ── Constructor ───────────────────────────────────────────────────────────────

func NewAddSyncEnv(
	cfg *config.Config, p *config.Profile,
	envName, serviceName string,
	addMode bool, existing *config.SyncEnv,
) *AddSyncEnvModel {
	// Resolve service and DB type from the profile
	var dbType string
	var serviceIdx int
	for i := range p.Environments {
		if p.Environments[i].Name == envName {
			for j := range p.Environments[i].Services {
				if p.Environments[i].Services[j].Name == serviceName {
					dbType = p.Environments[i].Services[j].DBType()
					serviceIdx = j
					break
				}
			}
		}
	}

	// Determine default port based on DB type
	defaultPort := "5432"
	switch dbType {
	case "mongo":
		defaultPort = "27017"
	case "mysql":
		defaultPort = "3306"
	case "mssql":
		defaultPort = "1433"
	}

	m := &AddSyncEnvModel{
		cfg:         cfg,
		profile:     p,
		envName:     envName,
		serviceName: serviceName,
		serviceIdx:  serviceIdx,
		dbType:      dbType,
		editMode:    !addMode,
	}

	// Step 1: name input
	ni := textinput.New()
	ni.Placeholder = "staging, prod, dev..."
	ni.CharLimit = 40
	ni.Width = 30
	ni.Focus()
	m.nameInput = ni

	// Step 2: credential inputs
	hi := textinput.New()
	hi.Placeholder = "db.example.com"
	hi.CharLimit = 255
	hi.Width = 30

	pi := textinput.New()
	pi.Placeholder = defaultPort
	pi.CharLimit = 5
	pi.Width = 8
	pi.SetValue(defaultPort)

	ui := textinput.New()
	ui.Placeholder = "username"
	ui.CharLimit = 60
	ui.Width = 24

	pai := textinput.New()
	pai.Placeholder = "password (optional)"
	pai.CharLimit = 100
	pai.Width = 28
	pai.EchoMode = textinput.EchoPassword
	pai.EchoCharacter = '•'

	di := textinput.New()
	di.Placeholder = "database"
	di.CharLimit = 60
	di.Width = 24

	m.hostInput = hi
	m.portInput = pi
	m.userInput = ui
	m.passInput = pai
	m.dbInput = di

	// Max focus: 4 for non-mongo, 5 for mongo (includes AuthDB)
	m.maxFocus = 4
	if dbType == "mongo" {
		m.maxFocus = 5
		ai := textinput.New()
		ai.Placeholder = "admin (auth db)"
		ai.CharLimit = 60
		ai.Width = 24
		m.authDBInput = ai
	}

	// Pre-fill in edit mode
	if existing != nil && !addMode {
		m.origName = existing.Name
		m.nameInput.SetValue(existing.Name)
		m.hostInput.SetValue(existing.Host)
		m.portInput.SetValue(existing.Port)
		m.userInput.SetValue(existing.User)
		m.passInput.SetValue(existing.Password)
		m.dbInput.SetValue(existing.Database)
		if dbType == "mongo" {
			m.authDBInput.SetValue(existing.AuthDB)
		}
	}

	return m
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m *AddSyncEnvModel) Init() tea.Cmd {
	return textinput.Blink
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m *AddSyncEnvModel) Update(msg tea.Msg) (*AddSyncEnvModel, tea.Cmd) {
	switch m.step {
	case syncEnvStepName:
		return m.updateNameStep(msg)
	case syncEnvStepCredentials:
		return m.updateCredentialsStep(msg)
	case syncEnvStepReview:
		return m.updateReviewStep(msg)
	}
	return m, nil
}

// ── Step: Name ────────────────────────────────────────────────────────────────

func (m *AddSyncEnvModel) updateNameStep(msg tea.Msg) (*AddSyncEnvModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, wizardBack):
			// esc from name step → cancel the wizard, signal parent via nil
			return nil, nil

		case key.Matches(msg, wizardNext):
			name := strings.TrimSpace(m.nameInput.Value())
			m.nameErr = ""

			// Validate: not empty
			if name == "" {
				m.nameErr = "Name is required"
				return m, nil
			}

			// Validate: no path characters that could interfere with filesystem
			if strings.ContainsAny(name, `/\\:.*?"<>|`) {
				m.nameErr = "Name must use letters, numbers, hyphens, and underscores only"
				return m, nil
			}

			// Validate: no duplicates (skip in edit mode if name unchanged)
			if !m.editMode || name != m.origName {
				svc := m.findService()
				if svc != nil {
					for _, se := range svc.SyncEnvs {
						if se.Name == name {
							m.nameErr = fmt.Sprintf("A sync environment named '%s' already exists for this service", name)
							return m, nil
						}
					}
				}
			}

			// Advance to credentials step
			m.step = syncEnvStepCredentials
			m.credFocus = credFocusHost
			m.hostInput.Focus()
			return m, textinput.Blink
		}
	}

	var cmd tea.Cmd
	m.nameInput, cmd = m.nameInput.Update(msg)
	return m, cmd
}

// ── Step: Credentials ─────────────────────────────────────────────────────────

func (m *AddSyncEnvModel) updateCredentialsStep(msg tea.Msg) (*AddSyncEnvModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, wizardBack):
			// Back to name step
			m.step = syncEnvStepName
			m.nameInput.Focus()
			return m, textinput.Blink

		case key.Matches(msg, wizardTab):
			// Cycle focus through credential fields
			m.credFocus = (m.credFocus + 1) % (m.maxFocus + 1)
			m.updateCredFocus()
			return m, textinput.Blink

		case key.Matches(msg, wizardNext):
			// On last field → validate all and advance
			if m.credFocus >= m.maxFocus {
				if err := m.validateCredentials(); err != "" {
					m.credErr = err
					return m, nil
				}
				m.credErr = ""
				m.step = syncEnvStepReview
				return m, nil
			}

			// Validate current field and advance to next
			if err := m.validateCurrentField(); err != "" {
				m.credErr = err
				return m, nil
			}
			m.credErr = ""
			m.credFocus++
			m.updateCredFocus()
			return m, textinput.Blink
		}
	}

	// Update the active text input
	var cmd tea.Cmd
	switch m.credFocus {
	case credFocusHost:
		m.hostInput, cmd = m.hostInput.Update(msg)
	case credFocusPort:
		m.portInput, cmd = m.portInput.Update(msg)
	case credFocusUser:
		m.userInput, cmd = m.userInput.Update(msg)
	case credFocusPass:
		m.passInput, cmd = m.passInput.Update(msg)
	case credFocusDB:
		m.dbInput, cmd = m.dbInput.Update(msg)
	case credFocusAuthDB:
		if m.dbType == "mongo" {
			m.authDBInput, cmd = m.authDBInput.Update(msg)
		}
	}
	return m, cmd
}

// ── Step: Review ──────────────────────────────────────────────────────────────

func (m *AddSyncEnvModel) updateReviewStep(msg tea.Msg) (*AddSyncEnvModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, wizardBack):
			// Go back to credentials step
			m.step = syncEnvStepCredentials
			m.credFocus = credFocusHost
			m.hostInput.Focus()
			return m, textinput.Blink

		case key.Matches(msg, wizardNext):
			// Confirm and save
			return m, m.save()
		}
	}
	return m, nil
}

// ── Save ──────────────────────────────────────────────────────────────────────

func (m *AddSyncEnvModel) save() tea.Cmd {
	return func() tea.Msg {
		syncEnv := &config.SyncEnv{
			Name:     strings.TrimSpace(m.nameInput.Value()),
			DBType:   m.dbType,
			Host:     strings.TrimSpace(m.hostInput.Value()),
			Port:     strings.TrimSpace(m.portInput.Value()),
			User:     strings.TrimSpace(m.userInput.Value()),
			Password: m.passInput.Value(),
			Database: strings.TrimSpace(m.dbInput.Value()),
		}
		if m.dbType == "mongo" {
			syncEnv.AuthDB = strings.TrimSpace(m.authDBInput.Value())
		}

		svc := m.findService()
		if svc == nil {
			return ErrorMsg(fmt.Sprintf("service %q not found in %s/%s", m.serviceName, m.profile.Name, m.envName))
		}

		if m.editMode {
			// Replace existing sync-env by original name
			for i, se := range svc.SyncEnvs {
				if se.Name == m.origName {
					svc.SyncEnvs[i] = *syncEnv
					break
				}
			}
		} else {
			// Append new sync-env
			svc.SyncEnvs = append(svc.SyncEnvs, *syncEnv)
		}

		m.profile.UpdatedAt = time.Now()
		if err := m.cfg.UpdateProfile(*m.profile); err != nil {
			return ErrorMsg(err.Error())
		}

		return wizardSavedMsg{
			env:      syncEnv,
			editMode: m.editMode,
			origName: m.origName,
		}
	}
}

// ── Credential Focus helpers ──────────────────────────────────────────────────

// updateCredFocus blurs all credential inputs and focuses the one at credFocus.
func (m *AddSyncEnvModel) updateCredFocus() {
	m.hostInput.Blur()
	m.portInput.Blur()
	m.userInput.Blur()
	m.passInput.Blur()
	m.dbInput.Blur()
	if m.dbType == "mongo" {
		m.authDBInput.Blur()
	}

	switch m.credFocus {
	case credFocusHost:
		m.hostInput.Focus()
	case credFocusPort:
		m.portInput.Focus()
	case credFocusUser:
		m.userInput.Focus()
	case credFocusPass:
		m.passInput.Focus()
	case credFocusDB:
		m.dbInput.Focus()
	case credFocusAuthDB:
		if m.dbType == "mongo" {
			m.authDBInput.Focus()
		}
	}
}

// validateCurrentField validates the field at the current credFocus.
// Returns an error string, or "" if valid.
func (m *AddSyncEnvModel) validateCurrentField() string {
	switch m.credFocus {
	case credFocusHost:
		if strings.TrimSpace(m.hostInput.Value()) == "" {
			return "Host is required"
		}
	case credFocusPort:
		port := strings.TrimSpace(m.portInput.Value())
		if port != "" {
			p, err := strconv.Atoi(port)
			if err != nil || p < 1 || p > 65535 {
				return "Port must be a number between 1 and 65535"
			}
		}
	case credFocusUser:
		if strings.TrimSpace(m.userInput.Value()) == "" {
			return "Username is required"
		}
	case credFocusDB:
		if strings.TrimSpace(m.dbInput.Value()) == "" {
			return "Database name is required"
		}
	}
	return ""
}

// validateCredentials validates all credential fields in order.
// Returns the first error found, or "" if all valid.
func (m *AddSyncEnvModel) validateCredentials() string {
	for focus := 0; focus <= m.maxFocus; focus++ {
		m.credFocus = focus
		if err := m.validateCurrentField(); err != "" {
			return err
		}
	}
	return ""
}

// ── findService ───────────────────────────────────────────────────────────────

// findService returns a pointer to the config.Service in the profile's
// environment, so modifications are reflected in m.profile directly.
func (m *AddSyncEnvModel) findService() *config.Service {
	for i := range m.profile.Environments {
		if m.profile.Environments[i].Name == m.envName {
			for j := range m.profile.Environments[i].Services {
				if m.profile.Environments[i].Services[j].Name == m.serviceName {
					return &m.profile.Environments[i].Services[j]
				}
			}
		}
	}
	return nil
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m *AddSyncEnvModel) View() string {
	var b strings.Builder
	b.WriteString("\n")

	mode := "Add"
	if m.editMode {
		mode = "Edit"
	}
	b.WriteString(styles.StyleTitle.Render(
		fmt.Sprintf(" %s Sync Env — %s / %s ", mode, m.profile.Name, m.serviceName)))
	b.WriteString("\n\n")

	switch m.step {
	case syncEnvStepName:
		b.WriteString(m.viewNameStep())
	case syncEnvStepCredentials:
		b.WriteString(m.viewCredentialsStep())
	case syncEnvStepReview:
		b.WriteString(m.viewReviewStep())
	}

	if m.err != "" {
		b.WriteString("\n" + styles.StyleError.Render("  ✗ "+m.err))
	}

	b.WriteString("\n\n")
	b.WriteString(styles.StyleHelp.Render(
		styles.HelpItem("esc", "cancel"),
	))
	return b.String()
}

func (m *AddSyncEnvModel) viewNameStep() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("  DB type: %s\n\n", styles.StyleCode.Render(m.dbType)))

	if m.editMode {
		b.WriteString("  " + styles.StyleInputLabel.Render("Name") + "\n")
		b.WriteString("  " + styles.StyleCode.Render(m.nameInput.Value()) + "  ")
		b.WriteString(styles.StyleSubtitle.Render("(not editable)"))
	} else {
		b.WriteString("  " + styles.StyleInputLabel.Render("Display name") + "\n")
		b.WriteString("  " + m.nameInput.View() + "\n")
		b.WriteString("  " + styles.StyleSubtitle.Render("e.g., staging, prod, dev"))
	}

	if m.nameErr != "" {
		b.WriteString("\n\n  " + styles.StyleError.Render("✗ "+m.nameErr))
	}

	b.WriteString("\n\n")
	b.WriteString(styles.StyleHelp.Render(
		styles.HelpItem("esc", "cancel") + "   " +
			styles.HelpItem("↵", "next"),
	))

	return b.String()
}

func (m *AddSyncEnvModel) viewCredentialsStep() string {
	var b strings.Builder
	b.WriteString("  Connection details\n\n")

	// Host
	label := "  " + styles.StyleInputLabel.Render("Host")
	if m.credFocus == credFocusHost {
		b.WriteString(fmt.Sprintf("%s  %s\n", label, m.hostInput.View()))
	} else {
		val := m.hostInput.Value()
		if val == "" {
			val = "(empty)"
		}
		b.WriteString(fmt.Sprintf("%s  %s\n", label, styles.StyleCode.Render(val)))
	}

	// Port
	label = "  " + styles.StyleInputLabel.Render("Port")
	if m.credFocus == credFocusPort {
		b.WriteString(fmt.Sprintf("%s  %s\n", label, m.portInput.View()))
	} else {
		b.WriteString(fmt.Sprintf("%s  %s\n", label, styles.StyleCode.Render(m.portInput.Value())))
	}

	// User
	label = "  " + styles.StyleInputLabel.Render("User")
	if m.credFocus == credFocusUser {
		b.WriteString(fmt.Sprintf("%s  %s\n", label, m.userInput.View()))
	} else {
		val := m.userInput.Value()
		if val == "" {
			val = "(empty)"
		}
		b.WriteString(fmt.Sprintf("%s  %s\n", label, styles.StyleCode.Render(val)))
	}

	// Password
	label = "  " + styles.StyleInputLabel.Render("Password")
	if m.credFocus == credFocusPass {
		b.WriteString(fmt.Sprintf("%s  %s\n", label, m.passInput.View()))
	} else {
		masked := ""
		if m.passInput.Value() != "" {
			masked = "••••••"
		} else {
			masked = "(empty)"
		}
		b.WriteString(fmt.Sprintf("%s  %s\n", label, styles.StyleSubtitle.Render(masked)))
	}

	// Database
	label = "  " + styles.StyleInputLabel.Render("Database")
	if m.credFocus == credFocusDB {
		b.WriteString(fmt.Sprintf("%s  %s\n", label, m.dbInput.View()))
	} else {
		val := m.dbInput.Value()
		if val == "" {
			val = "(empty)"
		}
		b.WriteString(fmt.Sprintf("%s  %s\n", label, styles.StyleCode.Render(val)))
	}

	// AuthDB (mongo only)
	if m.dbType == "mongo" {
		label = "  " + styles.StyleInputLabel.Render("Auth DB")
		if m.credFocus == credFocusAuthDB {
			b.WriteString(fmt.Sprintf("%s  %s\n", label, m.authDBInput.View()))
		} else {
			val := m.authDBInput.Value()
			if val == "" {
				val = "(none)"
			}
			b.WriteString(fmt.Sprintf("%s  %s\n", label, styles.StyleCode.Render(val)))
		}
	}

	if m.credErr != "" {
		b.WriteString("\n  " + styles.StyleError.Render("✗ "+m.credErr))
	}

	b.WriteString("\n")
	b.WriteString(styles.StyleHelp.Render(
		styles.HelpItem("esc", "back") + "   " +
			styles.HelpItem("tab", "next field") + "   " +
			styles.HelpItem("↵", "next"),
	))

	return b.String()
}

func (m *AddSyncEnvModel) viewReviewStep() string {
	var b strings.Builder
	b.WriteString("  Review configuration\n\n")

	dbTypeLabel := styles.StyleCode.Render(m.dbType)
	hostPort := m.hostInput.Value() + ":" + m.portInput.Value()
	maskedPass := styles.StyleSubtitle.Render("•••••• (stored in plaintext YAML)")
	if m.passInput.Value() == "" {
		maskedPass = styles.StyleSubtitle.Render("(empty)")
	}

	b.WriteString(styles.StylePanel.Render(
		lipgloss.JoinVertical(lipgloss.Left,
			styles.StyleHeader.Render(m.nameInput.Value()),
			"",
			styles.StyleInputLabel.Render("Name")+"  "+styles.StyleCode.Render(m.nameInput.Value()),
			styles.StyleInputLabel.Render("DB Type")+"  "+dbTypeLabel,
			styles.StyleInputLabel.Render("Host")+"  "+styles.StyleCode.Render(hostPort),
			styles.StyleInputLabel.Render("User")+"  "+styles.StyleCode.Render(m.userInput.Value()),
			styles.StyleInputLabel.Render("Password")+"  "+maskedPass,
			styles.StyleInputLabel.Render("Database")+"  "+styles.StyleCode.Render(m.dbInput.Value()),
			m.authDBIfMongo(),
		),
	))

	b.WriteString("\n\n")
	b.WriteString("  " + styles.StyleButton.Render(" Save Sync Environment ") + "\n")

	b.WriteString("\n")
	b.WriteString(styles.StyleHelp.Render(
		styles.HelpItem("esc", "back") + "   " +
			styles.HelpItem("↵", "save"),
	))

	return b.String()
}

// authDBIfMongo returns the Auth DB review line if the DB type is mongo,
// or an empty string otherwise.
func (m *AddSyncEnvModel) authDBIfMongo() string {
	if m.dbType == "mongo" {
		return styles.StyleInputLabel.Render("Auth DB") + "  " + styles.StyleCode.Render(m.authDBInput.Value())
	}
	return ""
}
