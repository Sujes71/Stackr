package views

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/docker"
	"github.com/stackr/stackr/internal/tui/styles"
)

// ── Internal screen enum ──────────────────────────────────────────────────────

type queueOpsScreen int

const (
	queueScreenSyncEnvList queueOpsScreen = iota
	queueScreenActions
	queueScreenWizard
)

// ── Key bindings ──────────────────────────────────────────────────────────────

// Key bindings are defined in ops_common.go (opsKeyUp, opsKeyDown, etc.).

// ── Queue Ops specific messages ───────────────────────────────────────────────

// queueOpsDoneMsg is reused from messages.go (already defined there).

// ── Model ─────────────────────────────────────────────────────────────────────

// QueueOpsModel implements the 3-screen queue operations view.
// Internal screens:
//   - SyncEnv List (default) — shows configured QueueSyncEnvs + virtual "(local)" entry
//   - Actions — pre-defined and custom actions for the selected queue type
//   - Wizard — delegates to AddQueueSyncEnvModel for add/edit
type QueueOpsModel struct {
	cfg         *config.Config
	profile     *config.Profile
	envName     string
	serviceName string
	serviceIdx  int

	// Internal screen
	screen queueOpsScreen

	// SyncEnv list state
	listCursor       int
	deleteArmed      bool
	selectedEnv      string // name of selected QueueSyncEnv (empty = local)
	selectedEnvName  string // display name

	// Actions state
	actions       []actionItem
	actionCursor  int
	targetSyncEnv bool // true = remote via docker run --rm, false = local via docker exec

	// Running state — per-action inline spinners
	pendingOps map[string]string
	lastResult string
	doneOp     string
	doneOpAt   time.Time

	// Spinner
	spinnerFrame int

	// Viewport
	height int
	width  int

	// Result panel overlay
	viewingResult   bool
	resultOutput    string
	resultTitle     string
	resultViewport  viewport.Model
	viewportContent string // cached styled content for viewport

	// Wizard (delegated)
	wizardModel *AddQueueSyncEnvModel

	// Custom action form
	addingCustomAction bool
	editingCustomIdx   int
	customFormFocus    int
	customNameInput    textinput.Model
	customCmdInput     textinput.Model

	// Custom action param prompting
	promptingParams   bool
	promptingForCore  bool // true = prompting for pre-defined action, false = for custom
	paramsToPrompt    []string
	paramValues       map[string]string
	currentParamIdx   int
	paramInput        textinput.Model
	paramTextarea     textarea.Model

	// Custom action delete
	deleteActionArmed bool

	// Confirm before execute (double confirmation)
	confirmAction       bool
	confirmCustomAction bool
	pendingCustomCmd    string

	// Prompt state (start service before action)
	promptStartService bool
	promptStartDaemon  bool
	daemonStarting     bool
	pendingStartLabel  string
}

// ── Constructor ───────────────────────────────────────────────────────────────

func NewQueueOps(cfg *config.Config, p *config.Profile, envName, serviceName string) *QueueOpsModel {
	m := &QueueOpsModel{
		cfg:         cfg,
		profile:     p,
		envName:     envName,
		serviceName: serviceName,
		screen:      queueScreenSyncEnvList,
		pendingOps:  make(map[string]string),
		customNameInput: textinput.New(),
		customCmdInput:  textinput.New(),
		paramInput:      textinput.New(),
		resultViewport:  viewport.New(0, 0),
	}
	vp := &m.resultViewport
	vp.KeyMap.Up.SetKeys("up")
	vp.KeyMap.Down.SetKeys("down")
	vp.KeyMap.PageUp.SetKeys()
	vp.KeyMap.PageDown.SetKeys()
	vp.KeyMap.HalfPageUp.SetKeys()
	vp.KeyMap.HalfPageDown.SetKeys()
	m.customNameInput.Placeholder = "Action name"
	m.customCmdInput.Placeholder = "Command (use {param} for variables)"
	m.paramInput.Placeholder = "value"
	m.paramTextarea = textarea.New()
	m.paramTextarea.SetHeight(6)
	m.paramTextarea.ShowLineNumbers = false
	m.paramTextarea.Placeholder = "message content"
	m.paramTextarea.CharLimit = 0
	// Resolve service index for mutations
	for i, svc := range m.currentEnv().Services {
		if svc.Name == serviceName {
			m.serviceIdx = i
			break
		}
	}
	return m
}

// ── Getters for breadcrumb navigation ─────────────────────────────────────────

func (m *QueueOpsModel) ProfileName() string { return m.profile.Name }
func (m *QueueOpsModel) EnvName() string     { return m.envName }
func (m *QueueOpsModel) ServiceName() string { return m.serviceName }

func (m *QueueOpsModel) ServiceKey() string {
	return m.profile.Name + "/" + m.envName + "/" + m.serviceName
}

func (m *QueueOpsModel) ReShow(cfg *config.Config) {
	if p, ok := cfg.GetProfile(m.profile.Name); ok {
		m.profile = p
	}
	m.deleteArmed = false
	m.deleteActionArmed = false
}

func (m *QueueOpsModel) IsBusy() bool {
	return len(m.pendingOps) > 0
}

// ── Environment / Service helpers ─────────────────────────────────────────────

func (m *QueueOpsModel) currentEnv() *config.Environment {
	return opsCurrentEnv(m.profile, m.envName)
}

func (m *QueueOpsModel) currentService() *config.Service {
	return opsCurrentService(m.currentEnv(), m.serviceName, &m.serviceIdx)
}

func (m *QueueOpsModel) currentQueueType() string {
	svc := m.currentService()
	if svc == nil {
		return ""
	}
	return svc.QueueType()
}

// ── Init / Size ───────────────────────────────────────────────────────────────

func (m *QueueOpsModel) Init() tea.Cmd {
	return nil
}

func (m *QueueOpsModel) SetSize(w, h int) {
	m.width = w
	m.height = max(0, h)
	m.resultViewport.Width = w - 4
	if m.viewingResult {
		lineCount := strings.Count(m.viewportContent, "\n") + 1
		// +3 for title + blank lines outside the viewport
		m.resultViewport.Height = min(lineCount, max(8, h-13))
	} else {
		m.resultViewport.Height = max(8, h-10)
	}
	// Recalculate textarea height if param prompting with a textarea param
	if m.promptingParams && m.currentParamIdx < len(m.paramsToPrompt) {
		if m.isTextareaParam(m.paramsToPrompt[m.currentParamIdx]) {
			m.setMessageTextareaHeight()
		}
	}
}

// ── Update dispatch ───────────────────────────────────────────────────────────

func (m *QueueOpsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if wsMsg, ok := msg.(tea.WindowSizeMsg); ok {
		m.SetSize(wsMsg.Width, wsMsg.Height-5)
		return m, nil
	}

	if _, ok := msg.(SpinnerTickMsg); ok {
		if len(m.pendingOps) > 0 {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return SpinnerTickMsg{}
			})
		}
		// Keep spinner running while doneOp is active to trigger re-renders
		// (the "✓" is shown based on timestamp, not a one-shot timer).
		if m.doneOp != "" {
			if time.Since(m.doneOpAt) >= 5*time.Second {
				m.doneOp = ""
			} else {
				m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
				return m, tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
					return SpinnerTickMsg{}
				})
			}
		}
		return m, nil
	}

	switch m.screen {
	case queueScreenWizard:
		return m.updateWizard(msg)
	case queueScreenActions:
		return m.updateActions(msg)
	default:
		return m.updateList(msg)
	}
}

// ── SyncEnv List screen ───────────────────────────────────────────────────────

func (m *QueueOpsModel) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, opsKeyBack):
			m.deleteArmed = false
			return m, func() tea.Msg {
				return NavigateTo{Screen: NavProfileView, ProfileName: m.profile.Name, EnvName: m.envName, ServiceName: m.serviceName}
			}
		}

		svc := m.currentService()
		if svc == nil {
			return m, nil
		}
		queueSyncEnvs := svc.QueueSyncEnvs
		// Cursor range: 0 (local) to len(queueSyncEnvs) (last real entry)
		maxCursor := len(queueSyncEnvs)

		switch {
		case key.Matches(msg, opsKeyUp) && m.listCursor > 0 && !m.deleteArmed:
			m.listCursor--

		case key.Matches(msg, opsKeyDown) && m.listCursor < maxCursor && !m.deleteArmed:
			m.listCursor++

		case key.Matches(msg, opsKeyEnter):
			if m.listCursor == 0 {
				// Local entry
				m.targetSyncEnv = false
				m.selectedEnv = ""
				m.selectedEnvName = "(local)"
				m.actions = m.buildActionsList(nil)
			} else {
				// Real QueueSyncEnv
				qse := &queueSyncEnvs[m.listCursor-1]
				m.targetSyncEnv = true
				m.selectedEnv = qse.Name
				m.selectedEnvName = qse.Name
				m.actions = m.buildActionsList(qse)
			}
			m.actionCursor = m.firstEnabledAction()
			m.screen = queueScreenActions
			m.deleteArmed = false

		case key.Matches(msg, opsKeyAdd):
			m.deleteArmed = false
			m.wizardModel = NewAddQueueSyncEnv(m.cfg, m.profile, m.envName, m.serviceName, true, nil)
			m.screen = queueScreenWizard

		case key.Matches(msg, opsKeyEdit):
			if m.listCursor > 0 && m.listCursor <= len(queueSyncEnvs) {
				qse := &queueSyncEnvs[m.listCursor-1]
				m.wizardModel = NewAddQueueSyncEnv(m.cfg, m.profile, m.envName, m.serviceName, false, qse)
				m.screen = queueScreenWizard
				m.deleteArmed = false
			}

		case key.Matches(msg, opsKeyDelete):
			if m.listCursor == 0 {
				break // can't delete local entry
			}
			if !m.deleteArmed {
				m.deleteArmed = true
			} else {
				idx := m.listCursor - 1
				m.deleteArmed = false
				svc.QueueSyncEnvs = append(queueSyncEnvs[:idx], queueSyncEnvs[idx+1:]...)
				m.profile.UpdatedAt = time.Now()
				if err := m.cfg.UpdateProfile(*m.profile); err != nil {
					m.lastResult = formatErrorMsg(err.Error())
					return m, nil
				}
				if m.listCursor > len(svc.QueueSyncEnvs) && m.listCursor > 0 {
					m.listCursor--
				}
				return m, nil
			}

		default:
			if m.deleteArmed {
				m.deleteArmed = false
			}
		}
	}

	return m, nil
}

func (m *QueueOpsModel) viewSyncEnvList() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.StyleTitle.Render(
		fmt.Sprintf(" Queue Ops — %s / %s / %s ", m.profile.Name, m.envName, m.serviceName)))
	b.WriteString("\n\n")
	b.WriteString(styles.StyleSubtitle.Render("  Queue Environments"))
	b.WriteString("\n\n")

	svc := m.currentService()
	if svc == nil {
		b.WriteString(styles.StyleError.Render("  Service not found."))
		b.WriteString("\n")
		b.WriteString(styles.StyleSubtitle.Render("  This Queue Ops entry is stale or the service was removed."))
		b.WriteString("\n")
		b.WriteString(styles.StyleSubtitle.Render("  Press esc to go back to the profile and choose another service."))
		b.WriteString("\n\n")
		b.WriteString(styles.BackHelp())
		return b.String()
	}
	queueSyncEnvs := svc.QueueSyncEnvs

	selStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)

	// Build all items as strings for windowing
	totalItems := 1 + len(queueSyncEnvs) // local + remote envs
	overhead := 14                        // title, subtitle, help, margins
	if m.deleteArmed {
		overhead++
	}
	if m.lastResult != "" {
		overhead += 2
	}
	win := m.queueOpsWindow(totalItems, m.listCursor, 1, overhead)

	if win.hasAbove {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", win.start)))
		b.WriteString("\n")
	}

	for idx := win.start; idx < win.end; idx++ {
		if idx == 0 {
			// Virtual "(local)" entry
			localLine := fmt.Sprintf("    %s  %s  %s", "(local)", m.currentQueueType(), "(docker exec)")
			if m.listCursor == 0 {
				b.WriteString(selStyle.Render("  > " + localLine))
			} else {
				b.WriteString("    " + localLine)
			}
			b.WriteString("\n")
		} else {
			qse := queueSyncEnvs[idx-1]
			prefix := "    "
			if idx == m.listCursor {
				prefix = "  > "
			}
			line := fmt.Sprintf("%s%s  %s  %s:%s",
				prefix, qse.Name, qse.QueueType, qse.Host, qse.Port)
			if idx == m.listCursor {
				b.WriteString(selStyle.Render(line))
			} else {
				b.WriteString(line)
			}
			b.WriteString("\n")
		}
	}

	if win.hasBelow {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↓ %d more", totalItems-win.end)))
		b.WriteString("\n")
	}

	if len(queueSyncEnvs) == 0 {
		b.WriteString("\n")
		b.WriteString(styles.StyleSubtitle.Render("  No remote queue environments configured."))
		b.WriteString("\n")
		b.WriteString(styles.StyleSubtitle.Render("  Press a to add one."))
		b.WriteString("\n")
	}

	if m.deleteArmed {
		b.WriteString("\n" + styles.StyleError.Render("  Press d again to confirm removal"))
		b.WriteString("\n")
	}

	if m.lastResult != "" {
		b.WriteString("\n  " + m.lastResult + "\n")
	}

	b.WriteString("\n")
	// Contextual actions
	var contextItems []styles.HelpItemResult
	contextItems = append(contextItems, styles.HelpItem("a", "add"))
	if len(queueSyncEnvs) > 0 && m.listCursor > 0 {
		contextItems = append(contextItems, styles.HelpItem("e", "edit"))
		contextItems = append(contextItems, styles.HelpItem("d d", "delete"))
	}
	b.WriteString("      " + styles.InlineHelp(contextItems...))
	b.WriteString("\n\n")
	// Navigation
	b.WriteString(styles.BackHelp(
		styles.HelpItem("↑ ↓", "navigate"),
		styles.HelpItem("↵", "open"),
	))
	return b.String()
}

// ── Build Actions List ────────────────────────────────────────────────────────

// actionParams returns the param names required for a pre-defined action, or nil.
func (m *QueueOpsModel) actionParams(act actionItem) []string {
	switch act.label {
	case "Describe topic":
		return []string{"topic"}
	case "Create topic":
		return []string{"topic", "partitions", "replication_factor"}
	case "Delete topic":
		return []string{"topic"}
	case "Send message":
		if m.currentQueueType() == "activemq" {
			return []string{"queue", "message"}
		}
		return []string{"topic", "message"}
	case "Consume messages":
		if m.currentQueueType() == "activemq" {
			return []string{"queue"}
		}
		return []string{"topic"}
	case "Describe group":
		return []string{"group"}
	case "Reset offset":
		return []string{"group", "topic"}
	case "Create queue":
		return []string{"name"}
	case "Delete queue":
		return []string{"name"}
	case "Purge queue":
		if m.currentQueueType() == "activemq" {
			return []string{"queue"}
		}
		return []string{"name"}
	case "Browse queue":
		return []string{"queue"}
	}
	return nil
}

func (m *QueueOpsModel) buildActionsList(qse *config.QueueSyncEnv) []actionItem {
	queueType := m.currentQueueType()
	container := docker.ContainerName(m.profile.Name, m.envName, m.serviceName)

	// helper: return the right exec function
	qExec := func(program string, args ...string) tea.Cmd {
		return func() tea.Msg {
			var out string
			var err error
			if qse != nil {
				out, err = docker.QueueExecRemote(qse, program, args...)
			} else {
				out, err = docker.QueueExecLocal(container, program, args...)
			}
			if err != nil {
				return queueOpsDoneMsg{err: err}
			}
			return queueOpsDoneMsg{result: out}
		}
	}

	var actions []actionItem

	switch queueType {
	case "kafka":
		// Bootstrap servers: local uses localhost, remote uses qse config
		bs := "localhost:9092"
		if qse != nil {
			bs = qse.BootstrapServers
			if bs == "" {
				bs = qse.Host + ":" + qse.Port
			}
		}

		actions = []actionItem{
			// Topics group
			{label: "List topics", group: "Topics", enabled: true,
				execute: 		qExec("/usr/bin/kafka-topics", "--bootstrap-server", bs, "--list")},
			{label: "Describe topic", group: "Topics", enabled: true,
				execute: nil}, // params: topic → will be prompted
			{label: "Create topic", group: "Topics", enabled: true,
				execute: nil}, // params: name, partitions, replication_factor
			{label: "Delete topic", group: "Topics", enabled: true,
				execute: nil}, // params: topic → destructive

			// Messaging group
			{label: "Send message", group: "Messaging", enabled: true,
				execute: nil}, // params: topic, message
			{label: "Consume messages", group: "Messaging", enabled: true,
				execute: nil}, // params: topic

			// Consumer Groups group
			{label: "List groups", group: "Consumer Groups", enabled: true,
				execute: 		qExec("/usr/bin/kafka-consumer-groups", "--bootstrap-server", bs, "--list")},
			{label: "Describe group", group: "Consumer Groups", enabled: true,
				execute: nil}, // params: group
			{label: "Reset offset", group: "Consumer Groups", enabled: true,
				execute: nil}, // params: group, topic → destructive
		}

	case "rabbitmq":
		// For rabbitmqadmin: local uses -H localhost, remote uses qse host:port
		hostFlag := "localhost"
		if qse != nil {
			hostFlag = qse.Host
		}
		portFlag := "15672"
		if qse != nil && qse.Port != "" {
			portFlag = qse.Port
		}

		rabbitArgs := func(subcmd string) []string {
			args := []string{"-H", hostFlag, "-P", portFlag}
			if qse != nil && qse.User != "" {
				args = append(args, "-u", qse.User, "-p", qse.Password)
			}
			args = append(args, subcmd)
			return args
		}

		actions = []actionItem{
			// Queues group
			{label: "List queues", group: "Queues", enabled: true,
				execute: qExec("/usr/local/bin/rabbitmqadmin", rabbitArgs("list queues")...)},
			{label: "Create queue", group: "Queues", enabled: true,
				execute: nil}, // params: name
			{label: "Delete queue", group: "Queues", enabled: true,
				execute: nil}, // params: name → destructive
			{label: "Purge queue", group: "Queues", enabled: true,
				execute: nil}, // params: name → destructive

			// Exchanges & Bindings group
			{label: "List exchanges", group: "Exchanges & Bindings", enabled: true,
				execute: qExec("/usr/local/bin/rabbitmqadmin", rabbitArgs("list exchanges")...)},
			{label: "List bindings", group: "Exchanges & Bindings", enabled: true,
				execute: qExec("/usr/local/bin/rabbitmqadmin", rabbitArgs("list bindings")...)},

			// Connections group
			{label: "List connections", group: "Connections", enabled: true,
				execute: qExec("/usr/local/bin/rabbitmqadmin", rabbitArgs("list connections")...)},
			{label: "List channels", group: "Connections", enabled: true,
				execute: qExec("/usr/local/bin/rabbitmqadmin", rabbitArgs("list channels")...)},
			{label: "List consumers", group: "Connections", enabled: true,
				execute: qExec("/usr/local/bin/rabbitmqadmin", rabbitArgs("list consumers")...)},
		}

			case "activemq":
		// JMX exec helper: uses --jmxurl for both remote and local.
		// --jmxlocal (Attach API) was initially used for local but doesn't
		// reliably find destination MBeans inside the container — --jmxurl
		// via RMI/JMX remote works consistently.
		// The flags MUST come AFTER the subcommand (CLI syntax: task [task-options]).
		jmxExec := func(args ...string) tea.Cmd {
			jmxURL := "service:jmx:rmi:///jndi/rmi://localhost:1099/jmxrmi"
			if qse != nil {
				jmxURL = "service:jmx:rmi:///jndi/rmi://" + qse.Host + ":1099/jmxrmi"
			}
			return qExec("activemq", append(args, "--jmxurl", jmxURL)...)
		}

		actions = []actionItem{
			// Queues group
			{label: "List destinations", group: "Queues", enabled: true,
				execute: jmxExec("dstat")},
			{label: "Browse queue", group: "Queues", enabled: true,
				execute: nil},
			{label: "Purge queue", group: "Queues", enabled: true,
				execute: nil},

			// Messaging group
			{label: "Send message", group: "Messaging", enabled: true,
				execute: nil},
			{label: "Consume messages", group: "Messaging", enabled: true,
				execute: nil},
		}
	}

	// Append custom actions (only when remote — qse != nil)
	if qse != nil && len(qse.CustomActions) > 0 {
		for idx := range qse.CustomActions {
			i := idx
			actions = append(actions, actionItem{
				label:     qse.CustomActions[i].Name,
				group:     actions[len(actions)-1].group, // same group as last pre-defined
				enabled:   true,
				isCustom:  true,
				customIdx: i,
				execute:   nil,
			})
		}
	}

	// "+ Add action" entry (only when remote)
	if qse != nil {
		lastGroup := ""
		if len(actions) > 0 {
			lastGroup = actions[len(actions)-1].group
		}
		actions = append(actions, actionItem{
			label:       "+ Add action",
			group:       lastGroup,
			enabled:     true,
			isAddAction: true,
			execute:     nil,
		})
	}

	return actions
}

// ── Actions screen ────────────────────────────────────────────────────────────

func (m *QueueOpsModel) updateActions(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle Docker daemon prompt (takes priority over service prompt)
	if m.promptStartDaemon {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "y", "Y":
				m.promptStartDaemon = false
				m.daemonStarting = true
				m.lastResult = ""
				if m.pendingStartLabel != "" {
					m.pendingOps[m.pendingStartLabel] = "starting"
				}
				return m, tea.Batch(
					m.launchDockerCmd(),
					spinnerCmd(),
				)
			case "n", "N", "esc":
				m.promptStartDaemon = false
				m.pendingOps = make(map[string]string)
				m.pendingStartLabel = ""
				return m, nil
			}
		}
		return m, nil
	}

	// Block keys while Docker daemon is starting
	if m.daemonStarting {
		if _, ok := msg.(tea.KeyMsg); ok {
			return m, nil
		}
	}

	// Handle start-service prompt inline
	if m.promptStartService {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "y", "Y":
				m.promptStartService = false
				svc := m.currentService()
				if svc == nil {
					m.pendingOps = make(map[string]string)
					m.lastResult = styles.StyleError.Render("Service not found.")
					return m, nil
				}
				if m.pendingStartLabel != "" {
					m.pendingOps[m.pendingStartLabel] = "starting"
				}
				return m, tea.Batch(
					func() tea.Msg {
						err := docker.UpService(m.profile.Name, m.envName, svc.ComposeName())
						return serviceStartedMsg{err: err}
					},
					spinnerCmd(),
				)
			case "n", "N", "esc":
				m.promptStartService = false
				m.pendingOps = make(map[string]string)
				m.pendingStartLabel = ""
				return m, nil
			}
		}
		return m, nil
	}

	// Param prompting (for pre-defined and custom actions)
	if m.promptingParams {
		return m.updateParamPrompt(msg)
	}

	// Confirm custom action before execute
	if m.confirmCustomAction {
		return m.updateCustomConfirm(msg)
	}

	// Confirm core action before execute (double confirmation)
	if m.confirmAction {
		return m.updateCoreConfirm(msg)
	}

	// Custom action form (add/edit)
	if m.addingCustomAction {
		return m.updateCustomForm(msg)
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.viewingResult {
			switch {
			case key.Matches(msg, opsKeyUp):
				m.resultViewport.LineUp(1)
			case key.Matches(msg, opsKeyDown):
				m.resultViewport.LineDown(1)
			default:
				m.viewingResult = false
				return m, nil
			}
			return m, nil
		}
		switch {
		case key.Matches(msg, opsKeyBack):
			if m.promptStartDaemon || len(m.pendingOps) > 0 {
				if m.promptStartDaemon {
					m.promptStartDaemon = false
					m.promptStartService = false
				}
				break
			}
			m.deleteArmed = false
			m.deleteActionArmed = false
			m.screen = queueScreenSyncEnvList
			m.selectedEnv = ""
			m.actions = nil

		case key.Matches(msg, opsKeyUp) && len(m.pendingOps) == 0 && !m.promptStartService:
			for i := m.actionCursor - 1; i >= 0; i-- {
				if m.actions[i].enabled {
					m.actionCursor = i
					break
				}
			}

		case key.Matches(msg, opsKeyDown) && len(m.pendingOps) == 0 && !m.promptStartService:
			for i := m.actionCursor + 1; i < len(m.actions); i++ {
				if m.actions[i].enabled {
					m.actionCursor = i
					break
				}
			}

		case key.Matches(msg, opsKeyEnter) && len(m.pendingOps) == 0 && !m.promptStartService:
			m.deleteActionArmed = false
			if m.actionCursor < len(m.actions) {
				act := m.actions[m.actionCursor]
				if act.enabled {
					if act.isAddAction {
						m.addingCustomAction = true
						m.editingCustomIdx = -1
						m.customFormFocus = 0
						m.customNameInput.SetValue("")
						m.customCmdInput.SetValue("")
						return m, nil
					}
					// Check for pre-defined actions that need param prompting
					if params := m.actionParams(act); len(params) > 0 {
						m.promptingParams = true
						m.promptingForCore = true
						m.paramsToPrompt = params
						m.paramValues = make(map[string]string)
						m.currentParamIdx = 0
						m.refreshParamInput()
						return m, nil
					}
					if act.isCustom {
						svc := m.currentService()
						if svc == nil {
							return m, nil
						}
						qse := svc.FindQueueSyncEnv(m.selectedEnv)
						if qse == nil || act.customIdx >= len(qse.CustomActions) {
							return m, nil
						}
						cmd := qse.CustomActions[act.customIdx].Command
						m.pendingCustomCmd = cmd
						params := extractParams(cmd)
						if len(params) > 0 {
							m.promptingParams = true
							m.promptingForCore = false
							m.paramsToPrompt = params
							m.paramValues = make(map[string]string)
							m.currentParamIdx = 0
							m.refreshParamInput()
						} else {
							m.confirmCustomAction = true
						}
						return m, nil
					}
					// Core action (no params or all params pre-filled): double confirmation
					if act.execute != nil {
						m.confirmAction = true
					}
				}
				if act.disabledReason != "" {
					m.lastResult = styles.StyleSubtitle.Render("Unavailable: " + act.disabledReason)
					return m, nil
				}
			}

		case key.Matches(msg, opsKeyEdit) && len(m.pendingOps) == 0 && !m.promptStartService:
			m.deleteActionArmed = false
			if m.actionCursor < len(m.actions) {
				act := m.actions[m.actionCursor]
				if act.isCustom {
					svc := m.currentService()
					if svc == nil {
						return m, nil
					}
					qse := svc.FindQueueSyncEnv(m.selectedEnv)
					if qse == nil || act.customIdx >= len(qse.CustomActions) {
						return m, nil
					}
					m.addingCustomAction = true
					m.editingCustomIdx = act.customIdx
					m.customFormFocus = 0
					m.customNameInput.SetValue(qse.CustomActions[act.customIdx].Name)
					m.customCmdInput.SetValue(qse.CustomActions[act.customIdx].Command)
				}
			}

		case key.Matches(msg, opsKeyDelete) && len(m.pendingOps) == 0 && !m.promptStartService:
			if m.actionCursor < len(m.actions) {
				act := m.actions[m.actionCursor]
				if act.isCustom {
					if !m.deleteActionArmed {
						m.deleteActionArmed = true
					} else {
						m.deleteActionArmed = false
						svc := m.currentService()
						if svc == nil {
							return m, nil
						}
						qse := svc.FindQueueSyncEnv(m.selectedEnv)
						if qse == nil || act.customIdx >= len(qse.CustomActions) {
							return m, nil
						}
						qse.CustomActions = append(qse.CustomActions[:act.customIdx], qse.CustomActions[act.customIdx+1:]...)
						m.profile.UpdatedAt = time.Now()
						if err := m.cfg.UpdateProfile(*m.profile); err != nil {
							m.lastResult = formatErrorMsg(err.Error())
							return m, nil
						}
						m.actions = m.buildActionsList(qse)
						if m.actionCursor >= len(m.actions) {
							m.actionCursor = len(m.actions) - 1
						}
						return m, nil
					}
				} else {
					m.deleteActionArmed = false
				}
			}

		default:
			if m.deleteActionArmed {
				m.deleteActionArmed = false
			}
		}

	case queueOpsDoneMsg:
		var completedLabel string
		for label := range m.pendingOps {
			completedLabel = label
		}
		m.pendingOps = make(map[string]string)

		if msg.err != nil {
			errMsg := msg.err.Error()
			if m.currentQueueType() == "activemq" {
				errMsg = m.cleanActiveMQError(errMsg)
			}
			m.lastResult = formatErrorMsg(errMsg)
			return m, nil
		}

		// Rebuild actions (custom actions may have changed)
		svc := m.currentService()
		if svc != nil {
			qse := svc.FindQueueSyncEnv(m.selectedEnv)
			m.actions = m.buildActionsList(qse)
		}
		if m.actionCursor >= len(m.actions) || !m.actions[m.actionCursor].enabled {
			m.actionCursor = m.firstEnabledAction()
		}

		m.doneOp = completedLabel
		m.doneOpAt = time.Now()
		// Clean and format output per queue type.
		cleaned := msg.result
		switch m.currentQueueType() {
		case "activemq":
			cleaned = cleanActiveMQOutput(msg.result)
			cleaned = formatActiveMQTable(cleaned)
			cleaned = formatActiveMQBrowse(cleaned)
		case "kafka":
			cleaned = formatActiveMQTable(cleaned)  // consumer groups describe
			cleaned = formatKafkaDescribe(cleaned)  // topic describe
		case "rabbitmq":
			cleaned = formatRabbitMQTable(cleaned)  // rabbitmqadmin list *
		}
		if cleaned == "" {
			// Auto-generated success — no output, skip the result panel entirely.
			// The "✓" in the action list is enough confirmation.
			m.viewingResult = false
			m.resultOutput = ""
			return m, spinnerCmd()
		}
		m.viewingResult = true
		m.resultOutput = cleaned
		m.resultTitle = completedLabel
		// Only the code goes inside the viewport — title stays visible above.
		m.viewportContent = styles.StyleCode.Render(m.resultOutput)
		m.resultViewport.SetContent(m.viewportContent)
		m.resultViewport.Width = m.width - 4
		lineCount := strings.Count(m.viewportContent, "\n") + 1
		// +3 for title + blank line outside the viewport
		m.resultViewport.Height = min(lineCount, max(8, m.height-13))
		m.resultViewport.YPosition = 0
		return m, spinnerCmd()

	case daemonStartedMsg:
		m.daemonStarting = false
		if !msg.available {
			m.pendingOps = make(map[string]string)
			m.pendingStartLabel = ""
			m.lastResult = formatErrorMsg("Docker daemon failed to start")
			return m, nil
		}
		svc := m.currentService()
		if svc == nil {
			m.pendingOps = make(map[string]string)
			m.lastResult = styles.StyleError.Render("Service not found.")
			return m, nil
		}
		if m.pendingStartLabel != "" {
			m.pendingOps[m.pendingStartLabel] = "starting"
		}
		return m, tea.Batch(
			func() tea.Msg {
				err := docker.UpService(m.profile.Name, m.envName, svc.ComposeName())
				if err != nil {
					return serviceStartedMsg{err: err}
				}
				env := m.currentEnv()
				if env != nil {
					for _, s := range env.Services {
						if s.ComposeName() != svc.ComposeName() {
							_ = docker.RmService(m.profile.Name, m.envName, s.ComposeName())
						}
					}
				}
				return serviceStartedMsg{}
			},
			spinnerCmd(),
		)

	case serviceStartedMsg:
		if msg.err != nil {
			m.pendingOps = make(map[string]string)
			m.lastResult = formatErrorMsg("start service: " + msg.err.Error())
			return m, nil
		}
		// Poll container readiness before executing
		svc := m.currentService()
		if svc != nil {
			qse := svc.FindQueueSyncEnv(m.selectedEnv)
			m.actions = m.buildActionsList(qse)
			for pendingLabel, state := range m.pendingOps {
				if state == "starting" {
					return m, tea.Batch(
						func() tea.Msg {
							return readyRetryMsg{label: pendingLabel, retry: 0}
						},
						spinnerCmd(),
					)
				}
			}
		}
		m.pendingOps = make(map[string]string)
		m.lastResult = styles.StyleError.Render("Could not find pending action")
		return m, nil

	case readyRetryMsg:
		container := docker.ContainerName(m.profile.Name, m.envName, m.serviceName)
		ready, _ := docker.QueueContainerRunning(container)
		if ready {
			m.spinnerFrame = 0
			// Execute pending action
			if m.pendingCustomCmd != "" {
				return m, tea.Batch(m.customActionExecCmd(m.pendingCustomCmd), spinnerCmd())
			}
			for _, act := range m.actions {
				if act.label == msg.label && act.enabled && act.execute != nil {
					m.pendingOps[msg.label] = "running"
					return m, tea.Batch(act.execute, spinnerCmd())
				}
			}
			m.pendingOps = make(map[string]string)
			m.lastResult = styles.StyleError.Render("Could not find pending action")
			return m, nil
		}
		if msg.retry >= 30 {
			m.pendingOps = make(map[string]string)
			m.lastResult = formatErrorMsg("container not ready after 30s")
			return m, nil
		}
		return m, tea.Batch(
			func() tea.Msg {
				time.Sleep(1 * time.Second)
				return readyRetryMsg{label: msg.label, retry: msg.retry + 1}
			},
			spinnerCmd(),
		)
	}
	return m, nil
}

// ── Param prompt helpers (textarea-aware) ──────────────────────────────────────

// isTextareaParam returns true when the param should use a textarea instead of textinput.
func (m *QueueOpsModel) isTextareaParam(param string) bool {
	return param == "message"
}

// setMessageTextareaHeight auto-sizes the message textarea to fit content,
// with a minimum of 6 lines and a dynamic cap based on available screen height.
func (m *QueueOpsModel) setMessageTextareaHeight() {
	lines := len(strings.Split(m.paramTextarea.Value(), "\n"))
	maxH := m.height - 18 // reserve ~18 lines for view chrome
	if maxH < 6 {
		maxH = 6
	}
	switch {
	case lines < 6:
		m.paramTextarea.SetHeight(6)
	case lines > maxH:
		m.paramTextarea.SetHeight(maxH)
	default:
		m.paramTextarea.SetHeight(lines)
	}
}

// refreshParamInput syncs the active input (textarea or textinput) to the current param.
func (m *QueueOpsModel) refreshParamInput() {
	param := m.paramsToPrompt[m.currentParamIdx]
	if m.isTextareaParam(param) {
		m.paramInput.Blur()
		// Start in preview mode — blurred; Enter to edit, esc to navigate params
		m.paramTextarea.Blur()
		if v, ok := m.paramValues[param]; ok && v != "" {
			m.paramTextarea.SetValue(v)
		} else {
			m.paramTextarea.SetValue("")
		}
		m.setMessageTextareaHeight()
		m.paramTextarea.CursorEnd()
	} else {
		m.paramTextarea.Blur()
		m.paramInput.Focus()
		m.paramInput.SetValue("")
		m.paramInput.Placeholder = param
		m.paramInput.CursorEnd()
	}
}

func (m *QueueOpsModel) currentParamValue() string {
	param := m.paramsToPrompt[m.currentParamIdx]
	if m.isTextareaParam(param) {
		return m.paramTextarea.Value()
	}
	return m.paramInput.Value()
}

func (m *QueueOpsModel) setCurrentParamValue() {
	param := m.paramsToPrompt[m.currentParamIdx]
	val := m.currentParamValue()
	if !m.isTextareaParam(param) {
		val = strings.TrimSpace(val)
	}
	m.paramValues[param] = val
}

func (m *QueueOpsModel) allParamsFilled() bool {
	for _, p := range m.paramsToPrompt {
		if v, ok := m.paramValues[p]; !ok || v == "" {
			return false
		}
	}
	return true
}

// submitForm exits param prompting and goes to confirm step (core or custom).
func (m *QueueOpsModel) submitForm() (tea.Model, tea.Cmd) {
	m.promptingParams = false
	if m.promptingForCore {
		m.confirmAction = true
	} else {
		m.confirmCustomAction = true
	}
	return m, nil
}

// formatMessage pretty-prints the message value as JSON if valid.
func (m *QueueOpsModel) formatMessage() {
	val := m.paramTextarea.Value()
	var pretty bytes.Buffer
	if json.Indent(&pretty, []byte(val), "", "  ") == nil {
		m.paramTextarea.SetValue(pretty.String())
		m.setMessageTextareaHeight()
	}
}

// ── Param prompt update ───────────────────────────────────────────────────────

func (m *QueueOpsModel) updateParamPrompt(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)
	if !isKey {
		var cmd tea.Cmd
		if m.isTextareaParam(m.paramsToPrompt[m.currentParamIdx]) {
			m.paramTextarea, cmd = m.paramTextarea.Update(msg)
		} else {
			m.paramInput, cmd = m.paramInput.Update(msg)
		}
		return m, cmd
	}

	isMessage := m.isTextareaParam(m.paramsToPrompt[m.currentParamIdx])

	switch {
	case key.Matches(keyMsg, opsKeyBack):
		if isMessage && m.paramTextarea.Focused() {
			// First esc on message → exit edit mode, back to param navigation
			m.paramTextarea.Blur()
			return m, nil
		}
		m.promptingParams = false

	case key.Matches(keyMsg, opsKeyUp):
		if isMessage && m.paramTextarea.Focused() {
			if m.paramTextarea.Line() > 0 {
				break // textarea handles cursor up within text
			}
		}
		if m.currentParamIdx > 0 {
			m.setCurrentParamValue()
			m.currentParamIdx--
			m.refreshParamInput()
		}
		return m, nil

	case key.Matches(keyMsg, opsKeyDown):
		if isMessage && m.paramTextarea.Focused() {
			totalLines := len(strings.Split(m.paramTextarea.Value(), "\n"))
			if m.paramTextarea.Line() < totalLines-1 {
				break // textarea handles cursor down within text
			}
		}
		if m.currentParamIdx < len(m.paramsToPrompt)-1 {
			m.setCurrentParamValue()
			m.currentParamIdx++
			m.refreshParamInput()
		}
		return m, nil

	case key.Matches(keyMsg, opsKeyEnter):
		if isMessage {
			if !m.paramTextarea.Focused() {
				// Enter on preview → enter edit mode
				m.paramTextarea.Focus()
				return m, nil
			}
			break // Enter in edit mode → newline
		}
		// Non-message: save current field and try to submit
		m.setCurrentParamValue()
		if m.allParamsFilled() {
			return m.submitForm()
		}
		return m, nil

	case keyMsg.String() == "ctrl+f":
		if isMessage {
			if !m.paramTextarea.Focused() {
				break // ignore when not in edit mode
			}
			m.formatMessage()
		}
		return m, nil

	case keyMsg.String() == "ctrl+s":
		if isMessage {
			if !m.paramTextarea.Focused() {
				break // ignore when not in edit mode
			}
			m.setCurrentParamValue()
			m.formatMessage()
			m.paramValues[m.paramsToPrompt[m.currentParamIdx]] = m.paramTextarea.Value()
			if m.allParamsFilled() {
				return m.submitForm()
			}
		}
		return m, nil
	}

	// Delegate to focused input
	if isMessage {
		if m.paramTextarea.Focused() {
			var cmd tea.Cmd
			m.paramTextarea, cmd = m.paramTextarea.Update(msg)
			m.setMessageTextareaHeight()
			return m, cmd
		}
		// Preview mode: ignore non-navigation keys
		return m, nil
	}
	if m.paramInput.Focused() {
		var cmd tea.Cmd
		m.paramInput, cmd = m.paramInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

// ── Custom confirm update ─────────────────────────────────────────────────────

func (m *QueueOpsModel) updateCustomConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, opsKeyEnter):
			container := docker.ContainerName(m.profile.Name, m.envName, m.serviceName)
			if !m.isContainerRunning(container) && m.targetSyncEnv == false {
				m.confirmCustomAction = false
				if !docker.DaemonAvailable() {
					m.promptStartDaemon = true
				} else {
					m.promptStartService = true
				}
				m.pendingStartLabel = m.actions[m.actionCursor].label
				return m, nil
			}
			m.confirmCustomAction = false
			m.lastResult = ""
			label := "Custom action"
			if m.actionCursor < len(m.actions) {
				label = m.actions[m.actionCursor].label
			}
			m.pendingOps[label] = "running"
			return m, tea.Batch(m.customActionExecCmd(m.pendingCustomCmd), spinnerCmd())
		case key.Matches(msg, opsKeyBack):
			m.confirmCustomAction = false
		default:
			m.confirmCustomAction = false
		}
	}
	return m, nil
}

// ── Core confirm update (double confirmation) ─────────────────────────────────

func (m *QueueOpsModel) updateCoreConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, opsKeyEnter):
			m.confirmAction = false
			act := m.actions[m.actionCursor]

			// For local targeting, check container is running
			if m.targetSyncEnv == false {
				container := docker.ContainerName(m.profile.Name, m.envName, m.serviceName)
				if !m.isContainerRunning(container) {
					if !docker.DaemonAvailable() {
						m.promptStartDaemon = true
					} else {
						m.promptStartService = true
					}
					m.pendingStartLabel = act.label
					return m, nil
				}
			}

			// Build execute closure for pre-defined actions with params
			var execCmd tea.Cmd
			if act.execute != nil {
				execCmd = act.execute
			} else if params := m.actionParams(act); len(params) > 0 {
				// Build dynamic execution for param-based actions
				execCmd = m.buildParamActionCmd(act)
			}

			if execCmd != nil {
				m.pendingOps[act.label] = "running"
				m.lastResult = ""
				return m, tea.Batch(execCmd, spinnerCmd())
			}
		default:
			m.confirmAction = false
		}
	}
	return m, nil
}

// buildParamActionCmd creates a tea.Cmd for a pre-defined action that needs param substitution.
func (m *QueueOpsModel) buildParamActionCmd(act actionItem) tea.Cmd {
	container := docker.ContainerName(m.profile.Name, m.envName, m.serviceName)
	queueType := m.currentQueueType()

	return func() tea.Msg {
		var program string
		var args []string
		switch {
		case queueType == "kafka":
			bs := "localhost:9092"
			if m.targetSyncEnv {
				svc := m.currentService()
				if svc != nil {
					qse := svc.FindQueueSyncEnv(m.selectedEnv)
					if qse != nil {
						bs = qse.BootstrapServers
						if bs == "" {
							bs = qse.Host + ":" + qse.Port
						}
					}
				}
			}
			switch act.label {
			case "Describe topic":
				program, args = "/usr/bin/kafka-topics", []string{"--bootstrap-server", bs, "--describe", "--topic", m.paramValues["topic"]}
			case "Create topic":
				program, args = "/usr/bin/kafka-topics", []string{"--bootstrap-server", bs, "--create", "--topic", m.paramValues["topic"], "--partitions", m.paramValues["partitions"], "--replication-factor", m.paramValues["replication_factor"]}
			case "Delete topic":
				program, args = "/usr/bin/kafka-topics", []string{"--bootstrap-server", bs, "--delete", "--topic", m.paramValues["topic"]}
			case "Send message":
				program = "sh"
				args = []string{"-c", fmt.Sprintf(
					`/usr/bin/kafka-console-producer --bootstrap-server '%s' --topic '%s' <<'INNEREOF'
%s
INNEREOF`,
					bs, m.paramValues["topic"], m.paramValues["message"],
				)}
			case "Consume messages":
				program, args = "/usr/bin/kafka-console-consumer", []string{"--bootstrap-server", bs, "--topic", m.paramValues["topic"], "--max-messages", "1", "--from-beginning"}
			case "Describe group":
				program, args = "/usr/bin/kafka-consumer-groups", []string{"--bootstrap-server", bs, "--describe", "--group", m.paramValues["group"]}
			case "Reset offset":
				program, args = "/usr/bin/kafka-consumer-groups", []string{"--bootstrap-server", bs, "--reset-offsets", "--group", m.paramValues["group"], "--to-earliest", "--execute", "--topic", m.paramValues["topic"]}
			}

		case queueType == "rabbitmq":
			hostFlag := "localhost"
			portFlag := "15672"
			userFlag := ""
			passFlag := ""
			if m.targetSyncEnv {
				svc := m.currentService()
				if svc != nil {
					qse := svc.FindQueueSyncEnv(m.selectedEnv)
					if qse != nil {
						hostFlag = qse.Host
						if qse.Port != "" {
							portFlag = qse.Port
						}
						userFlag = qse.User
						passFlag = qse.Password
					}
				}
			}
			// Build rabbitmqadmin base args with host, port, and optional auth
			rabbitBase := []string{"-H", hostFlag, "-P", portFlag}
			if userFlag != "" {
				rabbitBase = append(rabbitBase, "-u", userFlag, "-p", passFlag)
			}
			switch act.label {
			case "Create queue":
				program = "/usr/local/bin/rabbitmqadmin"
				args = append(rabbitBase, "declare", "queue", "name="+m.paramValues["name"])
			case "Delete queue":
				program = "/usr/local/bin/rabbitmqadmin"
				args = append(rabbitBase, "delete", "queue", "name="+m.paramValues["name"])
			case "Purge queue":
				program = "/usr/local/bin/rabbitmqadmin"
				args = append(rabbitBase, "purge", "queue", "name="+m.paramValues["name"])
			}

		case queueType == "activemq":
			switch act.label {
			case "Browse queue":
				program = "activemq"
				args = []string{"browse", m.paramValues["queue"]}
				if m.targetSyncEnv {
					svc := m.currentService()
					if svc != nil {
						qse := svc.FindQueueSyncEnv(m.selectedEnv)
						if qse != nil {
							port := qse.Port
							if port == "" {
								port = "61616"
							}
							amqURL := "tcp://" + qse.Host + ":" + port
							args = append(args, "--amqurl", amqURL)
						}
					}
				}
		case "Purge queue":
			program = "activemq"
			// --jmxlocal avoids a NullPointerException in ActiveMQ 6.2.0
			// that occurs with --jmxurl when running purge locally.
			args = []string{"purge", m.paramValues["queue"], "--jmxlocal"}
			if m.targetSyncEnv {
				svc := m.currentService()
				if svc != nil {
					qse := svc.FindQueueSyncEnv(m.selectedEnv)
					if qse != nil {
						jmxURL := "service:jmx:rmi:///jndi/rmi://" + qse.Host + ":1099/jmxrmi"
						args = []string{"purge", m.paramValues["queue"], "--jmxurl", jmxURL}
					}
				}
			}
			case "Send message":
				program = "activemq"
				args = []string{"producer", "--destination", m.paramValues["queue"], "--message", m.paramValues["message"], "--messageCount", "1"}
				if m.targetSyncEnv {
					svc := m.currentService()
					if svc != nil {
						qse := svc.FindQueueSyncEnv(m.selectedEnv)
						if qse != nil {
							port := qse.Port
							if port == "" {
								port = "61616"
							}
							amqURL := "tcp://" + qse.Host + ":" + port
							args = append(args, "--brokerUrl", amqURL)
						}
					}
				}
			case "Consume messages":
				program = "activemq"
				args = []string{"consumer", "--destination", m.paramValues["queue"], "--messageCount", "1"}
				if m.targetSyncEnv {
					svc := m.currentService()
					if svc != nil {
						qse := svc.FindQueueSyncEnv(m.selectedEnv)
						if qse != nil {
							port := qse.Port
							if port == "" {
								port = "61616"
							}
							amqURL := "tcp://" + qse.Host + ":" + port
							args = append(args, "--brokerUrl", amqURL)
						}
					}
				}
			}
		}

		var out string
		var err error
		if m.targetSyncEnv {
			svc := m.currentService()
			if svc == nil {
				return queueOpsDoneMsg{err: fmt.Errorf("service not available")}
			}
			qse := svc.FindQueueSyncEnv(m.selectedEnv)
			if qse == nil {
				return queueOpsDoneMsg{err: fmt.Errorf("sync environment %q not found", m.selectedEnv)}
			}
			out, err = docker.QueueExecRemote(qse, program, args...)
		} else {
			out, err = docker.QueueExecLocal(container, program, args...)
		}
		if err != nil {
			return queueOpsDoneMsg{err: err}
		}
		return queueOpsDoneMsg{result: out}
	}
}

// ── Custom form update ────────────────────────────────────────────────────────

// saveCustomForm validates and persists the custom action form (add or edit).
func (m *QueueOpsModel) saveCustomForm() (tea.Model, tea.Cmd) {
	if m.customNameInput.Value() == "" || m.customCmdInput.Value() == "" {
		return m, nil
	}
	svc := m.currentService()
	if svc == nil {
		return m, nil
	}
	qse := svc.FindQueueSyncEnv(m.selectedEnv)
	if qse == nil {
		return m, nil
	}
	name := strings.TrimSpace(m.customNameInput.Value())
	cmd := strings.TrimSpace(m.customCmdInput.Value())
	if m.editingCustomIdx >= 0 && m.editingCustomIdx < len(qse.CustomActions) {
		qse.CustomActions[m.editingCustomIdx].Name = name
		qse.CustomActions[m.editingCustomIdx].Command = cmd
	} else {
		qse.CustomActions = append(qse.CustomActions, config.CustomAction{Name: name, Command: cmd})
	}
	m.profile.UpdatedAt = time.Now()
	if err := m.cfg.UpdateProfile(*m.profile); err != nil {
		m.lastResult = formatErrorMsg(err.Error())
		return m, nil
	}
	m.addingCustomAction = false
	m.actions = m.buildActionsList(qse)
	if m.actionCursor >= len(m.actions) {
		m.actionCursor = len(m.actions) - 1
	}
	return m, nil
}

func (m *QueueOpsModel) updateCustomForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)
	if !isKey {
		var cmd tea.Cmd
		if m.customFormFocus == 0 {
			m.customNameInput, cmd = m.customNameInput.Update(msg)
		} else {
			m.customCmdInput, cmd = m.customCmdInput.Update(msg)
		}
		return m, cmd
	}

	switch {
	case key.Matches(keyMsg, opsKeyBack):
		m.addingCustomAction = false

	case key.Matches(keyMsg, opsKeyUp):
		if m.customFormFocus > 0 {
			m.customFormFocus--
		}

	case key.Matches(keyMsg, opsKeyDown):
		if m.customFormFocus < 1 {
			m.customFormFocus++
		}

	case key.Matches(keyMsg, opsKeyEnter):
		return m.saveCustomForm()

	case keyMsg.String() == "ctrl+s":
		return m.saveCustomForm()

	default:
		var cmd tea.Cmd
		if m.customFormFocus == 0 {
			m.customNameInput, cmd = m.customNameInput.Update(msg)
		} else {
			m.customCmdInput, cmd = m.customCmdInput.Update(msg)
		}
		return m, cmd
	}
	return m, nil
}

// ── Custom action execution ───────────────────────────────────────────────────

func (m *QueueOpsModel) customActionExecCmd(cmd string) tea.Cmd {
	return func() tea.Msg {
		svc := m.currentService()
		if svc == nil {
			return queueOpsDoneMsg{err: fmt.Errorf("service not found")}
		}
		qse := svc.FindQueueSyncEnv(m.selectedEnv)
		if qse == nil && m.targetSyncEnv {
			return queueOpsDoneMsg{err: fmt.Errorf("queue sync env not found")}
		}

		container := docker.ContainerName(m.profile.Name, m.envName, m.serviceName)

		// Auto-variables
		autoVars := map[string]string{
			"service":   m.serviceName,
			"container": container,
		}
		if qse != nil {
			autoVars["qs_host"] = qse.Host
			autoVars["qs_port"] = qse.Port
			autoVars["qs_user"] = qse.User
			autoVars["qs_pass"] = qse.Password
			autoVars["qs_type"] = qse.QueueType
			autoVars["qs_bs"] = qse.BootstrapServers
			autoVars["qs_vhost"] = qse.VHost
			autoVars["qs_broker"] = qse.BrokerURL
		}

		// Substitute user params and auto vars
		substituted := cmd
		for k, v := range m.paramValues {
			substituted = strings.ReplaceAll(substituted, "{"+k+"}", v)
		}
		for k, v := range autoVars {
			if v != "" {
				substituted = strings.ReplaceAll(substituted, "{"+k+"}", v)
			}
		}

		// Execute the command string as a shell command
		var out string
		var err error
		if qse != nil && m.targetSyncEnv {
			out, err = docker.QueueExecRemote(qse, "sh", "-c", substituted)
		} else {
			out, err = docker.QueueExecLocal(container, "sh", "-c", substituted)
		}
		if err != nil {
			return queueOpsDoneMsg{err: err}
		}
		return queueOpsDoneMsg{result: out}
	}
}

// ── Wizard delegate ───────────────────────────────────────────────────────────

func (m *QueueOpsModel) updateWizard(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case queueSyncEnvSavedMsg:
		m.wizardModel = nil
		m.screen = queueScreenSyncEnvList
		// Re-read profile (it was updated by the wizard via cfg.UpdateProfile)
		if p, ok := m.cfg.GetProfile(m.profile.Name); ok {
			m.profile = p
		}
		return m, nil
	}

	if m.wizardModel == nil {
		m.screen = queueScreenSyncEnvList
		return m, nil
	}

	wm, cmd := m.wizardModel.Update(msg)
	if wm == nil {
		// Wizard cancelled (esc from name step)
		m.wizardModel = nil
		m.screen = queueScreenSyncEnvList
		return m, nil
	}
	m.wizardModel = wm
	return m, cmd
}

func (m *QueueOpsModel) viewWizard() string {
	if m.wizardModel == nil {
		return ""
	}
	return m.wizardModel.View()
}

// ── View dispatch ─────────────────────────────────────────────────────────────

func (m *QueueOpsModel) View() string {
	switch m.screen {
	case queueScreenActions:
		return m.viewActions()
	case queueScreenWizard:
		return m.viewWizard()
	default:
		return m.viewSyncEnvList()
	}
}

// ── View: Actions screen ──────────────────────────────────────────────────────

func (m *QueueOpsModel) viewActions() string {
	if m.viewingResult {
		// Scroll hint: shows direction arrows when content is scrollable
		scrollHint := ""
		atTop := m.resultViewport.AtTop()
		atBottom := m.resultViewport.AtBottom()
		switch {
		case !atTop && !atBottom:
			scrollHint = "\n  " + styles.StyleHelp.Render("▲ up  ▼ down") + "\n"
		case !atTop:
			scrollHint = "\n  " + styles.StyleHelp.Render("▲ up") + "\n"
		case !atBottom:
			scrollHint = "\n  " + styles.StyleHelp.Render("▼ down") + "\n"
		}
		titleLine := styles.StyleHeader.Render(m.resultTitle)
		return titleLine + "\n\n" + m.resultViewport.View() + scrollHint + "\n  " + styles.StyleHelp.Render("press any key to close")
	}

	svc := m.currentService()
	if svc == nil {
		m.screen = queueScreenSyncEnvList
		return m.viewSyncEnvList()
	}

	var b strings.Builder
	b.WriteString("\n")

	targetLabel := "local (docker exec)"
	if m.targetSyncEnv {
		targetLabel = m.selectedEnvName
	}
	b.WriteString(styles.StyleTitle.Render(
		fmt.Sprintf(" Queue Actions — %s ", m.serviceName)))
	b.WriteString("\n\n")
	b.WriteString(styles.StyleSubtitle.Render(
		fmt.Sprintf("  Target: %s  |  Type: %s", targetLabel, m.currentQueueType()),
	))
	b.WriteString("\n\n")

	// Custom action form
	if m.addingCustomAction {
		b.WriteString(m.viewCustomForm())
		return b.String()
	}

	// Param prompting
	if m.promptingParams {
		b.WriteString(m.viewParamPrompt())
		return b.String()
	}

	// Action group sections
	groupHeaderStyle := lipgloss.NewStyle().
		Foreground(styles.ColorPrimary).
		Bold(true)
	selStyle := lipgloss.NewStyle().
		Foreground(styles.ColorPrimary).
		Bold(true)
	customSelStyle := lipgloss.NewStyle().
		Foreground(styles.ColorAccent).
		Bold(true)
	addSelStyle := lipgloss.NewStyle().
		Foreground(styles.ColorPrimary).
		Bold(true)

	// Windowing for actions list
	totalActions := len(m.actions)
	overhead := 12 // title, subtitle, help, margins
	if m.lastResult != "" {
		overhead += 2
	}
	if m.deleteActionArmed {
		overhead++
	}
	if m.promptStartDaemon || m.promptStartService {
		overhead += 3
	}
	if m.confirmAction || m.confirmCustomAction {
		overhead += 2
	}
	win := m.queueOpsWindow(totalActions, m.actionCursor, 2, overhead)

	if win.hasAbove {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", win.start)))
		b.WriteString("\n")
	}

	currentGroup := ""
	for i := win.start; i < win.end; i++ {
		act := m.actions[i]
		if act.group != currentGroup {
			currentGroup = act.group
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString("  " + groupHeaderStyle.Render(currentGroup))
			b.WriteString("\n")
		}

		isCursor := i == m.actionCursor

		if !act.enabled {
			b.WriteString(styles.StyleSubtitle.Render("      " + act.label) + "\n")
			if act.disabledReason != "" {
				b.WriteString(styles.StyleSubtitle.Render("      unavailable: " + act.disabledReason) + "\n")
			}
			continue
		}

		if state, pending := m.pendingOps[act.label]; pending {
			b.WriteString("    " + m.renderSpinnerDot(state) + "\n")
			continue
		}
		if m.doneOp == act.label && time.Since(m.doneOpAt) < 5*time.Second {
			b.WriteString("    " + styles.StyleSuccess.Render("✓") + " " + act.label + "\n")
			continue
		}
		if len(m.pendingOps) > 0 {
			b.WriteString("      " + act.label + "\n")
			continue
		}

		if isCursor {
			if act.isCustom {
				b.WriteString(customSelStyle.Render("    > " + act.label) + "\n")
			} else if act.isAddAction {
				b.WriteString(addSelStyle.Render("    > " + act.label) + "\n")
			} else {
				b.WriteString(selStyle.Render("    > " + act.label) + "\n")
			}
		} else {
			b.WriteString("      " + act.label + "\n")
		}
	}

	if win.hasBelow {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↓ %d more", totalActions-win.end)))
		b.WriteString("\n")
	}

	b.WriteString("\n")

	if m.lastResult != "" {
		b.WriteString("  " + m.lastResult + "\n\n")
	}

	if m.deleteActionArmed {
		b.WriteString(styles.StyleError.Render("  Press d again to delete this custom action") + "\n")
	}

	if m.promptStartDaemon {
		promptStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		b.WriteString(styles.StyleError.Render("  Docker daemon is not running.") + "\n")
		b.WriteString("  " + promptStyle.Render("▸ Start it?") + "  " + styles.StyleSubtitle.Render("(y/n/esc)") + "\n\n")
	} else if m.promptStartService {
		promptStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		b.WriteString(styles.StyleError.Render("  Service '" + m.serviceName + "' is not running.") + "\n")
		b.WriteString("  " + promptStyle.Render("▸ Start it?") + "  " + styles.StyleSubtitle.Render("(y/n/esc)") + "\n\n")
	}

	if m.confirmAction {
		b.WriteString(styles.StyleError.Render("  Press enter to execute, any other key to cancel") + "\n\n")
		b.WriteString(styles.BackHelp(styles.HelpItem("↵", "execute")))
	} else if m.confirmCustomAction {
		b.WriteString(styles.StyleError.Render("  Press enter to execute, any other key to cancel") + "\n\n")
		b.WriteString(styles.BackHelp(styles.HelpItem("↵", "execute")))
	} else {
		var contextItems []styles.HelpItemResult
		if m.actionCursor < len(m.actions) && m.actions[m.actionCursor].isCustom {
			contextItems = append(contextItems, styles.HelpItem("e", "edit"), styles.HelpItem("d d", "delete"))
		}
		if len(contextItems) > 0 {
			b.WriteString("      " + styles.InlineHelp(contextItems...))
			b.WriteString("\n\n")
		}
		b.WriteString(styles.BackHelp(
			styles.HelpItem("↑ ↓", "navigate"), styles.HelpItem("↵", "run"),
		))
	}
	return b.String()
}

// ── Custom form view ──────────────────────────────────────────────────────────

func (m *QueueOpsModel) viewCustomForm() string {
	var b strings.Builder
	titleStr := "Add Custom Action"
	if m.editingCustomIdx >= 0 {
		titleStr = "Edit Custom Action"
	}
	b.WriteString(styles.StyleSubtitle.Render("  " + titleStr) + "\n\n")

	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	normalStyle := lipgloss.NewStyle()

	// Name field
	if m.customFormFocus == 0 {
		m.customNameInput.Focus()
		m.customCmdInput.Blur()
		b.WriteString("  " + focusStyle.Render("Name:    ") + m.customNameInput.View() + "\n")
	} else {
		m.customNameInput.Blur()
		b.WriteString("  " + normalStyle.Render("Name:    ") + m.customNameInput.View() + "\n")
	}

	// Command field
	if m.customFormFocus == 1 {
		m.customCmdInput.Focus()
		m.customNameInput.Blur()
		b.WriteString("  " + focusStyle.Render("Command: ") + m.customCmdInput.View() + "\n\n")
	} else {
		m.customCmdInput.Blur()
		b.WriteString("  " + normalStyle.Render("Command: ") + m.customCmdInput.View() + "\n\n")
	}

	b.WriteString("  " + styles.InlineHelp(
		styles.HelpItem("esc", "cancel"),
		styles.HelpItem("↑ ↓", "field"),
		styles.HelpItem("↵", "save"),
		styles.HelpItem("ctrl+s", "save"),
	))
	return b.String()
}

// ── Param prompt view ─────────────────────────────────────────────────────────

func (m *QueueOpsModel) viewParamPrompt() string {
	var b strings.Builder
	b.WriteString(styles.StyleSubtitle.Render("  Enter parameter values") + "\n\n")

	cursorStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	filledStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	emptyStyle := lipgloss.NewStyle().Foreground(styles.ColorMuted)

	// Fixed width for param lines ensures old content is fully overwritten
	// when switching between cursor/non-cursor views of the same line.
	linePad := lipgloss.NewStyle().Width(60)

	onMessage := false
	if m.currentParamIdx < len(m.paramsToPrompt) {
		onMessage = m.isTextareaParam(m.paramsToPrompt[m.currentParamIdx])
	}

	for i, param := range m.paramsToPrompt {
		if i == m.currentParamIdx && onMessage && m.isTextareaParam(param) {
			continue
		}
		if i == m.currentParamIdx {
			line := "  " + cursorStyle.Render("> "+param+"  ") + m.paramInput.View()
			b.WriteString(linePad.Render(line) + "\n")
		} else if v, ok := m.paramValues[param]; ok && v != "" {
			b.WriteString("    " + param + "  " + filledStyle.Render(v) + "\n")
		} else {
			b.WriteString("    " + param + "  " + emptyStyle.Render("-") + "\n")
		}
	}

	if onMessage {
		b.WriteString("  " + cursorStyle.Render("> message  ") + "\n\n")
		ta := m.paramTextarea.View()
		taStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(styles.ColorPrimary).
			Padding(0, 1)
		b.WriteString(taStyle.Render(ta))
		b.WriteString("\n")
	}

	b.WriteString("\n")

	if onMessage {
		b.WriteString("  " + styles.InlineHelp(
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("↑ ↓", "navigate"),
			styles.HelpItem("enter", "newline"),
			styles.HelpItem("ctrl+f", "format"),
			styles.HelpItem("ctrl+s", "save"),
		))
	} else {
		// Dynamic label: "save" if just storing the field, "submit" if it triggers the action
		willSubmit := false
		if m.currentParamIdx == len(m.paramsToPrompt)-1 {
			othersFilled := true
			for i, p := range m.paramsToPrompt {
				if i == m.currentParamIdx {
					continue
				}
				if v, ok := m.paramValues[p]; !ok || v == "" {
					othersFilled = false
					break
				}
			}
			willSubmit = othersFilled
		}
		enterLabel := "save"
		if willSubmit {
			enterLabel = "submit"
		}
		b.WriteString("  " + styles.InlineHelp(
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("↑ ↓", "navigate"),
			styles.HelpItem("enter", enterLabel),
		))
	}
	return b.String()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// queueOpsWindow calculates the visible item range given total items, cursor
// position, terminal height, and per-item height estimate.
func (m *QueueOpsModel) queueOpsWindow(total, cursor, itemHeight, overhead int) itemWindow {
	w := itemWindow{start: 0, end: total}
	if total == 0 || m.height <= 0 {
		return w
	}
	avail := m.height - overhead
	if avail <= 0 {
		return itemWindow{start: cursor, end: min(cursor+1, total), hasAbove: cursor > 0, hasBelow: cursor+1 < total}
	}
	fit := avail / itemHeight
	if fit <= 0 {
		fit = 1
	}
	if total <= fit {
		return w
	}
	half := fit / 2
	start := cursor - half
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

func (m *QueueOpsModel) renderSpinnerDot(op string) string {
	return opsRenderSpinnerDot(m.spinnerFrame, op)
}

func (m *QueueOpsModel) firstEnabledAction() int {
	return opsFirstEnabledAction(m.actions)
}

func (m *QueueOpsModel) isContainerRunning(container string) bool {
	return opsIsContainerRunning(m.profile.Name, m.envName, container)
}

func (m *QueueOpsModel) launchDockerCmd() tea.Cmd {
	return opsLaunchDockerCmd()
}

// cleanActiveMQOutput strips JVM startup noise and internal advisory queues
// from activemq CLI output, keeping only the meaningful data.
func cleanActiveMQOutput(out string) string {
	// Strip JVM noise at the start: everything after ACTIVEMQ_DATA:
	if idx := strings.LastIndex(out, "ACTIVEMQ_DATA:"); idx >= 0 {
		if end := strings.Index(out[idx:], "\n"); end >= 0 {
			out = out[idx+end+1:]
		}
	}
	// Strip "Connecting to JMX URL:" line (remote commands)
	if idx := strings.LastIndex(out, "Connecting to JMX URL:"); idx >= 0 {
		if end := strings.Index(out[idx:], "\n"); end >= 0 {
			out = out[idx+end+1:]
		}
	}
	// Filter out internal advisory queues (system noise)
	lines := strings.Split(out, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip ActiveMQ advisory queues — they're internal system topics/queues
		if strings.Contains(trimmed, "ActiveMQ.Advisory") {
			continue
		}
		// Skip INFO lines — they're internal progress/confirmation noise.
		// activemq producer: "INFO | producer-1 Produced: X messages"
		// activemq consumer: "INFO | consumer-1 Consumed: X messages"
		// activemq purge:    "INFO: Purging all messages in queue: X"
		// The checkmark (✓) in the action list for 5s is enough
		// confirmation — no need for an intrusive result panel.
		// Data commands (dstat, browse, list) use a different output
		// format without INFO prefix, so they're unaffected.
		if strings.HasPrefix(trimmed, "INFO |") || strings.HasPrefix(trimmed, "INFO:") {
			continue
		}
		result = append(result, line)
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

// cleanActiveMQError cleans an activemq CLI error message for display.
// QueueExecLocal wraps the raw CombinedOutput with "activemq: <out>: <err>".
// This strips JVM noise and, if nothing useful remains, shows a concise
// fallback instead of a raw dump.
func (m *QueueOpsModel) cleanActiveMQError(errMsg string) string {
	if !strings.HasPrefix(errMsg, "activemq: ") {
		// Not from activemq — still try to clean noise from raw output
		cleaned := cleanActiveMQOutput(errMsg)
		if cleaned != "" {
			return cleaned
		}
		return errMsg
	}
	cleaned := strings.TrimPrefix(errMsg, "activemq: ")
	cleaned = cleanActiveMQOutput(cleaned)
	if cleaned == "" {
		// All output was filtered — likely a timeout on an empty queue.
		// Show a concise fallback instead of the raw noise.
		if strings.Contains(errMsg, "signal: killed") || strings.Contains(errMsg, "context deadline exceeded") {
			cleaned = "timed out waiting for messages"
		} else {
			cleaned = "command failed"
		}
	}
	return "activemq: " + cleaned
}

// ── Table Formatting ────────────────────────────────────────────────────────────

// reTableSplit splits on 2+ spaces, preserving multi-word column names.
var reTableSplit = regexp.MustCompile(`\s{2,}`)

// padRight pads s on the right to at least the given width.
func padRight(s string, width int) string {
	if d := width - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// padCenter pads s equally on both sides to at least the given width.
func padCenter(s string, width int) string {
	d := width - lipgloss.Width(s)
	if d <= 0 {
		return s
	}
	left := d / 2
	right := d - left
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
}

// formatActiveMQTable renders a fixed-format CLI table (like dstat) as a styled
// lipgloss table with aligned columns and consistent theming. Returns the raw
// string unchanged if the output doesn't look like a table.
//
// Each row is split on 2+ consecutive spaces (reTableSplit), preserving
// multi-word column names and handling variable-length values correctly
// (e.g. queue names longer than the header padding).
func formatActiveMQTable(raw string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) < 2 {
		return raw
	}

	// Parse every row by splitting on 2+ spaces — each row independently.
	// This is robust against variable-length values (long queue names, etc.).
	type row []string
	var rows []row
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fields := reTableSplit.Split(trimmed, -1)
		rows = append(rows, fields)
	}
	if len(rows) < 2 {
		return raw
	}
	numCols := len(rows[0])
	if numCols < 3 {
		return raw // not tabular enough to format
	}

	// ── Calculate max width per column across all rows ──────────────────────

	colWidths := make([]int, numCols)
	for _, row := range rows {
		for i, val := range row {
			if i < numCols && lipgloss.Width(val) > colWidths[i] {
				colWidths[i] = lipgloss.Width(val)
			}
		}
	}

	// ── Build total width for the separator ─────────────────────────────────

	totalWidth := 0
	for i, w := range colWidths {
		if i > 0 {
			totalWidth += 2 // inter-column spacing
		}
		totalWidth += w
	}

	var b strings.Builder

	// ── Header row (rows[0]) ────────────────────────────────────────────────

	for i, val := range rows[0] {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(styles.StyleHeader.Render(padCenter(val, colWidths[i])))
	}
	b.WriteString("\n")

	// ── Separator ───────────────────────────────────────────────────────────

	b.WriteString(styles.StyleSubtitle.Render(strings.Repeat("─", totalWidth)))
	b.WriteString("\n")

	// ── Data rows (rows[1:]) ────────────────────────────────────────────────

	nameStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary)
	numStyle := styles.StyleSubtitle
	for _, row := range rows[1:] {
		for i, val := range row {
			if i >= numCols {
				break
			}
			if i > 0 {
				b.WriteString("  ")
			}
			if i == 0 {
				// Queue name — left-aligned, primary (blue)
				b.WriteString(nameStyle.Render(padRight(val, colWidths[i])))
			} else {
				// Numeric values — centered, muted
				b.WriteString(numStyle.Render(padCenter(val, colWidths[i])))
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}

// formatActiveMQBrowse formats ActiveMQ browse output (JMS messages) as
// styled grouped message blocks. Auto-activates when JMSMessageID is present.
// Groups fields by message: each JMSMessageID starts a new block, JMSText
// is shown prominently, and other fields are indented key-value pairs.
func formatActiveMQBrowse(raw string) string {
	if !strings.Contains(raw, "JMSMessageID") {
		return raw
	}

	keyStyle := styles.StyleSubtitle
	valStyle := lipgloss.NewStyle().Foreground(styles.ColorCyan)
	textStyle := styles.StyleHeader
	sepStyle := styles.StyleSubtitle

	// Collect each message as a string block (delimited by JMSText),
	// then reverse so most recent message appears first.
	var messages []string
	var curBuf strings.Builder

	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		cleaned := strings.TrimPrefix(trimmed, "JMS_HEADER_FIELD:")
		cleaned = strings.TrimPrefix(cleaned, "JMS_BODY_FIELD:")

		parts := strings.SplitN(cleaned, " = ", 2)
		if len(parts) != 2 {
			curBuf.WriteString(line + "\n")
			continue
		}

		key, val := parts[0], parts[1]

		if key == "JMSText" {
			// Flush previous message
			if curBuf.Len() > 0 {
				messages = append(messages, strings.TrimRight(curBuf.String(), " \n\r"))
				curBuf.Reset()
			}
			curBuf.WriteString("  ")
			curBuf.WriteString(textStyle.Render(val))
			curBuf.WriteString("\n")
		} else {
			curBuf.WriteString("    ")
			curBuf.WriteString(keyStyle.Render(key))
			curBuf.WriteString("  ")
			curBuf.WriteString(valStyle.Render(val))
			curBuf.WriteString("\n")
		}
	}

	// Flush last message
	if curBuf.Len() > 0 {
		messages = append(messages, strings.TrimRight(curBuf.String(), " \n\r"))
	}

	// Reverse: most recent message first
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	var b strings.Builder
	for i, msg := range messages {
		if i > 0 {
			b.WriteString("  ")
			b.WriteString(sepStyle.Render(strings.Repeat("─", 40)))
			b.WriteString("\n")
		}
		b.WriteString(msg)
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), " \n\r")
}

// ── Kafka & RabbitMQ Formatting ──────────────────────────────────────────────────

// formatKafkaDescribe formats kafka-topics --describe output as styled key-value
// pairs grouped by topic. Topic headers (no indentation) get primary color and
// bold styling; partition detail lines (tab-indented) are indented with muted keys.
func formatKafkaDescribe(raw string) string {
	if !strings.Contains(raw, "Topic:") {
		return raw
	}

	keyStyle := styles.StyleSubtitle
	valStyle := lipgloss.NewStyle().Foreground(styles.ColorCyan)
	headerStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)

	var b strings.Builder

	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		isPartitionLine := strings.HasPrefix(line, "\t")
		indent := "  "
		style := keyStyle
		if isPartitionLine {
			indent = "    "
		} else {
			style = headerStyle
		}

		// Parse tab-separated "Key: Value" fields
		parts := strings.Split(trimmed, "\t")
		var fields []string
		for i, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}

			// Try "Key: Value" split first
			if kv := strings.SplitN(p, ":", 2); len(kv) == 2 {
				key := strings.TrimSpace(kv[0])
				val := strings.TrimSpace(kv[1])
				if val == "" {
					continue // skip empty values like "Elr: "
				}
				if i == 0 || key == "Topic" {
					fields = append(fields, style.Render(key)+": "+valStyle.Render(val))
				} else {
					fields = append(fields, keyStyle.Render(key)+": "+valStyle.Render(val))
				}
			} else if strings.Contains(p, "=") {
				// Configs use "Key=Value" format
				if eq := strings.SplitN(p, "=", 2); len(eq) == 2 {
					fields = append(fields, keyStyle.Render(eq[0])+"="+valStyle.Render(eq[1]))
				}
			}
		}

		if len(fields) > 0 {
			b.WriteString(indent)
			b.WriteString(strings.Join(fields, "  "))
			b.WriteString("\n")
		}
	}

	return strings.TrimRight(b.String(), " \n\r")
}

// formatRabbitMQTable formats rabbitmqadmin list * output (ASCII table with
// +---+ borders) into a styled table with colorized header and data rows.
func formatRabbitMQTable(raw string) string {
	if !strings.Contains(raw, "+---") {
		return raw
	}

	// Collect data lines (skip border lines and empty lines)
	var dataLines []string
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "+") {
			continue
		}
		if strings.HasPrefix(trimmed, "|") {
			dataLines = append(dataLines, trimmed)
		}
	}

	if len(dataLines) < 2 {
		return raw
	}

	// Parse header and data rows
	headers := parseRMQColumns(dataLines[0])
	if len(headers) < 2 {
		return raw
	}

	// Calculate column widths
	colWidths := make([]int, len(headers))
	for _, line := range dataLines {
		cells := parseRMQColumns(line)
		for i, cell := range cells {
			if i < len(colWidths) && lipgloss.Width(cell) > colWidths[i] {
				colWidths[i] = lipgloss.Width(cell)
			}
		}
	}

	totalWidth := 0
	for i, w := range colWidths {
		if i > 0 {
			totalWidth += 2
		}
		totalWidth += w
	}

	var b strings.Builder

	// Header
	for i, h := range headers {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(styles.StyleHeader.Render(padCenter(h, colWidths[i])))
	}
	b.WriteString("\n")
	b.WriteString(styles.StyleSubtitle.Render(strings.Repeat("─", totalWidth)))
	b.WriteString("\n")

	// Data rows
	nameStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary)
	numStyle := styles.StyleSubtitle
	for _, line := range dataLines[1:] {
		cells := parseRMQColumns(line)
		for i, cell := range cells {
			if i >= len(colWidths) {
				break
			}
			if i > 0 {
				b.WriteString("  ")
			}
			if i == 0 {
				b.WriteString(nameStyle.Render(padRight(cell, colWidths[i])))
			} else {
				b.WriteString(numStyle.Render(padCenter(cell, colWidths[i])))
			}
		}
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), " \n\r")
}

// parseRMQColumns splits a rabbitmqadmin table row (delimited by |) into cells.
func parseRMQColumns(line string) []string {
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	var cols []string
	for _, part := range strings.Split(line, "|") {
		cols = append(cols, strings.TrimSpace(part))
	}
	return cols
}
