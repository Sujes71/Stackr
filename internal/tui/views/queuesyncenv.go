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
	"github.com/stackr/stackr/internal/docker"
	"github.com/stackr/stackr/internal/tui/styles"
)

// ── Step enum ─────────────────────────────────────────────────────────────────

type queueSyncEnvStep int

const (
	queueStepName queueSyncEnvStep = iota
	queueStepConnection
	queueStepReview
)

// Connection focus order. TypeSpecific1 and TypeSpecific2 fields vary by queue
// type. Non-rabbitmq types skip TypeSpecific2 (VHost) during navigation.
const (
	connFocusHost          = iota // 0
	connFocusPort                 // 1
	connFocusUser                 // 2
	connFocusPass                 // 3
	connFocusTypeSpecific1        // 4 — BootstrapServers | ManagementEndpoint | BrokerURL
	connFocusTypeSpecific2        // 5 — VHost (rabbitmq only)
	connFocusToolImage            // 6
)

// ── Messages ──────────────────────────────────────────────────────────────────

// queueSyncEnvSavedMsg is sent by save() when the user confirms the review step.
type queueSyncEnvSavedMsg struct {
	env      *config.QueueSyncEnv
	editMode bool
	origName string
}

// ── Model ─────────────────────────────────────────────────────────────────────

type AddQueueSyncEnvModel struct {
	cfg         *config.Config
	profile     *config.Profile
	envName     string
	serviceName string
	serviceIdx  int
	queueType   string // derived from service QueueType()

	step queueSyncEnvStep

	// Mode
	editMode bool
	origName string // original name in edit mode (NOT mutable)

	// Step 1: name
	nameInput textinput.Model
	nameErr   string

	// Step 2: connection
	hostInput               textinput.Model
	portInput               textinput.Model
	userInput               textinput.Model
	passInput               textinput.Model
	bootstrapServersInput   textinput.Model // kafka
	managementEndpointInput textinput.Model // rabbitmq
	brokerURLInput          textinput.Model // activemq
	vhostInput              textinput.Model // rabbitmq
	toolImageInput          textinput.Model
	connFocus               int
	connErr                 string
	maxFocus                int // always connFocusToolImage (6)

	// Error state (shared)
	err string
}

// ── Constructor ───────────────────────────────────────────────────────────────

func NewAddQueueSyncEnv(
	cfg *config.Config, p *config.Profile,
	envName, serviceName string,
	addMode bool, existing *config.QueueSyncEnv,
) *AddQueueSyncEnvModel {
	// Resolve service and queue type from the profile
	var queueType string
	var serviceIdx int
	var serviceImage string
	for i := range p.Environments {
		if p.Environments[i].Name == envName {
			for j := range p.Environments[i].Services {
				if p.Environments[i].Services[j].Name == serviceName {
					queueType = p.Environments[i].Services[j].QueueType()
					serviceImage = p.Environments[i].Services[j].FullImage()
					serviceIdx = j
					break
				}
			}
		}
	}

	// Determine default port based on queue type
	defaultPort := config.DefaultPortKafka
	switch queueType {
	case "rabbitmq":
		defaultPort = config.DefaultPortRabbitMQ
	case "activemq":
		defaultPort = config.DefaultPortActiveMQ
	}

	m := &AddQueueSyncEnvModel{
		cfg:         cfg,
		profile:     p,
		envName:     envName,
		serviceName: serviceName,
		serviceIdx:  serviceIdx,
		queueType:   queueType,
		editMode:    !addMode,
		maxFocus:    connFocusToolImage,
	}

	// Step 1: name input
	ni := textinput.New()
	ni.Placeholder = "staging, prod, dev..."
	ni.CharLimit = 64
	ni.Width = 30
	ni.Focus()
	m.nameInput = ni

	// Step 2: connection inputs
	hi := textinput.New()
	hi.Placeholder = "queue.example.com"
	hi.CharLimit = 256
	hi.Width = 30

	pi := textinput.New()
	pi.Placeholder = defaultPort
	pi.CharLimit = 10
	pi.Width = 8
	pi.SetValue(defaultPort)

	ui := textinput.New()
	ui.Placeholder = "username (optional)"
	ui.CharLimit = 128
	ui.Width = 24

	pai := textinput.New()
	pai.Placeholder = "password (optional)"
	pai.CharLimit = 256
	pai.Width = 28
	pai.EchoMode = textinput.EchoPassword
	pai.EchoCharacter = '•'

	m.hostInput = hi
	m.portInput = pi
	m.userInput = ui
	m.passInput = pai

	// Type-specific fields per queue type
	switch queueType {
	case "kafka":
		bi := textinput.New()
		bi.Placeholder = "kafka1:9092,kafka2:9092"
		bi.CharLimit = 512
		bi.Width = 40
		m.bootstrapServersInput = bi

	case "rabbitmq":
		mi := textinput.New()
		mi.Placeholder = "http://host:15672"
		mi.CharLimit = 256
		mi.Width = 40
		m.managementEndpointInput = mi

		vi := textinput.New()
		vi.Placeholder = "/"
		vi.CharLimit = 128
		vi.Width = 24
		m.vhostInput = vi

	case "activemq":
		bi := textinput.New()
		bi.Placeholder = "tcp://host:61616"
		bi.CharLimit = 256
		bi.Width = 40
		m.brokerURLInput = bi
	}

	ti := textinput.New()
	ti.Placeholder = docker.DefaultQueueToolImage(queueType)
	ti.CharLimit = 128
	ti.Width = 30
	if serviceImage != "" {
		ti.SetValue(serviceImage)
	}
	m.toolImageInput = ti

	// Pre-fill in edit mode
	if existing != nil && !addMode {
		m.origName = existing.Name
		m.nameInput.SetValue(existing.Name)
		m.hostInput.SetValue(existing.Host)
		m.portInput.SetValue(existing.Port)
		m.userInput.SetValue(existing.User)
		if decrypted, err := existing.GetPassword(); err == nil {
			m.passInput.SetValue(decrypted)
		}
		switch queueType {
		case "kafka":
			m.bootstrapServersInput.SetValue(existing.BootstrapServers)
		case "rabbitmq":
			m.managementEndpointInput.SetValue(existing.ManagementEndpoint)
			m.vhostInput.SetValue(existing.VHost)
		case "activemq":
			m.brokerURLInput.SetValue(existing.BrokerURL)
		}
		if existing.ToolImage != "" {
			m.toolImageInput.SetValue(existing.ToolImage)
		}
	}

	return m
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m *AddQueueSyncEnvModel) Init() tea.Cmd {
	return textinput.Blink
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m *AddQueueSyncEnvModel) Update(msg tea.Msg) (*AddQueueSyncEnvModel, tea.Cmd) {
	switch m.step {
	case queueStepName:
		return m.updateNameStep(msg)
	case queueStepConnection:
		return m.updateConnectionStep(msg)
	case queueStepReview:
		return m.updateReviewStep(msg)
	}
	return m, nil
}

// ── Step: Name ────────────────────────────────────────────────────────────────

func (m *AddQueueSyncEnvModel) updateNameStep(msg tea.Msg) (*AddQueueSyncEnvModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, wizardBack):
			// esc from name step → cancel the wizard, signal parent via nil
			return nil, nil

		case key.Matches(msg, wizardNext):
			name := strings.TrimSpace(m.nameInput.Value())
			m.nameErr = ""

			// Validate: not empty — silently block
			if name == "" {
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
					for _, qse := range svc.QueueSyncEnvs {
						if qse.Name == name {
							m.nameErr = fmt.Sprintf("A queue environment named '%s' already exists for this service", name)
							return m, nil
						}
					}
				}
			}

			// Advance to connection step
			m.step = queueStepConnection
			m.connFocus = connFocusHost
			m.hostInput.Focus()
			return m, textinput.Blink
		}
	}

	var cmd tea.Cmd
	m.nameInput, cmd = m.nameInput.Update(msg)
	return m, cmd
}

// ── Step: Connection ──────────────────────────────────────────────────────────

func (m *AddQueueSyncEnvModel) updateConnectionStep(msg tea.Msg) (*AddQueueSyncEnvModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, wizardBack):
			// Back to name step
			m.step = queueStepName
			m.nameInput.Focus()
			return m, textinput.Blink

		case key.Matches(msg, wizardUp):
			m.connFocus = (m.connFocus - 1 + m.maxFocus + 1) % (m.maxFocus + 1)
			// Skip VHost position (5) for non-rabbitmq queue types
			if m.queueType != "rabbitmq" && m.connFocus == connFocusTypeSpecific2 {
				m.connFocus = (m.connFocus - 1 + m.maxFocus + 1) % (m.maxFocus + 1)
			}
			m.updateConnFocus()
			return m, textinput.Blink

		case key.Matches(msg, wizardDown):
			m.connFocus = (m.connFocus + 1) % (m.maxFocus + 1)
			// Skip VHost position (5) for non-rabbitmq queue types
			if m.queueType != "rabbitmq" && m.connFocus == connFocusTypeSpecific2 {
				m.connFocus = (m.connFocus + 1) % (m.maxFocus + 1)
			}
			m.updateConnFocus()
			return m, textinput.Blink

		case key.Matches(msg, wizardNext):
			// Validate all fields and advance
			if err, failIdx := m.validateConnection(); err != "" {
				if !strings.Contains(err, " is required") {
					m.connErr = err
				}
				m.connFocus = failIdx
				m.updateConnFocus()
				return m, textinput.Blink
			}
			m.connErr = ""
			m.step = queueStepReview
			return m, nil
		}
	}

	// Update the active text input
	var cmd tea.Cmd
	switch m.connFocus {
	case connFocusHost:
		m.hostInput, cmd = m.hostInput.Update(msg)
	case connFocusPort:
		m.portInput, cmd = m.portInput.Update(msg)
	case connFocusUser:
		m.userInput, cmd = m.userInput.Update(msg)
	case connFocusPass:
		m.passInput, cmd = m.passInput.Update(msg)
	case connFocusTypeSpecific1:
		switch m.queueType {
		case "kafka":
			m.bootstrapServersInput, cmd = m.bootstrapServersInput.Update(msg)
		case "rabbitmq":
			m.managementEndpointInput, cmd = m.managementEndpointInput.Update(msg)
		case "activemq":
			m.brokerURLInput, cmd = m.brokerURLInput.Update(msg)
		}
	case connFocusTypeSpecific2:
		if m.queueType == "rabbitmq" {
			m.vhostInput, cmd = m.vhostInput.Update(msg)
		}
	case connFocusToolImage:
		m.toolImageInput, cmd = m.toolImageInput.Update(msg)
	}
	return m, cmd
}

// ── Step: Review ──────────────────────────────────────────────────────────────

func (m *AddQueueSyncEnvModel) updateReviewStep(msg tea.Msg) (*AddQueueSyncEnvModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, wizardBack):
			// Go back to connection step
			m.step = queueStepConnection
			m.connFocus = connFocusHost
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

func (m *AddQueueSyncEnvModel) save() tea.Cmd {
	return func() tea.Msg {
		qse := &config.QueueSyncEnv{
			Name:      strings.TrimSpace(m.nameInput.Value()),
			QueueType: m.queueType,
			Host:      strings.TrimSpace(m.hostInput.Value()),
			Port:      strings.TrimSpace(m.portInput.Value()),
			User:      strings.TrimSpace(m.userInput.Value()),
		}
		if err := qse.SetPassword(m.passInput.Value()); err != nil {
			return ErrorMsg{Context: "queuesyncenv", Message: "encrypt password failed", Detail: err.Error()}
		}

		switch m.queueType {
		case "kafka":
			qse.BootstrapServers = strings.TrimSpace(m.bootstrapServersInput.Value())
		case "rabbitmq":
			qse.ManagementEndpoint = strings.TrimSpace(m.managementEndpointInput.Value())
			qse.VHost = strings.TrimSpace(m.vhostInput.Value())
		case "activemq":
			qse.BrokerURL = strings.TrimSpace(m.brokerURLInput.Value())
		}

		if toolImage := strings.TrimSpace(m.toolImageInput.Value()); toolImage != "" {
			// Only override if the user explicitly changed it from the service image default.
			// Compare against the default to avoid storing redundant values.
			if toolImage != docker.DefaultQueueToolImage(m.queueType) {
				qse.ToolImage = toolImage
			}
		}

		svc := m.findService()
		if svc == nil {
			return ErrorMsg{Context: "queuesyncenv", Message: "service not found", Detail: fmt.Sprintf("service %q not found in %s/%s", m.serviceName, m.profile.Name, m.envName)}
		}

		if m.editMode {
			// Replace existing queue-sync-env by original name
			for i, se := range svc.QueueSyncEnvs {
				if se.Name == m.origName {
					svc.QueueSyncEnvs[i] = *qse
					break
				}
			}
		} else {
			// Append new queue-sync-env
			svc.QueueSyncEnvs = append(svc.QueueSyncEnvs, *qse)
		}

		m.profile.UpdatedAt = time.Now()
		if err := m.cfg.UpdateProfile(*m.profile); err != nil {
			return ErrorMsg{Context: "queuesyncenv", Message: "save profile failed", Detail: err.Error()}
		}

		return queueSyncEnvSavedMsg{
			env:      qse,
			editMode: m.editMode,
			origName: m.origName,
		}
	}
}

// ── Connection Focus helpers ──────────────────────────────────────────────────

// updateConnFocus blurs all connection inputs and focuses the one at connFocus.
func (m *AddQueueSyncEnvModel) updateConnFocus() {
	m.hostInput.Blur()
	m.portInput.Blur()
	m.userInput.Blur()
	m.passInput.Blur()
	m.toolImageInput.Blur()

	// Blur type-specific inputs
	switch m.queueType {
	case "kafka":
		m.bootstrapServersInput.Blur()
	case "rabbitmq":
		m.managementEndpointInput.Blur()
		m.vhostInput.Blur()
	case "activemq":
		m.brokerURLInput.Blur()
	}

	switch m.connFocus {
	case connFocusHost:
		m.hostInput.Focus()
	case connFocusPort:
		m.portInput.Focus()
	case connFocusUser:
		m.userInput.Focus()
	case connFocusPass:
		m.passInput.Focus()
	case connFocusTypeSpecific1:
		switch m.queueType {
		case "kafka":
			m.bootstrapServersInput.Focus()
		case "rabbitmq":
			m.managementEndpointInput.Focus()
		case "activemq":
			m.brokerURLInput.Focus()
		}
	case connFocusTypeSpecific2:
		if m.queueType == "rabbitmq" {
			m.vhostInput.Focus()
		}
	case connFocusToolImage:
		m.toolImageInput.Focus()
	}
}

// validateCurrentField validates the field at the current connFocus.
// Returns an error string, or "" if valid.
func (m *AddQueueSyncEnvModel) validateCurrentField() string {
	switch m.connFocus {
	case connFocusHost:
		if strings.TrimSpace(m.hostInput.Value()) == "" {
			return "Host is required"
		}
	case connFocusPort:
		port := strings.TrimSpace(m.portInput.Value())
		if port != "" {
			p, err := strconv.Atoi(port)
			if err != nil || p < 1 || p > 65535 {
				return "Port must be a number between 1 and 65535"
			}
		}
	}
	return ""
}

// validateConnection validates all connection fields in order.
// Returns the first error found and the index of the failing field,
// or ("", 0) if all valid.
func (m *AddQueueSyncEnvModel) validateConnection() (string, int) {
	cur := m.connFocus
	for focus := 0; focus <= m.maxFocus; focus++ {
		m.connFocus = focus
		err := m.validateCurrentField()
		if err != "" {
			m.connFocus = cur
			return err, focus
		}
	}
	m.connFocus = cur
	return "", 0
}

// ── findService ───────────────────────────────────────────────────────────────

// findService returns a pointer to the config.Service in the profile's
// environment, so modifications are reflected in m.profile directly.
func (m *AddQueueSyncEnvModel) findService() *config.Service {
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

func (m *AddQueueSyncEnvModel) View() string {
	var b strings.Builder
	b.WriteString("\n")

	mode := "Add"
	if m.editMode {
		mode = "Edit"
	}
	b.WriteString(styles.StyleTitle.Render(
		fmt.Sprintf(" %s Queue Environment — %s / %s ", mode, m.profile.Name, m.serviceName)))
	b.WriteString("\n\n")

	switch m.step {
	case queueStepName:
		b.WriteString(m.viewNameStep())
	case queueStepConnection:
		b.WriteString(m.viewConnectionStep())
	case queueStepReview:
		b.WriteString(m.viewReviewStep())
	}

	if m.err != "" {
		b.WriteString("\n" + styles.StyleError.Render("  ✗ "+m.err))
	}

	return b.String()
}

func (m *AddQueueSyncEnvModel) viewNameStep() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("  Queue type: %s\n\n", styles.StyleCode.Render(m.queueType)))
	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	b.WriteString("  " + focusStyle.Render("Name: ") + m.nameInput.View() + "\n")
	b.WriteString("  " + styles.StyleSubtitle.Render("e.g., staging, prod, dev"))

	if m.nameErr != "" {
		b.WriteString("\n\n  " + styles.StyleError.Render("✗ "+m.nameErr))
	}

	b.WriteString("\n\n")
	b.WriteString(styles.BackHelp(styles.HelpItem("↵", "next")))
	return b.String()
}

func (m *AddQueueSyncEnvModel) viewConnectionStep() string {
	var b strings.Builder
	b.WriteString("  Connection details\n\n")

	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	normalStyle := lipgloss.NewStyle()

	hostLabel, portLabel, userLabel, passLabel, type1Label, type2Label, toolLabel :=
		normalStyle, normalStyle, normalStyle, normalStyle, normalStyle, normalStyle, normalStyle
	switch m.connFocus {
	case connFocusHost:
		hostLabel = focusStyle
	case connFocusPort:
		portLabel = focusStyle
	case connFocusUser:
		userLabel = focusStyle
	case connFocusPass:
		passLabel = focusStyle
	case connFocusTypeSpecific1:
		type1Label = focusStyle
	case connFocusTypeSpecific2:
		type2Label = focusStyle
	case connFocusToolImage:
		toolLabel = focusStyle
	}

	b.WriteString("  " + hostLabel.Render("Host:     ") + m.hostInput.View() + "\n")
	b.WriteString("  " + portLabel.Render("Port:     ") + m.portInput.View() + "\n")
	b.WriteString("  " + userLabel.Render("User:     ") + styles.StyleSubtitle.Render("(optional) ") + m.userInput.View() + "\n")
	b.WriteString("  " + passLabel.Render("Password: ") + styles.StyleSubtitle.Render("(optional) ") + m.passInput.View() + "\n")

	// Queue-type-specific fields
	switch m.queueType {
	case "kafka":
		b.WriteString("  " + type1Label.Render("Bootstrap:") + m.bootstrapServersInput.View() + "\n")
	case "rabbitmq":
		b.WriteString("  " + type1Label.Render("Mgmt URL: ") + m.managementEndpointInput.View() + "\n")
		b.WriteString("  " + type2Label.Render("VHost:    ") + m.vhostInput.View() + "\n")
	case "activemq":
		b.WriteString("  " + type1Label.Render("Broker:   ") + m.brokerURLInput.View() + "\n")
	}

	b.WriteString("  " + toolLabel.Render("Tool img: ") + styles.StyleSubtitle.Render("(optional) ") + m.toolImageInput.View() + "\n")

	if m.connErr != "" {
		b.WriteString("\n  " + styles.StyleError.Render("✗ "+m.connErr))
	}

	b.WriteString("\n")
	b.WriteString(styles.BackHelp(styles.HelpItem("↵", "next")))

	return b.String()
}

func (m *AddQueueSyncEnvModel) viewReviewStep() string {
	var b strings.Builder
	b.WriteString("  Review configuration\n\n")

	queueTypeLabel := styles.StyleCode.Render(m.queueType)
	hostPort := styles.StyleCode.Render(m.hostInput.Value() + ":" + m.portInput.Value())
	maskedPass := styles.StyleSubtitle.Render("•••••• (encrypted at rest — password is not stored in plaintext)")
	if m.passInput.Value() == "" {
		maskedPass = styles.StyleSubtitle.Render("(empty)")
	}

	lines := []string{
		styles.StyleHeader.Render(m.nameInput.Value()),
		"",
		styles.StyleInputLabel.Render("Queue Type") + "  " + queueTypeLabel,
		styles.StyleInputLabel.Render("Host") + "  " + hostPort,
	}

	if m.userInput.Value() != "" {
		lines = append(lines, styles.StyleInputLabel.Render("User")+"  "+styles.StyleCode.Render(m.userInput.Value()))
	}
	lines = append(lines, styles.StyleInputLabel.Render("Password")+"  "+maskedPass)

	// Type-specific fields
	switch m.queueType {
	case "kafka":
		if m.bootstrapServersInput.Value() != "" {
			lines = append(lines, styles.StyleInputLabel.Render("Bootstrap Servers")+"  "+styles.StyleCode.Render(m.bootstrapServersInput.Value()))
		}
	case "rabbitmq":
		if m.managementEndpointInput.Value() != "" {
			lines = append(lines, styles.StyleInputLabel.Render("Mgmt Endpoint")+"  "+styles.StyleCode.Render(m.managementEndpointInput.Value()))
		}
		if m.vhostInput.Value() != "" {
			lines = append(lines, styles.StyleInputLabel.Render("VHost")+"  "+styles.StyleCode.Render(m.vhostInput.Value()))
		}
	case "activemq":
		if m.brokerURLInput.Value() != "" {
			lines = append(lines, styles.StyleInputLabel.Render("Broker URL")+"  "+styles.StyleCode.Render(m.brokerURLInput.Value()))
		}
	}

	// Tool image
	toolDisplay := m.toolImageInput.Value()
	if toolDisplay == "" {
		toolDisplay = docker.DefaultQueueToolImage(m.queueType)
	}
	lines = append(lines, styles.StyleInputLabel.Render("Tool Image")+"  "+styles.StyleCode.Render(toolDisplay))

	panelContent := lipgloss.JoinVertical(lipgloss.Left, lines...)
	panelW := lipgloss.Width(panelContent) + 4
	if panelW < 60 {
		panelW = 60
	}
	b.WriteString(styles.StylePanel.Width(panelW).Render(panelContent))

	b.WriteString("\n\n")
	button := styles.StyleButton.Render(" Save Queue Environment ")
	b.WriteString(lipgloss.PlaceHorizontal(panelW, lipgloss.Center, button) + "\n")

	b.WriteString("\n")
	b.WriteString(styles.BackHelp(styles.HelpItem("↵", "save")))
	return b.String()
}
