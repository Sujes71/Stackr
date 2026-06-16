package views

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/crypto"
	"github.com/stackr/stackr/internal/docker"
	"github.com/stackr/stackr/internal/tui/styles"
)

// ── Internal screen enum ──────────────────────────────────────────────────────

type dbOpsScreen int

const (
	dbScreenSyncEnvList dbOpsScreen = iota
	dbScreenSyncEnvActions
	dbScreenWizard
)

// ── Messages ──────────────────────────────────────────────────────────────────

// dbOpsDoneMsg is sent when an export/import action completes.
type dbOpsDoneMsg struct {
	result string
	err    error
}

// serviceStartedMsg, daemonStartedMsg, readyRetryMsg are defined in ops_common.go.

// ── Model ─────────────────────────────────────────────────────────────────────

// DBOpsModel implements the 3-screen DB operations view.
// Internal screens:
//   - SyncEnv List (default) — CRUD for configured sync-from environments
//   - SyncEnv Actions — export/import actions for the selected sync env
//   - Wizard — delegates to AddSyncEnvModel for add/edit
type DBOpsModel struct {
	cfg         *config.Config
	profile     *config.Profile
	envName     string
	serviceName string
	serviceIdx  int

	// Internal screen
	screen dbOpsScreen

	// SyncEnv list state
	listCursor  int
	deleteArmed bool

	// SyncEnv actions state
	selectedEnv        string
	actionCursor       int
	actions            []actionItem // cached for view rendering


	// Prompt state (start service before import/truncate)
	promptStartService bool
	promptStartDaemon  bool   // Docker daemon needs to start before service
	daemonStarting     bool   // true while Docker daemon is being launched
	pendingStartLabel  string // action label to execute after service/service starts

	// Running state — per-action inline spinners
	pendingOps   map[string]string // action label → "starting" | "running"
	lastResult string
	doneOp     string    // briefly set to completed action label for checkmark feedback
	doneOpAt   time.Time // timestamp of when doneOp was set

	// Spinner
	spinnerFrame int

	// Viewport
	width  int
	height int

	// Wizard (delegated)
	wizardModel *AddSyncEnvModel

	// Custom action form
	addingCustomAction bool
	editingCustomIdx   int // -1 = adding new, >= 0 = editing index
	customFormFocus    int // 0=name, 1=command
	commandEditMode    bool // true = textarea focused, false = preview (blurred)
	customNameInput    textinput.Model
	customCmdInput     textinput.Model
	customCmdTextarea  textarea.Model

	// Custom action param prompting
	promptingParams   bool
	paramsToPrompt    []string
	paramValues       map[string]string
	currentParamIdx   int
	paramInput       textinput.Model

	// Custom action delete
	deleteActionArmed bool

	// Confirm before execute (double confirmation)
	confirmAction       bool
	confirmCustomAction bool
	pendingCustomCmd   string
}

// actionItem is defined in ops_common.go.

// ── Key bindings ──────────────────────────────────────────────────────────────

// Key bindings are defined in ops_common.go (opsKeyUp, opsKeyDown, etc.).

// ── Constructor ───────────────────────────────────────────────────────────────

func NewDBOps(cfg *config.Config, p *config.Profile, envName, serviceName string) *DBOpsModel {
	m := &DBOpsModel{
		cfg:         cfg,
		profile:     p,
		envName:     envName,
		serviceName: serviceName,
		screen:      dbScreenSyncEnvList,
		pendingOps:  make(map[string]string),
		customNameInput: textinput.New(),
		customCmdInput:  textinput.New(),
		paramInput:      textinput.New(),
	}
	m.customNameInput.Placeholder = "Function name"
	m.customCmdInput.Placeholder = "SQL command (use {param} for variables)"
	m.paramInput.Placeholder = "value"
	m.customCmdTextarea = textarea.New()
	m.customCmdTextarea.SetHeight(6)
	m.customCmdTextarea.ShowLineNumbers = false
	m.customCmdTextarea.Placeholder = "SQL command (use {param} for variables)"
	m.customCmdTextarea.CharLimit = 0
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

func (m *DBOpsModel) ProfileName() string { return m.profile.Name }
func (m *DBOpsModel) EnvName() string     { return m.envName }
func (m *DBOpsModel) ServiceName() string { return m.serviceName }

// ServiceKey returns a unique key to identify this service across navigation.
func (m *DBOpsModel) ServiceKey() string {
	return m.profile.Name + "/" + m.envName + "/" + m.serviceName
}

// ReShow refreshes the profile pointer when returning to an active model.
func (m *DBOpsModel) ReShow(cfg *config.Config) {
	if p, ok := cfg.GetProfile(m.profile.Name); ok {
		m.profile = p
	}
	m.deleteArmed = false
	m.deleteActionArmed = false
}

// IsBusy returns true when there are pending operations.
func (m *DBOpsModel) IsBusy() bool {
	return len(m.pendingOps) > 0
}

// ── Environment / Service helpers ─────────────────────────────────────────────

func (m *DBOpsModel) currentEnv() *config.Environment {
	return opsCurrentEnv(m.profile, m.envName)
}

func (m *DBOpsModel) currentService() *config.Service {
	return opsCurrentService(m.currentEnv(), m.serviceName, &m.serviceIdx)
}

func (m *DBOpsModel) dumpDir(se *config.SyncEnv) string {
	return se.DumpDir(m.profile.Name, m.envName, m.serviceName)
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m *DBOpsModel) Init() tea.Cmd {
	return nil
}

func (m *DBOpsModel) SetSize(w, h int) {
	m.width = max(0, w)
	m.height = max(0, h)
	if m.addingCustomAction {
		m.setCustomCmdTextareaHeight()
	}
}

// setCustomCmdTextareaHeight auto-sizes the SQL command textarea to fit content.
// The cap is dynamic so the total custom-form view (builder prefix + form chrome
// + textarea border + help) stays within m.height, keeping the header visible.
func (m *DBOpsModel) setCustomCmdTextareaHeight() {
	lines := len(strings.Split(m.customCmdTextarea.Value(), "\n"))
	// Overhead outside textarea + border when command field is focused:
	//   builder prefix (7) + form chrome (10: title, name, command label,
	//   help, trailing blank) = 17 lines
	maxVisible := m.height - 17
	if maxVisible < 6 {
		maxVisible = 6
	}
	switch {
	case lines < 6:
		m.customCmdTextarea.SetHeight(6)
	case lines > maxVisible:
		m.customCmdTextarea.SetHeight(maxVisible)
	default:
		m.customCmdTextarea.SetHeight(lines)
	}
}

// ── Update dispatch ───────────────────────────────────────────────────────────

func (m *DBOpsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		m.SetSize(msg.Width, msg.Height-5)
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
		if m.doneOp != "" {
			if time.Since(m.doneOpAt) >= 5*time.Second {
				m.doneOp = ""
				m.doneOpAt = time.Time{}
			} else {
				m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
				return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
					return SpinnerTickMsg{}
				})
			}
		}
		return m, nil
	}

	switch m.screen {
	case dbScreenWizard:
		return m.updateWizard(msg)
	case dbScreenSyncEnvActions:
		return m.updateActions(msg)
	default:
		return m.updateList(msg)
	}
}

// ── Task 4.2: SyncEnv List screen (CRUD) ──────────────────────────────────────

func (m *DBOpsModel) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		syncEnvs := svc.SyncEnvs

		switch {

		case key.Matches(msg, opsKeyUp) && m.listCursor > 0 && !m.deleteArmed:
			m.listCursor--

		case key.Matches(msg, opsKeyDown) && m.listCursor < len(syncEnvs)-1 && !m.deleteArmed:
			m.listCursor++

		case key.Matches(msg, opsKeyEnter):
			if len(syncEnvs) > 0 {
				se := &syncEnvs[m.listCursor]
				m.selectedEnv = se.Name
				m.actions = m.buildActionsList(se)
				m.actionCursor = m.firstEnabledAction()
				m.screen = dbScreenSyncEnvActions
				m.deleteArmed = false
			}

		case key.Matches(msg, opsKeyAdd):
			m.deleteArmed = false
			m.wizardModel = NewAddSyncEnv(m.cfg, m.profile, m.envName, m.serviceName, true, nil)
			m.screen = dbScreenWizard

		case key.Matches(msg, opsKeyEdit):
			if len(syncEnvs) > 0 {
				se := &syncEnvs[m.listCursor]
				m.wizardModel = NewAddSyncEnv(m.cfg, m.profile, m.envName, m.serviceName, false, se)
				m.screen = dbScreenWizard
				m.deleteArmed = false
			}

		case key.Matches(msg, opsKeyDelete):
			if len(syncEnvs) == 0 {
				break
			}
			if !m.deleteArmed {
				m.deleteArmed = true
			} else {
				// Second d: confirm delete
				m.deleteArmed = false
				svc.SyncEnvs = append(syncEnvs[:m.listCursor], syncEnvs[m.listCursor+1:]...)
				m.profile.UpdatedAt = time.Now()
			if err := m.cfg.UpdateProfile(*m.profile); err != nil {
				m.lastResult = formatErrorMsg(err.Error())
				return m, nil
			} else {
					if m.listCursor >= len(svc.SyncEnvs) && m.listCursor > 0 {
						m.listCursor--
					}
					return m, nil
				}
			}

	default:
		// Any other key disarms delete
		if m.deleteArmed {
			m.deleteArmed = false
		}
	}

	}

	return m, nil
}

func (m *DBOpsModel) viewSyncEnvList() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.StyleTitle.Render(
		fmt.Sprintf(" DB Ops — %s / %s / %s ", m.profile.Name, m.envName, m.serviceName)))
	b.WriteString("\n\n")
	b.WriteString(styles.StyleSubtitle.Render("  Sync From Environments"))
	b.WriteString("\n\n")

	svc := m.currentService()
	if svc == nil {
		b.WriteString(styles.StyleError.Render("  Service not found."))
		b.WriteString("\n")
		b.WriteString(styles.StyleSubtitle.Render("  This DB Ops entry is stale or the service was removed."))
		b.WriteString("\n")
		b.WriteString(styles.StyleSubtitle.Render("  Press esc to go back to the profile and choose another service."))
		b.WriteString("\n\n")
		b.WriteString(styles.BackHelp())
		return b.String()
	}
	syncEnvs := svc.SyncEnvs

	if len(syncEnvs) == 0 {
		b.WriteString(styles.StyleSubtitle.Render("  No sync-from environments configured."))
		b.WriteString("\n")
		b.WriteString(styles.StyleSubtitle.Render("  Press a to add one."))
		b.WriteString("\n")
	} else {
		selStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)
		// Window the list: overhead = title(3) + subtitle(2) + deleteArmed(2) + lastResult(2) + help(4) = ~13
		overhead := 13
		if m.deleteArmed {
			overhead += 0 // already counted
		}
		if m.lastResult == "" {
			overhead -= 2
		}
		avail := m.height - overhead
		if avail < 1 {
			avail = 1
		}
		total := len(syncEnvs)
		win := dbOpsWindow(total, m.listCursor, avail)
		if win.hasAbove {
			b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", win.start)))
			b.WriteString("\n")
		}
		for i := win.start; i < win.end; i++ {
			se := &syncEnvs[i]
			prefix := "    "
			if i == m.listCursor {
				prefix = "  > "
			}
			dumpIndicator := ""
			if m.hasDumpsFor(se) {
				dumpIndicator = styles.StyleSubtitle.Render("  [dump]")
			}
			line := fmt.Sprintf("%s%s  %s  %s:%s%s",
				prefix, se.Name, se.DBType, se.Host, se.Port, dumpIndicator)
			if i == m.listCursor {
				b.WriteString(selStyle.Render(line))
			} else {
				b.WriteString(line)
			}
			b.WriteString("\n")
		}
		if win.hasBelow {
			b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↓ %d more", total-win.end)))
			b.WriteString("\n")
		}
	}

	if m.deleteArmed {
		b.WriteString("\n" + styles.StyleError.Render("  Press d again to confirm removal"))
		b.WriteString("\n")
	}

	if m.lastResult != "" {
		b.WriteString("\n  " + m.lastResult + "\n")
	}

	b.WriteString("\n")
	// Line 1: contextual actions (affect sync envs)
	var contextItems []styles.HelpItemResult
	contextItems = append(contextItems, styles.HelpItem("a", "add"))
	svc = m.currentService()
	if svc != nil && len(svc.SyncEnvs) > 0 {
		contextItems = append(contextItems, styles.HelpItem("e", "edit"))
		contextItems = append(contextItems, styles.HelpItem("d d", "delete"))
	}
	b.WriteString("      " + styles.InlineHelp(contextItems...))
	b.WriteString("\n\n")
	// Line 2: global navigation
	b.WriteString(styles.BackHelp(
		styles.HelpItem("↑ ↓", "navigate"),
		styles.HelpItem("↵", "open"),
	))
	return b.String()
}

// ── Task 4.3: SyncEnv Actions screen ─────────────────────────────────────────

func (m *DBOpsModel) updateActions(msg tea.Msg) (tea.Model, tea.Cmd) {
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

	// Block keys while Docker daemon is starting (system msgs still pass through)
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

	// Param prompting for custom action
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
		switch {
		case key.Matches(msg, opsKeyBack):
			if m.promptStartDaemon || len(m.pendingOps) > 0 {
				if m.promptStartDaemon {
					m.promptStartDaemon = false
					m.promptStartService = false
				}
				break // block navigation while ops are running
			}
			m.deleteArmed = false
			m.deleteActionArmed = false
			m.screen = dbScreenSyncEnvList
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
					m.commandEditMode = true // name field focused
			m.customNameInput.SetValue("")
			m.customCmdTextarea.SetValue("")
			return m, nil
					}
					if act.isCustom {
						svc := m.currentService()
						if svc == nil {
							return m, nil
						}
						se := svc.FindSyncEnv(m.selectedEnv)
						if se == nil || act.customIdx >= len(se.CustomActions) {
							return m, nil
						}
						cmd := se.CustomActions[act.customIdx].Command
						m.pendingCustomCmd = cmd
						params := extractParams(cmd)
						if len(params) > 0 {
							m.promptingParams = true
							m.paramsToPrompt = params
			m.paramValues = make(map[string]string)
			m.currentParamIdx = 0
			m.paramInput.SetValue("")
			m.paramInput.Placeholder = params[0]
			m.paramInput.Focus()
						} else {
							m.confirmCustomAction = true
						}
						return m, nil
					}
					// Core action: require double confirmation
					m.confirmAction = true
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
					se := svc.FindSyncEnv(m.selectedEnv)
					if se == nil || act.customIdx >= len(se.CustomActions) {
						return m, nil
					}
				m.addingCustomAction = true
				m.editingCustomIdx = act.customIdx
				m.customFormFocus = 0
				m.commandEditMode = true // name field focused
			m.customNameInput.SetValue(se.CustomActions[act.customIdx].Name)
			m.customCmdTextarea.SetValue(se.CustomActions[act.customIdx].Command)
				}
			}

		case key.Matches(msg, opsKeyDelete) && len(m.pendingOps) == 0 && !m.promptStartService:
			if m.actionCursor < len(m.actions) {
				act := m.actions[m.actionCursor]
				if act.isCustom {
					if !m.deleteActionArmed {
						m.deleteActionArmed = true
					} else {
						// Second d: confirm delete
						m.deleteActionArmed = false
						svc := m.currentService()
						if svc == nil {
							return m, nil
						}
						se := svc.FindSyncEnv(m.selectedEnv)
						if se == nil || act.customIdx >= len(se.CustomActions) {
							return m, nil
						}
						se.CustomActions = append(se.CustomActions[:act.customIdx], se.CustomActions[act.customIdx+1:]...)
						m.profile.UpdatedAt = time.Now()
						if err := m.cfg.UpdateProfile(*m.profile); err != nil {
							m.lastResult = formatErrorMsg(err.Error())
							return m, nil
						}
						m.actions = m.buildActionsList(se)
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

	case dbOpsDoneMsg:
		// Get label of completing action before clearing
		var completedLabel string
		for label := range m.pendingOps {
			completedLabel = label
		}
		m.pendingOps = make(map[string]string)

		if msg.err != nil {
			m.lastResult = formatErrorMsg(msg.err.Error())
			return m, nil
		}

		// Re-scan dump states — imports/exports affect dump availability
		svc := m.currentService()
		if svc != nil {
			if se := svc.FindSyncEnv(m.selectedEnv); se != nil {
				m.actions = m.buildActionsList(se)
			}
		}
		if m.actionCursor >= len(m.actions) || !m.actions[m.actionCursor].enabled {
			m.actionCursor = m.firstEnabledAction()
		}

		// Show brief checkmark feedback
		m.doneOp = completedLabel
		m.doneOpAt = time.Now()
		return m, spinnerCmd()

	case daemonStartedMsg:
		m.daemonStarting = false
		if !msg.available {
			m.pendingOps = make(map[string]string)
			m.pendingStartLabel = ""
			m.lastResult = formatErrorMsg("Docker daemon failed to start")
			return m, nil
		}
		// Daemon is up — now start the service
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
				// `docker compose up` creates ALL services in the compose file
				// even though it only starts the target one. Stop the others to
				// avoid leaving them in "Created" state.
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
		// Poll container readiness before executing the pending action
		svc := m.currentService()
		if svc != nil {
			if se := svc.FindSyncEnv(m.selectedEnv); se != nil {
				m.actions = m.buildActionsList(se)
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
		}
		m.pendingOps = make(map[string]string)
		m.lastResult = styles.StyleError.Render("Could not find pending action")
		return m, nil

	case readyRetryMsg:
		container := docker.ContainerName(m.profile.Name, m.envName, m.serviceName)
		svc := m.currentService()
		ready := false
		if svc != nil {
			if se := svc.FindSyncEnv(m.selectedEnv); se != nil {
				ready = docker.ContainerReady(container, se.DBType)
			}
		}
		if ready {
			// Execute the pending action
			m.spinnerFrame = 0
			for _, act := range m.actions {
				if act.label == msg.label && act.enabled {
					m.pendingOps[msg.label] = "running"
					if act.isCustom {
						return m, tea.Batch(m.customActionExecCmd(m.pendingCustomCmd), spinnerCmd())
					}
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

// ── Param prompt update ───────────────────────────────────────────────────────

func (m *DBOpsModel) updateParamPrompt(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)
	if !isKey {
		var cmd tea.Cmd
		m.paramInput, cmd = m.paramInput.Update(msg)
		return m, cmd
	}

	switch {
	case key.Matches(keyMsg, opsKeyBack):
		m.promptingParams = false
	case key.Matches(keyMsg, opsKeyEnter):
		if m.paramInput.Value() != "" {
			paramName := m.paramsToPrompt[m.currentParamIdx]
			m.paramValues[paramName] = m.paramInput.Value()
			m.currentParamIdx++
			m.paramInput.SetValue("")
			m.paramInput.Placeholder = m.paramsToPrompt[min(m.currentParamIdx, len(m.paramsToPrompt)-1)]
			if m.currentParamIdx >= len(m.paramsToPrompt) {
				m.promptingParams = false
				m.confirmCustomAction = true
			}
		}
	default:
		var cmd tea.Cmd
		m.paramInput, cmd = m.paramInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

// ── Custom confirm update ─────────────────────────────────────────────────────

func (m *DBOpsModel) updateCustomConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, opsKeyEnter):
			container := docker.ContainerName(m.profile.Name, m.envName, m.serviceName)
			if !m.isContainerRunning(container) {
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

func (m *DBOpsModel) updateCoreConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, opsKeyEnter):
			m.confirmAction = false
			act := m.actions[m.actionCursor]
			if act.group == "Import" || act.group == "Export" || strings.Contains(act.label, "Truncate") {
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
			m.pendingOps[act.label] = "running"
			m.lastResult = ""
			return m, tea.Batch(act.execute, spinnerCmd())
		default:
			m.confirmAction = false
		}
	}
	return m, nil
}

// ── Custom form update ────────────────────────────────────────────────────────

func (m *DBOpsModel) updateCustomForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)
	if !isKey {
		// Forward non-key messages to focused input
		var cmd tea.Cmd
		if m.customFormFocus == 0 {
			m.customNameInput, cmd = m.customNameInput.Update(msg)
		} else {
			m.customCmdTextarea, cmd = m.customCmdTextarea.Update(msg)
			m.setCustomCmdTextareaHeight()
		}
		return m, cmd
	}

	saveForm := func() (tea.Model, tea.Cmd) {
		cmdVal := m.customCmdTextarea.Value()
		if m.customNameInput.Value() != "" && cmdVal != "" {
			svc := m.currentService()
			if svc == nil {
				return m, nil
			}
			se := svc.FindSyncEnv(m.selectedEnv)
			if se == nil {
				return m, nil
			}
			name := strings.TrimSpace(m.customNameInput.Value())
			cmd := strings.TrimSpace(cmdVal)
			if m.editingCustomIdx >= 0 && m.editingCustomIdx < len(se.CustomActions) {
				se.CustomActions[m.editingCustomIdx].Name = name
				se.CustomActions[m.editingCustomIdx].Command = cmd
			} else {
				se.CustomActions = append(se.CustomActions, config.CustomAction{Name: name, Command: cmd})
			}
			m.profile.UpdatedAt = time.Now()
			if err := m.cfg.UpdateProfile(*m.profile); err != nil {
				m.lastResult = formatErrorMsg(err.Error())
				return m, nil
			}
			m.addingCustomAction = false
			m.actions = m.buildActionsList(se)
			if m.actionCursor >= len(m.actions) {
				m.actionCursor = len(m.actions) - 1
			}
		}
		return m, nil
	}

	switch {
	case key.Matches(keyMsg, opsKeyBack):
		if m.customFormFocus == 1 && m.commandEditMode {
			// First esc on command → exit edit mode
			m.commandEditMode = false
			return m, nil
		}
		m.addingCustomAction = false

	case key.Matches(keyMsg, opsKeyUp):
		if m.customFormFocus == 1 && m.commandEditMode {
			if m.customCmdTextarea.Line() > 0 {
				break // textarea handles cursor up within text
			}
		}
		if m.customFormFocus > 0 {
			m.customFormFocus--
			m.commandEditMode = true // name field is always editable
		}

	case key.Matches(keyMsg, opsKeyDown):
		if m.customFormFocus == 0 {
			// Going from name to command → start in preview mode
			m.customFormFocus = 1
			m.commandEditMode = false
			return m, nil
		}
		if m.customFormFocus == 1 && m.commandEditMode {
			totalLines := len(strings.Split(m.customCmdTextarea.Value(), "\n"))
			if m.customCmdTextarea.Line() < totalLines-1 {
				break // textarea handles cursor down within text
			}
		}

	case key.Matches(keyMsg, opsKeyEnter):
		if m.customFormFocus == 1 {
			if !m.commandEditMode {
				// Enter on preview → enter edit mode
				m.commandEditMode = true
				return m, nil
			}
			break // Enter in edit mode → newline
		}
		// Name field (textinput): save and submit
		return saveForm()

	case keyMsg.String() == "ctrl+s":
		if m.customFormFocus == 1 && m.commandEditMode {
			return saveForm()
		}
		return m, nil

	default:
		// Forward to focused input (handles typing, backspace, cursor, paste)
		var cmd tea.Cmd
		if m.customFormFocus == 0 {
			m.customNameInput, cmd = m.customNameInput.Update(msg)
		} else if m.commandEditMode {
			m.customCmdTextarea, cmd = m.customCmdTextarea.Update(msg)
			m.setCustomCmdTextareaHeight()
		}
		return m, cmd
	}
	return m, nil
}

// ── Custom action execution ───────────────────────────────────────────────────

func (m *DBOpsModel) customActionExecCmd(cmd string) tea.Cmd {
	return func() tea.Msg {
		svc := m.currentService()
		if svc == nil {
			return dbOpsDoneMsg{err: fmt.Errorf("service not found")}
		}
		se := svc.FindSyncEnv(m.selectedEnv)
		if se == nil {
			return dbOpsDoneMsg{err: fmt.Errorf("sync env not found")}
		}

	container := docker.ContainerName(m.profile.Name, m.envName, svc.ComposeName())

	// Get local Docker credentials from service environment variables
	localUser, localPass, localDB := getLocalDBCredentials(svc, se.DBType)

	// Auto-variables: {database}/{db}/{user}/{password} resolve to the LOCAL
	// container credentials because ALL custom function execution runs via
	// docker exec inside the container. Use {se_db} / {se_user} etc. for the
	// SyncEnv's remote credentials (for export-like operations).
	autoVars := map[string]string{
		"host":        se.Host,
		"user":        se.User,
		"password":    se.Password,
		"database":    localDB,
		"db":          localDB,
		"env":         m.envName,
		"local_user":  localUser,
		"local_pass":  localPass,
		"local_db":    localDB,
		"se_db":       se.Database,
		"se_user":     se.User,
		"se_password": se.Password,
	}

	// Substitute all variables (auto + user params)
	substituted := cmd
	for k, v := range m.paramValues {
		substituted = strings.ReplaceAll(substituted, "{"+k+"}", v)
	}
	for k, v := range autoVars {
		if v != "" {
			substituted = strings.ReplaceAll(substituted, "{"+k+"}", v)
		}
	}

	// Auto-wrap in DB client if it looks like raw SQL (no shell command prefix)
	shellCmd := substituted
	if !strings.HasPrefix(shellCmd, "psql") &&
		!strings.HasPrefix(shellCmd, "mysql") &&
		!strings.HasPrefix(shellCmd, "mongo") &&
		!strings.HasPrefix(shellCmd, "mongosh") &&
		!strings.HasPrefix(shellCmd, "sqlcmd") &&
		!strings.HasPrefix(shellCmd, "sh ") &&
		!strings.HasPrefix(shellCmd, "bash ") {
		switch se.DBType {
		case "postgres":
			shellCmd = fmt.Sprintf(`psql -h localhost -U %s -d %s -c "%s"`, localUser, localDB, strings.ReplaceAll(substituted, `"`, `\"`))
		case "mongo":
			shellCmd = fmt.Sprintf(`mongo -u %s -p %s --authenticationDatabase admin %s --eval '%s'`, localUser, localPass, localDB, strings.ReplaceAll(substituted, "'", `'\''`))
		case "mysql", "mariadb":
			shellCmd = fmt.Sprintf(`mysql -h localhost -u %s -p%s %s -e "%s"`, localUser, localPass, localDB, strings.ReplaceAll(substituted, `"`, `\"`))
		case "mssql":
			shellCmd = fmt.Sprintf(`sqlcmd -S localhost -U %s -P %s -d %s -Q "%s"`, localUser, localPass, localDB, strings.ReplaceAll(substituted, `"`, `\"`))
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		dockerCmd := exec.CommandContext(ctx, "docker", "exec", "-i", container, "sh", "-c", shellCmd)
		out, err := dockerCmd.CombinedOutput()
		if err != nil {
			return dbOpsDoneMsg{err: fmt.Errorf("custom action: %s: %w", strings.TrimSpace(string(out)), err)}
		}
		return dbOpsDoneMsg{}
	}
}

// ── Param extraction ──────────────────────────────────────────────────────────

// paramRe, autoVarNames, and extractParams are defined in ops_common.go.

// ── View: Actions screen ──────────────────────────────────────────────────────

func (m *DBOpsModel) viewActions() string {
	svc := m.currentService()
	if svc == nil {
		m.screen = dbScreenSyncEnvList
		return m.viewSyncEnvList()
	}
	syncEnv := svc.FindSyncEnv(m.selectedEnv)
	if syncEnv == nil {
		m.screen = dbScreenSyncEnvList
		return m.View()
	}

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.StyleTitle.Render(
		fmt.Sprintf(" Actions for: %s ", m.selectedEnv)))
	b.WriteString("\n\n")

	// Connection info (inline, no border)
	b.WriteString(styles.StyleSubtitle.Render(
		fmt.Sprintf("  %s  %s:%s  %s@%s", syncEnv.DBType, syncEnv.Host, syncEnv.Port, syncEnv.User, syncEnv.Database),
	))
	if syncEnv.Password != "" && crypto.IsEncrypted(syncEnv.Password) {
		b.WriteString("\n" + styles.StyleSubtitle.Render("  \U0001F512 Password: [encrypted]"))
	}
	b.WriteString("\n\n")

	// ── Custom action form ────────────────────────────────────────────────────
	if m.addingCustomAction {
		b.WriteString(m.viewCustomForm())
		return b.String()
	}

	// ── Param prompting ───────────────────────────────────────────────────────
	if m.promptingParams {
		b.WriteString(m.viewParamPrompt())
		return b.String()
	}

	// ── Confirm messages ──────────────────────────────────────────────────────
	// Rendered in help bar section below

	// Action group sections rendered inline (no boxes)
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

	// Window the actions list: overhead = title(3) + connInfo(2) + password(1) +
	// trailing(8: blank, lastResult, deleteArmed, prompts, help) = ~16
	overhead := 16
	avail := m.height - overhead
	if avail < 1 {
		avail = 1
	}
	total := len(m.actions)
	// Estimate ~2 lines per action (some have disabled reason, group headers)
	itemHeight := 2
	fit := avail / itemHeight
	if fit < 1 {
		fit = 1
	}
	win := dbOpsWindow(total, m.actionCursor, fit)

	if win.hasAbove {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", win.start)))
		b.WriteString("\n")
	}

	// Initialize currentGroup from the item before the window so group headers render correctly
	currentGroup := ""
	if win.start > 0 {
		currentGroup = m.actions[win.start-1].group
	}
	firstVisibleGroup := true
	for i := win.start; i < win.end; i++ {
		act := m.actions[i]
		if act.group != currentGroup {
			currentGroup = act.group
			if !firstVisibleGroup {
				b.WriteString("\n")
			}
			firstVisibleGroup = false
			b.WriteString("  " + groupHeaderStyle.Render(currentGroup))
			switch currentGroup {
			case "Export":
				b.WriteString(" " + styles.StyleSubtitle.Render("("+m.selectedEnv+")"))
			default:
				b.WriteString(" " + styles.StyleSubtitle.Render("(local)"))
			}
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
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↓ %d more", total-win.end)))
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

	if m.confirmAction || m.confirmCustomAction {
		b.WriteString(styles.StyleError.Render("  Press enter to execute, any other key to cancel") + "\n\n")
		b.WriteString(styles.BackHelp(styles.HelpItem("↵", "execute")))
	} else {
		// Line 1: contextual actions (affect selected action)
		var contextItems []styles.HelpItemResult
		if m.actionCursor < len(m.actions) && m.actions[m.actionCursor].isCustom {
			contextItems = append(contextItems, styles.HelpItem("e", "edit"), styles.HelpItem("d d", "delete"))
		}
		if len(contextItems) > 0 {
			b.WriteString("      " + styles.InlineHelp(contextItems...))
			b.WriteString("\n\n")
		}
		// Line 2: global navigation
		b.WriteString(styles.BackHelp(
			styles.HelpItem("↑ ↓", "navigate"), styles.HelpItem("↵", "run"),
		))
	}
	return b.String()
}

// ── Custom form rendering ─────────────────────────────────────────────────────

func (m *DBOpsModel) viewCustomForm() string {
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
		m.customCmdTextarea.Blur()
		b.WriteString("  " + focusStyle.Render("Name:    ") + m.customNameInput.View() + "\n")
	} else {
		m.customNameInput.Blur()
		b.WriteString("  " + normalStyle.Render("Name:    ") + m.customNameInput.View() + "\n")
	}

	// Command field
	if m.customFormFocus == 1 && m.commandEditMode {
		m.customCmdTextarea.Focus()
		m.customNameInput.Blur()
		b.WriteString("  " + focusStyle.Render("Command: ") + "\n\n")
		ta := m.customCmdTextarea.View()
		taStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(styles.ColorPrimary).
			Padding(0, 1)
		b.WriteString("  " + taStyle.Render(ta) + "\n\n")
	} else if m.customFormFocus == 1 {
		// Preview mode — blurred, muted border
		m.customCmdTextarea.Blur()
		m.customNameInput.Blur()
		b.WriteString("  " + normalStyle.Render("Command: ") + "\n\n")
		ta := m.customCmdTextarea.View()
		taStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(styles.ColorMuted).
			Padding(0, 1)
		b.WriteString("  " + taStyle.Render(ta) + "\n\n")
	} else {
		m.customCmdTextarea.Blur()
		b.WriteString("  " + normalStyle.Render("Command: ") + "\n\n")
		ta := m.customCmdTextarea.View()
		taStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(styles.ColorMuted).
			Padding(0, 1)
		b.WriteString("  " + taStyle.Render(ta) + "\n\n")
	}

	if m.customFormFocus == 1 {
		b.WriteString("  " + styles.InlineHelp(
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("↑ ↓", "field"),
			styles.HelpItem("enter", "newline"),
			styles.HelpItem("ctrl+s", "save"),
		))
	} else {
		// Name field: Enter always submits the form
		b.WriteString("  " + styles.InlineHelp(
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("↑ ↓", "field"),
			styles.HelpItem("↵", "submit"),
		))
	}
	return b.String()
}

// ── Param prompt rendering ────────────────────────────────────────────────────

func (m *DBOpsModel) viewParamPrompt() string {
	var b strings.Builder
	b.WriteString(styles.StyleSubtitle.Render("  Enter parameter values") + "\n\n")

	purpleStyle := lipgloss.NewStyle().Foreground(styles.ColorPurple).Bold(true)
	valStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)

	for i, param := range m.paramsToPrompt {
		if i < m.currentParamIdx {
			// Already filled
			b.WriteString("  " + purpleStyle.Render("{"+param+"}") + " " + valStyle.Render(m.paramValues[param]) + "\n")
		} else if i == m.currentParamIdx {
			// Current
			b.WriteString("  " + purpleStyle.Render("{"+param+"}") + " " + m.paramInput.View() + "\n")
		} else {
			// Upcoming
			b.WriteString("  " + purpleStyle.Render("{"+param+"}") + " \n")
		}
	}

	b.WriteString("\n")
	// Dynamic label: "save" if just storing, "submit" if it triggers the action
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
		styles.HelpItem("enter", enterLabel),
	))
	return b.String()
}

// buildActionsList builds the list of available actions for the given sync-env,
// checking dump file existence at render time.
func (m *DBOpsModel) buildActionsList(se *config.SyncEnv) []actionItem {
	dDir := m.dumpDir(se)
		truncate := m.truncateTables(se)

	var actions []actionItem
	switch se.DBType {
	case "postgres":
		hasSchema := docker.HasDumpFile(dDir, "schema-*.sql")
		hasData := docker.HasDumpFile(dDir, "data-*.sql")
		actions = []actionItem{
			{label: "Export schema", group: "Export", enabled: true, execute: m.exportSchema(se)},
			{label: "Export data", group: "Export", enabled: true, execute: m.exportData(se)},
			{label: "Import schema", group: "Import", enabled: hasSchema, disabledReason: disabledIf(!hasSchema, "no schema dump"), execute: m.importSchema(se)},
			{label: "Import data", group: "Import", enabled: hasData, disabledReason: disabledIf(!hasData, "no data dump"), execute: m.importData(se)},
			{label: "Truncate tables", group: "Functions", enabled: true, execute: truncate},
		}

	case "mongo":
		hasData := m.hasMongoDump(dDir)
		actions = []actionItem{
			{label: "Export", group: "Export", enabled: true, execute: m.exportData(se)},
			{label: "Import", group: "Import", enabled: hasData, disabledReason: disabledIf(!hasData, "no dump"), execute: m.importData(se)},
			{label: "Truncate tables", group: "Functions", enabled: true, execute: truncate},
		}

	case "mysql":
		hasData := docker.HasDumpFile(dDir, "data-*.sql")
		actions = []actionItem{
			{label: "Export", group: "Export", enabled: true, execute: m.exportData(se)},
			{label: "Import", group: "Import", enabled: hasData, disabledReason: disabledIf(!hasData, "no dump file"), execute: m.importData(se)},
			{label: "Truncate tables", group: "Functions", enabled: true, execute: truncate},
		}

	case "mssql":
		hasData := docker.HasDumpFile(dDir, "data-*.bacpac")
		actions = []actionItem{
			{label: "Export", group: "Export", enabled: true, execute: m.exportData(se)},
			{label: "Import", group: "Import", enabled: hasData, disabledReason: disabledIf(!hasData, "no .bacpac file"), execute: m.importData(se)},
			{label: "Truncate tables", group: "Functions", enabled: true, execute: truncate},
		}

	case "oracle":
		actions = []actionItem{
			{label: "Truncate tables", group: "Functions", enabled: true, execute: truncate},
		}
	}

	// Append custom actions below Truncate tables in Functions group
	for idx := range se.CustomActions {
		i := idx
		actions = append(actions, actionItem{
			label:    se.CustomActions[i].Name,
			group:    "Functions",
			enabled:  true,
			isCustom: true,
			customIdx: i,
			execute:  nil,
		})
	}

	// "+ Add function" entry
	actions = append(actions, actionItem{
		label:       "+ Add function",
		group:       "Functions",
		enabled:     true,
		isAddAction: true,
		execute:     nil,
	})

	return actions
}

// ── Export Cmd factories ──────────────────────────────────────────────────────

func (m *DBOpsModel) exportSchema(se *config.SyncEnv) tea.Cmd {
	return func() tea.Msg {
		dDir := m.dumpDir(se)
		if err := os.MkdirAll(dDir, 0755); err != nil {
			return dbOpsDoneMsg{err: fmt.Errorf("create dump dir: %w", err)}
		}
		outPath := filepath.Join(dDir, "schema-"+ts()+".sql")

		// Only postgres supports schema-only export
		if err := docker.PgDump(se, outPath, "-s"); err != nil {
			return dbOpsDoneMsg{err: fmt.Errorf("export schema: %w", err)}
		}
		return dbOpsDoneMsg{result: "Schema exported to " + filepath.Base(outPath)}
	}
}

func (m *DBOpsModel) exportData(se *config.SyncEnv) tea.Cmd {
	return func() tea.Msg {
		dDir := m.dumpDir(se)
		if err := os.MkdirAll(dDir, 0755); err != nil {
			return dbOpsDoneMsg{err: fmt.Errorf("create dump dir: %w", err)}
		}

		switch se.DBType {
		case "postgres":
			outPath := filepath.Join(dDir, "data-"+ts()+".sql")
			if err := docker.PgDump(se, outPath, "-a"); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("export data: %w", err)}
			}
			return dbOpsDoneMsg{result: "Data exported to " + filepath.Base(outPath)}

		case "mongo":
			outDir := filepath.Join(dDir, ts())
			if err := docker.MongoDump(se, outDir); err != nil {
				os.RemoveAll(outDir) // clean up empty dir created by failed mount
				return dbOpsDoneMsg{err: fmt.Errorf("export mongo: %w", err)}
			}
			return dbOpsDoneMsg{result: "Data exported to " + filepath.Base(outDir)}

		case "mysql":
			outPath := filepath.Join(dDir, "data-"+ts()+".sql")
			if err := docker.MySqlDump(se, outPath); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("export mysql: %w", err)}
			}
			return dbOpsDoneMsg{result: "Data exported to " + filepath.Base(outPath)}

		case "mssql":
			outPath := filepath.Join(dDir, "data-"+ts()+".bacpac")
			if err := docker.SqlPackageExport(se, outPath); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("export mssql: %w", err)}
			}
			return dbOpsDoneMsg{result: "Data exported to " + filepath.Base(outPath)}

		case "oracle":
			return dbOpsDoneMsg{err: fmt.Errorf("Oracle export not yet implemented. Set sync_env.tool_image and use Data Pump manually.")}
		default:
			return dbOpsDoneMsg{err: fmt.Errorf("unknown db type: %s", se.DBType)}
		}
	}
}

func (m *DBOpsModel) importSchema(se *config.SyncEnv) tea.Cmd {
	return func() tea.Msg {
		dDir := m.dumpDir(se)
		latest, err := docker.FindLatestDump(dDir, "schema-*.sql")
		if err != nil {
			return dbOpsDoneMsg{err: fmt.Errorf("no schema dump: %w", err)}
		}

		container := docker.ContainerName(m.profile.Name, m.envName, m.serviceName)
		if !m.isContainerRunning(container) {
			return dbOpsDoneMsg{err: fmt.Errorf("service '%s' is not running. Start it first.", m.serviceName)}
		}

		if err := docker.ExecPgRestore(m.profile.Name, m.envName, m.serviceName, latest); err != nil {
			return dbOpsDoneMsg{err: fmt.Errorf("import schema: %w", err)}
		}
		return dbOpsDoneMsg{result: "Schema imported from " + filepath.Base(latest)}
	}
}

func (m *DBOpsModel) importData(se *config.SyncEnv) tea.Cmd {
	return func() tea.Msg {
		dDir := m.dumpDir(se)
		container := docker.ContainerName(m.profile.Name, m.envName, m.serviceName)

		if !m.isContainerRunning(container) {
			return dbOpsDoneMsg{err: fmt.Errorf("service '%s' is not running. Start it first.", m.serviceName)}
		}

		switch se.DBType {
		case "postgres":
			latest, err := docker.FindLatestDump(dDir, "data-*.sql")
			if err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("no data dump: %w", err)}
			}
			if err := docker.ExecPgRestore(m.profile.Name, m.envName, m.serviceName, latest); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("import postgres: %w", err)}
			}
			return dbOpsDoneMsg{result: "Data imported from " + filepath.Base(latest)}

		case "mongo":
			latest, err := docker.FindLatestDump(dDir, "")
			if err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("no mongo dump: %w", err)}
			}
			if err := docker.ExecMongoRestore(m.profile.Name, m.envName, m.serviceName, latest); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("import mongo: %w", err)}
			}
			return dbOpsDoneMsg{result: "Data imported from " + filepath.Base(latest)}

		case "mysql":
			latest, err := docker.FindLatestDump(dDir, "data-*.sql")
			if err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("no data dump: %w", err)}
			}
			if err := docker.ExecMySqlRestore(m.profile.Name, m.envName, m.serviceName, latest); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("import mysql: %w", err)}
			}
			return dbOpsDoneMsg{result: "Data imported from " + filepath.Base(latest)}

		case "mssql":
			latest, err := docker.FindLatestDump(dDir, "data-*.bacpac")
			if err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("no .bacpac file: %w", err)}
			}
			// MSSQL import targets localhost with the container's mapped port
			hostPort, err := m.fetchMSSQLPort()
			if err != nil {
				return dbOpsDoneMsg{err: err}
			}
			if err := docker.SqlPackageImport(m.profile.Name, m.envName, m.serviceName, latest, hostPort, docker.ToolImage(se)); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("import mssql: %w", err)}
			}
			return dbOpsDoneMsg{result: "Data imported from " + filepath.Base(latest)}

		case "oracle":
			return dbOpsDoneMsg{err: fmt.Errorf("Oracle import not yet implemented. Set sync_env.tool_image and use Data Pump manually.")}
		default:
			return dbOpsDoneMsg{err: fmt.Errorf("unknown db type: %s", se.DBType)}
		}
	}
}

// ── Truncate Cmd factory ─────────────────────────────────────────────────────

func (m *DBOpsModel) truncateTables(se *config.SyncEnv) tea.Cmd {
	return func() tea.Msg {
		container := docker.ContainerName(m.profile.Name, m.envName, m.serviceName)
		if !m.isContainerRunning(container) {
			return dbOpsDoneMsg{err: fmt.Errorf("service '%s' is not running. Start it first.", m.serviceName)}
		}

		switch se.DBType {
		case "postgres":
			if err := docker.PgTruncate(m.profile.Name, m.envName, m.serviceName); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("truncate postgres: %w", err)}
			}
		case "mongo":
			if err := docker.MongoTruncate(m.profile.Name, m.envName, m.serviceName); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("truncate mongo: %w", err)}
			}
		case "mysql":
			if err := docker.MySqlTruncate(m.profile.Name, m.envName, m.serviceName); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("truncate mysql: %w", err)}
			}
		case "mssql":
			if err := docker.MsSqlTruncate(m.profile.Name, m.envName, m.serviceName, se.Database); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("truncate mssql: %w", err)}
			}
		case "oracle":
			if err := docker.OracleTruncate(m.profile.Name, m.envName, m.serviceName); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("truncate oracle: %w", err)}
			}
		default:
			return dbOpsDoneMsg{err: fmt.Errorf("unknown db type: %s", se.DBType)}
		}
		return dbOpsDoneMsg{result: "All tables truncated"}
	}
}

// isContainerRunning checks if the given container is running via docker status.
// launchDockerCmd tries to start the Docker daemon via Colima or Docker Desktop.
func (m *DBOpsModel) launchDockerCmd() tea.Cmd {
	return opsLaunchDockerCmd()
}

func (m *DBOpsModel) isContainerRunning(container string) bool {
	return opsIsContainerRunning(m.profile.Name, m.envName, container)
}

// fetchMSSQLPort reads the MSSQL container's mapped port from docker inspect.
// Returns "1433" as fallback if detection fails.
func (m *DBOpsModel) fetchMSSQLPort() (string, error) {
	container := docker.ContainerName(m.profile.Name, m.envName, m.serviceName)
	out, err := exec.Command("docker", "inspect", container,
		"--format", "{{(index (index .NetworkSettings.Ports \"1433/tcp\") 0).HostPort}}").Output()
	if err != nil {
		return "1433", nil // fallback to default
	}
	port := strings.TrimSpace(string(out))
	if port == "" {
		return "1433", nil
	}
	return port, nil
}

// ── Task 4.4: Wizard dispatch ─────────────────────────────────────────────────

func (m *DBOpsModel) updateWizard(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch wm := msg.(type) {
	case wizardSavedMsg:
		// Migrate dumps if sync env was renamed
		if wm.editMode && wm.env.Name != wm.origName {
			oldDir := filepath.Join(config.ProfileDir(m.profile.Name), m.envName, "dumps", m.serviceName, wm.origName)
			newDir := filepath.Join(config.ProfileDir(m.profile.Name), m.envName, "dumps", m.serviceName, wm.env.Name)
			if info, err := os.Stat(oldDir); err == nil && info.IsDir() {
				if err := os.Rename(oldDir, newDir); err != nil {
					m.lastResult = formatErrorMsg("rename dump dir: " + err.Error())
				}
			}
		}
		m.wizardModel = nil
		m.screen = dbScreenSyncEnvList
		return m, nil
	}

	if m.wizardModel == nil {
		m.screen = dbScreenSyncEnvList
		return m, nil
	}

	wm, cmd := m.wizardModel.Update(msg)
	if wm == nil {
		// Wizard cancelled (esc from name step)
		m.wizardModel = nil
		m.screen = dbScreenSyncEnvList
		return m, nil
	}
	m.wizardModel = wm
	return m, cmd
}

func (m *DBOpsModel) viewWizard() string {
	if m.wizardModel == nil {
		return ""
	}
	return m.wizardModel.View()
}

// ── View dispatch ─────────────────────────────────────────────────────────────

func (m *DBOpsModel) View() string {
	switch m.screen {
	case dbScreenSyncEnvActions:
		return m.viewActions()
	case dbScreenWizard:
		return m.viewWizard()
	default:
		return m.viewSyncEnvList()
	}
}

// ── Dump detection helpers ────────────────────────────────────────────────────

// hasDumpsFor checks whether any dump files exist for the given sync-env.
func (m *DBOpsModel) hasDumpsFor(se *config.SyncEnv) bool {
	dDir := m.dumpDir(se)
	switch se.DBType {
	case "postgres":
		return docker.HasAnyDump(dDir, []string{"schema-*.sql", "data-*.sql"})
	case "mongo":
		return m.hasMongoDump(dDir)
	case "mysql":
		return docker.HasDumpFile(dDir, "data-*.sql")
	case "mssql":
		return docker.HasDumpFile(dDir, "data-*.bacpac")
	}
	return false
}

// hasMongoDump checks for any subdirectories in the dump dir (mongodump creates timestamped dirs).
func (m *DBOpsModel) hasMongoDump(dumpDir string) bool {
	entries, err := os.ReadDir(dumpDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			return true
		}
	}
	return false
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// dbOpsWindow calculates the visible item range for a cursor-based list.
func dbOpsWindow(total, cursor, avail int) itemWindow {
	if total == 0 {
		return itemWindow{}
	}
	if total <= avail {
		return itemWindow{start: 0, end: total}
	}
	half := avail / 2
	start := cursor - half
	if start < 0 {
		start = 0
	}
	end := start + avail
	if end > total {
		end = total
		start = total - avail
	}
	return itemWindow{
		start:    start,
		end:      end,
		hasAbove: start > 0,
		hasBelow: end < total,
	}
}

func (m *DBOpsModel) renderSpinnerDot(op string) string {
	return opsRenderSpinnerDot(m.spinnerFrame, op)
}

func ts() string {
	return time.Now().Format("20060102-150405")
}

// disabledIf, wordWrap, and formatErrorMsg are defined in ops_common.go.

// firstEnabledAction returns the index of the first enabled action, or 0 if all are disabled.
func (m *DBOpsModel) firstEnabledAction() int {
	return opsFirstEnabledAction(m.actions)
}

// hasImportPrefix returns true when label matches any "Import" action.
func hasImportPrefix(label string) bool {
	return strings.HasPrefix(label, "Import")
}

// getLocalDBCredentials extracts local database credentials from the service's
// environment variables (POSTGRES_USER, MYSQL_ROOT_PASSWORD, etc.).
func getLocalDBCredentials(svc *config.Service, dbType string) (user, pass, db string) {
	envMap := make(map[string]string)
	for _, ev := range svc.EnvVars {
		envMap[ev.Key] = ev.Value
	}

	switch dbType {
	case "postgres":
		user = envMap["POSTGRES_USER"]
		if user == "" {
			user = "postgres"
		}
		pass = envMap["POSTGRES_PASSWORD"]
		db = envMap["POSTGRES_DB"]
		if db == "" {
			db = "postgres"
		}
	case "mongo":
		user = envMap["MONGO_INITDB_ROOT_USERNAME"]
		if user == "" {
			user = "admin"
		}
		pass = envMap["MONGO_INITDB_ROOT_PASSWORD"]
		db = envMap["MONGO_INITDB_DATABASE"]
		if db == "" {
			db = "admin"
		}
	case "mysql", "mariadb":
		user = "root"
		pass = envMap["MYSQL_ROOT_PASSWORD"]
		db = envMap["MYSQL_DATABASE"]
		if db == "" {
			db = "mysql"
		}
	case "mssql":
		user = "sa"
		pass = envMap["SA_PASSWORD"] // or MSSQL_SA_PASSWORD
		db = envMap["MSSQL_DB"]
		if db == "" {
			db = "master"
		}
	case "oracle":
		user = envMap["ORACLE_USER"]
		if user == "" {
			user = "system"
		}
		pass = envMap["ORACLE_PASSWORD"]
		if pass == "" {
			pass = "oracle"
		}
		db = envMap["ORACLE_PDB"]
		if db == "" {
			db = "FREEPDB1"
		}
	}
	return
}
