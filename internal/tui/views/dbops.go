package views

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
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

// serviceStartedMsg is sent when docker.UpService completes for the prompt flow.
type serviceStartedMsg struct {
	err error
}

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
	truncatedSinceData bool         // disable truncate until next import
	lastActionLabel    string       // label of action being executed

	// Prompt state (start service before import/truncate)
	promptStartService bool
	startingService    bool
	pendingAction      string // action label to execute after service starts

	// Running state (shared across screens)
	running    bool
	lastResult string

	// Spinner
	spinnerFrame int

	// Viewport
	height int

	// Wizard (delegated)
	wizardModel *AddSyncEnvModel

	// Custom action form
	addingCustomAction bool
	editingCustomIdx   int    // -1 = adding new, >= 0 = editing index
	customFormFocus    int    // 0=name, 1=command
	customFormName     string
	customFormCmd      string

	// Custom action param prompting
	promptingParams   bool
	paramsToPrompt    []string
	paramValues       map[string]string
	currentParamIdx   int
	currentParamInput string

	// Custom action delete
	deleteActionArmed bool

	// Confirm before execute (double confirmation)
	confirmAction       bool
	confirmCustomAction bool
	pendingCustomCmd   string
}

// actionItem describes a single action in the actions screen.
type actionItem struct {
	label          string
	group          string // panel group label: "Export", "Import", "Functions"
	enabled        bool
	disabledReason string
	execute        func() tea.Cmd
	isCustom       bool
	isAddAction    bool
	customIdx      int // index into se.CustomActions (valid when isCustom)
}

// ── Key bindings ──────────────────────────────────────────────────────────────

var (
	dbOpsUp     = key.NewBinding(key.WithKeys("up", "k"))
	dbOpsDown   = key.NewBinding(key.WithKeys("down", "j"))
	dbOpsEnter  = key.NewBinding(key.WithKeys("enter"))
	dbOpsBack   = key.NewBinding(key.WithKeys("esc"))
	dbOpsAdd    = key.NewBinding(key.WithKeys("a"))
	dbOpsEdit   = key.NewBinding(key.WithKeys("e"))
	dbOpsDelete = key.NewBinding(key.WithKeys("d"))
)

// ── Constructor ───────────────────────────────────────────────────────────────

func NewDBOps(cfg *config.Config, p *config.Profile, envName, serviceName string) *DBOpsModel {
	m := &DBOpsModel{
		cfg:         cfg,
		profile:     p,
		envName:     envName,
		serviceName: serviceName,
		screen:      dbScreenSyncEnvList,
	}
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

// ── Environment / Service helpers ─────────────────────────────────────────────

func (m *DBOpsModel) currentEnv() *config.Environment {
	for i := range m.profile.Environments {
		if m.profile.Environments[i].Name == m.envName {
			return &m.profile.Environments[i]
		}
	}
	return nil
}

func (m *DBOpsModel) currentService() *config.Service {
	env := m.currentEnv()
	if env == nil {
		return nil
	}
	if m.serviceIdx >= 0 && m.serviceIdx < len(env.Services) {
		svc := &env.Services[m.serviceIdx]
		if svc.Name == m.serviceName || svc.ComposeName() == m.serviceName {
			return svc
		}
	}
	for i := range env.Services {
		svc := &env.Services[i]
		if svc.Name == m.serviceName || svc.ComposeName() == m.serviceName {
			m.serviceIdx = i
			return svc
		}
	}
	return nil
}

func (m *DBOpsModel) dumpDir(se *config.SyncEnv) string {
	return se.DumpDir(m.profile.Name, m.envName, m.serviceName)
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m *DBOpsModel) Init() tea.Cmd {
	return nil
}

func (m *DBOpsModel) SetSize(w, h int) {
	m.height = max(0, h)
}

// ── Update dispatch ───────────────────────────────────────────────────────────

func (m *DBOpsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(spinnerTickMsg); ok {
		if m.running {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return spinnerTickMsg{}
			})
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
		case key.Matches(msg, dbOpsBack):
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

		case key.Matches(msg, dbOpsUp) && m.listCursor > 0 && !m.deleteArmed:
			m.listCursor--

		case key.Matches(msg, dbOpsDown) && m.listCursor < len(syncEnvs)-1 && !m.deleteArmed:
			m.listCursor++

		case key.Matches(msg, dbOpsEnter):
			if len(syncEnvs) > 0 {
				se := &syncEnvs[m.listCursor]
				m.selectedEnv = se.Name
				m.actions = m.buildActionsList(se)
				m.actionCursor = m.firstEnabledAction()
				m.screen = dbScreenSyncEnvActions
			}

		case key.Matches(msg, dbOpsAdd):
			m.wizardModel = NewAddSyncEnv(m.cfg, m.profile, m.envName, m.serviceName, true, nil)
			m.screen = dbScreenWizard

		case key.Matches(msg, dbOpsEdit):
			if len(syncEnvs) > 0 {
				se := &syncEnvs[m.listCursor]
				m.wizardModel = NewAddSyncEnv(m.cfg, m.profile, m.envName, m.serviceName, false, se)
				m.screen = dbScreenWizard
			}

		case key.Matches(msg, dbOpsDelete):
			if len(syncEnvs) == 0 {
				break
			}
			if !m.deleteArmed {
				m.deleteArmed = true
			} else {
				// Second d: confirm delete
				m.deleteArmed = false
				name := syncEnvs[m.listCursor].Name
				svc.SyncEnvs = append(syncEnvs[:m.listCursor], syncEnvs[m.listCursor+1:]...)
				m.profile.UpdatedAt = time.Now()
				if err := m.cfg.UpdateProfile(*m.profile); err != nil {
					m.lastResult = formatErrorMsg(err.Error())
				} else {
					m.lastResult = styles.StyleSuccess.Render("✓ Deleted " + name)
					if m.listCursor >= len(svc.SyncEnvs) && m.listCursor > 0 {
						m.listCursor--
					}
					return m, ClearActionAfter(3 * time.Second)
				}
			}

		default:
			// Any other key disarms delete
			if m.deleteArmed {
				m.deleteArmed = false
			}
		}

	case ClearActionMsg:
		m.lastResult = ""
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
		for i, se := range syncEnvs {
			prefix := "    "
			if i == m.listCursor {
				prefix = "  > "
			}
			dumpIndicator := ""
			if m.hasDumpsFor(&se) {
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
	}

	if m.deleteArmed {
		b.WriteString("\n" + styles.StyleError.Render("  Press d again to confirm removal"))
		b.WriteString("\n")
	}

	if m.lastResult != "" {
		b.WriteString("\n  " + m.lastResult + "\n")
	}

	b.WriteString("\n")
	var actItems []styles.HelpItemResult
	actItems = append(actItems, styles.HelpItem("a", "add"))
	svc = m.currentService()
	if svc != nil && len(svc.SyncEnvs) > 0 {
		actItems = append(actItems, styles.HelpItem("e", "edit"))
		actItems = append(actItems, styles.HelpItem("d d", "delete"))
	}
	b.WriteString(styles.HelpBar(
		styles.HelpGroup{Label: "Manage", Items: actItems},
	))
	b.WriteString("\n\n")
	if len(syncEnvs) > 0 {
		b.WriteString(styles.BackHelp(styles.HelpItem("↑ ↓", "navigate"), styles.HelpItem("↵", "open actions")))
	} else {
		b.WriteString(styles.BackHelp())
	}
	return b.String()
}

// ── Task 4.3: SyncEnv Actions screen ─────────────────────────────────────────

func (m *DBOpsModel) updateActions(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle start-service prompt inline
	if m.promptStartService {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "y", "Y":
				m.promptStartService = false
				m.startingService = true
				svc := m.currentService()
				if svc == nil {
					m.promptStartService = false
					m.startingService = false
					m.pendingAction = ""
					m.lastResult = styles.StyleError.Render("Service not found.")
					return m, ClearActionAfter(3 * time.Second)
				}
				return m, func() tea.Msg {
					err := docker.UpService(m.profile.Name, m.envName, svc.ComposeName())
					return serviceStartedMsg{err: err}
				}
			case "n", "N", "esc":
				m.promptStartService = false
				m.pendingAction = ""
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
		case key.Matches(msg, dbOpsBack):
			if m.running || m.startingService {
				return m, nil
			}
			m.screen = dbScreenSyncEnvList
			m.selectedEnv = ""
			m.actions = nil

		case key.Matches(msg, dbOpsUp) && !m.running && !m.promptStartService && !m.startingService:
			for i := m.actionCursor - 1; i >= 0; i-- {
				if m.actions[i].enabled {
					m.actionCursor = i
					break
				}
			}

		case key.Matches(msg, dbOpsDown) && !m.running && !m.promptStartService && !m.startingService:
			for i := m.actionCursor + 1; i < len(m.actions); i++ {
				if m.actions[i].enabled {
					m.actionCursor = i
					break
				}
			}

		case key.Matches(msg, dbOpsEnter) && !m.running && !m.promptStartService && !m.startingService:
			if m.actionCursor < len(m.actions) {
				act := m.actions[m.actionCursor]
				if act.enabled {
					if act.isAddAction {
						m.addingCustomAction = true
						m.editingCustomIdx = -1
						m.customFormFocus = 0
						m.customFormName = ""
						m.customFormCmd = ""
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
							m.currentParamInput = ""
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
					return m, ClearActionAfter(3 * time.Second)
				}
			}

		case key.Matches(msg, dbOpsEdit) && !m.running && !m.promptStartService && !m.startingService:
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
					m.customFormName = se.CustomActions[act.customIdx].Name
					m.customFormCmd = se.CustomActions[act.customIdx].Command
				}
			}

		case key.Matches(msg, dbOpsDelete) && !m.running && !m.promptStartService && !m.startingService:
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
						} else {
							m.lastResult = styles.StyleSuccess.Render("✓ Deleted custom action")
						}
						m.actions = m.buildActionsList(se)
						if m.actionCursor >= len(m.actions) {
							m.actionCursor = len(m.actions) - 1
						}
						return m, ClearActionAfter(3 * time.Second)
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
		m.running = false
		if msg.err != nil {
			m.lastResult = formatErrorMsg(msg.err.Error())
		} else {
			m.lastResult = styles.StyleSuccess.Render("\u2713 " + msg.result)
			// Re-scan dump states — imports/exports affect dump availability
			svc := m.currentService()
			if svc != nil {
				if se := svc.FindSyncEnv(m.selectedEnv); se != nil {
					m.actions = m.buildActionsList(se)
					m.actionCursor = m.firstEnabledAction()
				}
			}
			// Track truncate vs import to toggle truncate availability
			if m.lastActionLabel == "Truncate tables" {
				m.truncatedSinceData = true
			} else if hasImportPrefix(m.lastActionLabel) {
				m.truncatedSinceData = false
			}
			return m, ClearActionAfter(3 * time.Second)
		}

	case ClearActionMsg:
		m.lastResult = ""

	case serviceStartedMsg:
		m.startingService = false
		if msg.err != nil {
			m.lastResult = formatErrorMsg("start service: " + msg.err.Error())
			return m, ClearActionAfter(3 * time.Second)
		}
		// Execute the pending action
		m.running = true
		m.lastResult = ""
		m.lastActionLabel = m.pendingAction
		m.spinnerFrame = 0
		svc := m.currentService()
		if svc != nil {
			if se := svc.FindSyncEnv(m.selectedEnv); se != nil {
				m.actions = m.buildActionsList(se)
				for _, act := range m.actions {
					if act.label == m.pendingAction && act.enabled {
						return m, tea.Batch(act.execute(), spinnerCmd())
					}
				}
			}
		}
		m.running = false
		m.lastResult = styles.StyleError.Render("Could not find action '" + m.pendingAction + "'")
		return m, ClearActionAfter(3 * time.Second)
	}
	return m, nil
}

// ── Param prompt update ───────────────────────────────────────────────────────

func (m *DBOpsModel) updateParamPrompt(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, dbOpsBack):
			m.promptingParams = false
			return m, nil
		case key.Matches(msg, dbOpsEnter):
			if m.currentParamInput != "" {
				paramName := m.paramsToPrompt[m.currentParamIdx]
				m.paramValues[paramName] = m.currentParamInput
				m.currentParamIdx++
				m.currentParamInput = ""
				if m.currentParamIdx >= len(m.paramsToPrompt) {
					m.promptingParams = false
					m.confirmCustomAction = true
				}
			}
		default:
			switch msg.String() {
			case "backspace":
				if len(m.currentParamInput) > 0 {
					m.currentParamInput = m.currentParamInput[:len(m.currentParamInput)-1]
				}
			default:
				if len(msg.String()) == 1 {
					m.currentParamInput += msg.String()
				}
			}
		}
	}
	return m, nil
}

// ── Custom confirm update ─────────────────────────────────────────────────────

func (m *DBOpsModel) updateCustomConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, dbOpsEnter):
			m.confirmCustomAction = false
			m.running = true
			m.lastResult = ""
			m.lastActionLabel = "Custom action"
			return m, tea.Batch(m.customActionExecCmd(m.pendingCustomCmd), spinnerCmd())
		case key.Matches(msg, dbOpsBack):
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
		case key.Matches(msg, dbOpsEnter):
			m.confirmAction = false
			act := m.actions[m.actionCursor]
			if act.group == "Import" || strings.Contains(act.label, "Truncate") {
				container := docker.ContainerName(m.profile.Name, m.envName, m.serviceName)
				if !m.isContainerRunning(container) {
					m.promptStartService = true
					m.pendingAction = act.label
					return m, nil
				}
			}
			m.running = true
			m.lastResult = ""
			m.lastActionLabel = act.label
			return m, tea.Batch(act.execute(), spinnerCmd())
		default:
			m.confirmAction = false
		}
	}
	return m, nil
}

// ── Custom form update ────────────────────────────────────────────────────────

func (m *DBOpsModel) updateCustomForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Ctrl+V: paste from clipboard
		if msg.Type == tea.KeyCtrlV {
			paste, err := exec.Command("pbpaste").Output()
			if err == nil {
				pasted := strings.TrimSpace(string(paste))
				if m.customFormFocus == 0 {
					m.customFormName += pasted
				} else {
					m.customFormCmd += pasted
				}
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, dbOpsBack):
			m.addingCustomAction = false

		case key.Matches(msg, dbOpsUp), msg.String() == "tab":
			if m.customFormFocus > 0 {
				m.customFormFocus--
			}

		case key.Matches(msg, dbOpsDown):
			if m.customFormFocus < 1 {
				m.customFormFocus++
			}

		case key.Matches(msg, dbOpsEnter):
			// Save only when on Command field AND both fields non-empty
			if m.customFormFocus == 1 && m.customFormName != "" && m.customFormCmd != "" {
				svc := m.currentService()
				if svc == nil {
					return m, nil
				}
				se := svc.FindSyncEnv(m.selectedEnv)
				if se == nil {
					return m, nil
				}

				if m.editingCustomIdx >= 0 && m.editingCustomIdx < len(se.CustomActions) {
					se.CustomActions[m.editingCustomIdx].Name = m.customFormName
					se.CustomActions[m.editingCustomIdx].Command = m.customFormCmd
				} else {
					se.CustomActions = append(se.CustomActions, config.CustomAction{
						Name:    m.customFormName,
						Command: m.customFormCmd,
					})
				}
				m.profile.UpdatedAt = time.Now()
				if err := m.cfg.UpdateProfile(*m.profile); err != nil {
					m.lastResult = formatErrorMsg(err.Error())
				} else {
					m.lastResult = styles.StyleSuccess.Render("✓ Custom action saved")
				}
				m.addingCustomAction = false
				m.actions = m.buildActionsList(se)
				if m.actionCursor >= len(m.actions) {
					m.actionCursor = len(m.actions) - 1
				}
				return m, ClearActionAfter(3 * time.Second)
			}

		default:
			switch msg.String() {
			case "backspace":
				if m.customFormFocus == 0 && len(m.customFormName) > 0 {
					m.customFormName = m.customFormName[:len(m.customFormName)-1]
				} else if m.customFormFocus == 1 && len(m.customFormCmd) > 0 {
					m.customFormCmd = m.customFormCmd[:len(m.customFormCmd)-1]
				}
			default:
				if len(msg.String()) == 1 {
					ch := msg.String()
					if m.customFormFocus == 0 {
						m.customFormName += ch
					} else {
						m.customFormCmd += ch
					}
				}
			}
		}
	}
	return m, nil
}

// ── Custom action execution ───────────────────────────────────────────────────

func (m *DBOpsModel) customActionExecCmd(cmd string) tea.Cmd {
	return func() tea.Msg {
		substituted := cmd
		for k, v := range m.paramValues {
			substituted = strings.ReplaceAll(substituted, "{"+k+"}", v)
		}
		out, err := exec.Command("sh", "-c", substituted).CombinedOutput()
		if err != nil {
			return dbOpsDoneMsg{err: fmt.Errorf("custom action: %s: %w", strings.TrimSpace(string(out)), err)}
		}
		if len(out) > 0 {
			return dbOpsDoneMsg{result: "Custom action completed: " + strings.TrimSpace(string(out))}
		}
		return dbOpsDoneMsg{result: "Custom action completed"}
	}
}

// ── Param extraction ──────────────────────────────────────────────────────────

var paramRe = regexp.MustCompile(`\{(\w+)\}`)

func extractParams(cmd string) []string {
	matches := paramRe.FindAllStringSubmatch(cmd, -1)
	var params []string
	seen := map[string]bool{}
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			params = append(params, m[1])
		}
	}
	return params
}

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

	// Prompt: start service before import/truncate
	if m.promptStartService {
		promptStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)
		b.WriteString(styles.StyleSubtitle.Render("  Service '" + m.serviceName + "' is not running.") + "\n")
		b.WriteString("  " + promptStyle.Render("Start it?") + "  " + styles.StyleSubtitle.Render("(y/n)") + "\n\n")
	}

	if m.startingService {
		b.WriteString(styles.StyleSubtitle.Render("  Starting " + m.serviceName + "\u2026") + "\n\n")
	}

	// ── Custom action form ────────────────────────────────────────────────────
	if m.addingCustomAction {
		b.WriteString(m.viewCustomForm())
		b.WriteString("\n")
		return b.String()
	}

	// ── Param prompting ───────────────────────────────────────────────────────
	if m.promptingParams {
		b.WriteString(m.viewParamPrompt())
		b.WriteString("\n")
		return b.String()
	}

	// ── Confirm messages ──────────────────────────────────────────────────────
	if m.confirmCustomAction {
		b.WriteString(styles.StyleError.Render("  Press enter to execute custom action, esc to cancel") + "\n\n")
	}
	if m.confirmAction {
		b.WriteString(styles.StyleError.Render("  Press enter again to confirm, any other key to cancel") + "\n\n")
	}

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

	currentGroup := ""
	for i, act := range m.actions {
		if act.group != currentGroup {
			currentGroup = act.group
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString("  " + groupHeaderStyle.Render(currentGroup) + "\n")
		}

		isCursor := i == m.actionCursor

		if !act.enabled {
			b.WriteString(styles.StyleSubtitle.Render("      " + act.label) + "\n")
			if act.disabledReason != "" {
				b.WriteString(styles.StyleSubtitle.Render("      unavailable: " + act.disabledReason) + "\n")
			}
			continue
		}

		if m.running || m.startingService {
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

	b.WriteString("\n")

	if m.running {
		spinner := spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
		b.WriteString("  " + spinner + " Running " + m.lastActionLabel + "\n\n")
	}

	if m.lastResult != "" {
		b.WriteString("  " + m.lastResult + "\n\n")
	}

	if m.deleteActionArmed {
		b.WriteString(styles.StyleError.Render("  Press d again to delete this custom action") + "\n")
	}

	if m.running || m.promptStartService || m.startingService {
		b.WriteString(styles.StyleHelp.Render("  " + "Please wait…"))
	} else if m.confirmAction || m.confirmCustomAction {
		b.WriteString(styles.BackHelp())
	} else {
		var helpItems []styles.HelpItemResult
		helpItems = append(helpItems, styles.HelpItem("↑ ↓", "navigate"), styles.HelpItem("↵", "run"))
		if m.actionCursor < len(m.actions) && m.actions[m.actionCursor].isCustom {
			helpItems = append(helpItems, styles.HelpItem("e", "edit"), styles.HelpItem("d d", "delete"))
		}
		b.WriteString(styles.BackHelp(helpItems...))
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
	normalStyle := lipgloss.NewStyle().Foreground(styles.ColorFg)

	// Name field
	if m.customFormFocus == 0 {
		b.WriteString("  " + focusStyle.Render("Name: ") + focusStyle.Render(m.customFormName+"▌") + "\n")
	} else {
		b.WriteString("  " + normalStyle.Render("Name: ") + normalStyle.Render(m.customFormName) + "\n")
	}

	// Command field
	if m.customFormFocus == 1 {
		b.WriteString("  " + focusStyle.Render("Command: ") + focusStyle.Render(m.customFormCmd+"▌") + "\n\n")
	} else {
		b.WriteString("  " + normalStyle.Render("Command: ") + normalStyle.Render(m.customFormCmd) + "\n\n")
	}

		b.WriteString("  " + styles.InlineHelp(
			styles.HelpItem("↑ ↓", "field"),
			styles.HelpItem("enter", "save"),
			styles.HelpItem("esc", "cancel"),
		) + "\n\n")
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
			b.WriteString("  " + purpleStyle.Render("{"+param+"}") + " " + valStyle.Render(m.currentParamInput+"▌") + "\n")
		} else {
			// Upcoming
			b.WriteString("  " + purpleStyle.Render("{"+param+"}") + " \n")
		}
	}

	b.WriteString("\n")
	b.WriteString("  " + styles.InlineHelp(
		styles.HelpItem("enter", "next"),
		styles.HelpItem("esc", "cancel"),
	) + "\n")
	return b.String()
}

// buildActionsList builds the list of available actions for the given sync-env,
// checking dump file existence at render time.
func (m *DBOpsModel) buildActionsList(se *config.SyncEnv) []actionItem {
	dDir := m.dumpDir(se)
	truncate := func() tea.Cmd { return m.truncateTables(se) }

	var actions []actionItem
	switch se.DBType {
	case "postgres":
		pgDumpAvailable := docker.ToolAvailable("pg_dump")
		hasSchema := docker.HasDumpFile(dDir, "schema-*.sql")
		hasData := docker.HasDumpFile(dDir, "data-*.sql")
		actions = []actionItem{
			{label: "Export schema", group: "Export", enabled: pgDumpAvailable, disabledReason: disabledIf(!pgDumpAvailable, "pg_dump not found in PATH"), execute: func() tea.Cmd { return m.exportSchema(se) }},
			{label: "Export data", group: "Export", enabled: pgDumpAvailable, disabledReason: disabledIf(!pgDumpAvailable, "pg_dump not found in PATH"), execute: func() tea.Cmd { return m.exportData(se) }},
			{label: "Import schema", group: "Import", enabled: hasSchema, disabledReason: disabledIf(!hasSchema, "no schema dump"), execute: func() tea.Cmd { return m.importSchema(se) }},
			{label: "Import data", group: "Import", enabled: hasData, disabledReason: disabledIf(!hasData, "no data dump"), execute: func() tea.Cmd { return m.importData(se) }},
			{label: "Truncate tables", group: "Functions", enabled: m.truncateEnabled(hasData), disabledReason: disabledIf(!m.truncateEnabled(hasData), "import data first"), execute: truncate},
		}

	case "mongo":
		mongodumpAvailable := docker.ToolAvailable("mongodump")
		hasData := m.hasMongoDump(dDir)
		actions = []actionItem{
			{label: "Export", group: "Export", enabled: mongodumpAvailable, disabledReason: disabledIf(!mongodumpAvailable, "mongodump not found in PATH"), execute: func() tea.Cmd { return m.exportData(se) }},
			{label: "Import", group: "Import", enabled: hasData, disabledReason: disabledIf(!hasData, "no dump"), execute: func() tea.Cmd { return m.importData(se) }},
			{label: "Truncate tables", group: "Functions", enabled: m.truncateEnabled(hasData), disabledReason: disabledIf(!m.truncateEnabled(hasData), "import data first"), execute: truncate},
		}

	case "mysql":
		mysqldumpAvailable := docker.ToolAvailable("mysqldump")
		hasData := docker.HasDumpFile(dDir, "data-*.sql")
		actions = []actionItem{
			{label: "Export", group: "Export", enabled: mysqldumpAvailable, disabledReason: disabledIf(!mysqldumpAvailable, "mysqldump not found in PATH"), execute: func() tea.Cmd { return m.exportData(se) }},
			{label: "Import", group: "Import", enabled: hasData, disabledReason: disabledIf(!hasData, "no dump file"), execute: func() tea.Cmd { return m.importData(se) }},
			{label: "Truncate tables", group: "Functions", enabled: m.truncateEnabled(hasData), disabledReason: disabledIf(!m.truncateEnabled(hasData), "import data first"), execute: truncate},
		}

	case "mssql":
		sqlpackageAvailable := docker.ToolAvailable("sqlpackage")
		hasData := docker.HasDumpFile(dDir, "data-*.bacpac")
		actions = []actionItem{
			{label: "Export", group: "Export", enabled: sqlpackageAvailable, disabledReason: disabledIf(!sqlpackageAvailable, "sqlpackage not found in PATH"), execute: func() tea.Cmd { return m.exportData(se) }},
			{label: "Import", group: "Import", enabled: hasData && sqlpackageAvailable, disabledReason: disabledIf(!hasData, "no .bacpac file"), execute: func() tea.Cmd { return m.importData(se) }},
			{label: "Truncate tables", group: "Functions", enabled: m.truncateEnabled(hasData), disabledReason: disabledIf(!m.truncateEnabled(hasData), "import data first"), execute: truncate},
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
			if !docker.ToolAvailable("sqlpackage") {
				return dbOpsDoneMsg{err: fmt.Errorf("sqlpackage CLI not found. Install via: dotnet tool install -g microsoft.sqlpackage")}
			}
			outPath := filepath.Join(dDir, "data-"+ts()+".bacpac")
			if err := docker.SqlPackageExport(se, outPath); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("export mssql: %w", err)}
			}
			return dbOpsDoneMsg{result: "Data exported to " + filepath.Base(outPath)}

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
			if !docker.ToolAvailable("sqlpackage") {
				return dbOpsDoneMsg{err: fmt.Errorf("sqlpackage CLI not found. Install via: dotnet tool install -g microsoft.sqlpackage")}
			}
			latest, err := docker.FindLatestDump(dDir, "data-*.bacpac")
			if err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("no .bacpac file: %w", err)}
			}
			// MSSQL import targets localhost with the container's mapped port
			hostPort, err := m.fetchMSSQLPort()
			if err != nil {
				return dbOpsDoneMsg{err: err}
			}
			if err := docker.SqlPackageImport(m.profile.Name, m.envName, m.serviceName, latest, hostPort); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("import mssql: %w", err)}
			}
			return dbOpsDoneMsg{result: "Data imported from " + filepath.Base(latest)}

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
			if err := docker.MsSqlTruncate(m.profile.Name, m.envName, m.serviceName); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("truncate mssql: %w", err)}
			}
		default:
			return dbOpsDoneMsg{err: fmt.Errorf("unknown db type: %s", se.DBType)}
		}
		return dbOpsDoneMsg{result: "All tables truncated"}
	}
}

// isContainerRunning checks if the given container is running via docker status.
func (m *DBOpsModel) isContainerRunning(container string) bool {
	statuses, err := docker.Status(m.profile.Name, m.envName)
	if err != nil {
		return false
	}
	for _, s := range statuses {
		if s.Name == container && s.State == "running" {
			return true
		}
	}
	return false
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
	switch msg := msg.(type) {
	case wizardSavedMsg:
		// Wizard completed successfully
		m.wizardModel = nil
		m.screen = dbScreenSyncEnvList
		if msg.editMode {
			m.lastResult = styles.StyleSuccess.Render("\u2713 Updated " + msg.env.Name)
		} else {
			m.lastResult = styles.StyleSuccess.Render("\u2713 Sync environment '" + msg.env.Name + "' saved")
		}
		return m, ClearActionAfter(3 * time.Second)
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

func ts() string {
	return time.Now().Format("20060102-150405")
}

// truncateEnabled returns true when data dumps exist AND truncate was not
// already performed since the last data import.
func (m *DBOpsModel) truncateEnabled(hasData bool) bool {
	return hasData && !m.truncatedSinceData
}

// disabledIf returns reason when cond is true, or empty string when false.
func disabledIf(cond bool, reason string) string {
	if cond {
		return reason
	}
	return ""
}

// wordWrap splits s into lines of at most width characters,
// breaking at word boundaries. Words longer than width are placed
// on their own line without breaking.
func wordWrap(s string, width int) []string {
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		if len(para) <= width {
			lines = append(lines, para)
			continue
		}
		var current string
		for _, word := range strings.Fields(para) {
			if current == "" {
				current = word
			} else if len(current)+1+len(word) <= width {
				current += " " + word
			} else {
				lines = append(lines, current)
				current = word
			}
		}
		if current != "" {
			lines = append(lines, current)
		}
	}
	return lines
}

// formatErrorMsg formats an error string with a line break between the action
// prefix and the underlying error detail, word-wrapping the detail at ~80 chars.
func formatErrorMsg(err string) string {
	idx := strings.Index(err, ": ")
	if idx == -1 {
		return styles.StyleError.Render("\u2717 " + err)
	}
	prefix := err[:idx]
	detail := err[idx+2:]
	wrapped := wordWrap(detail, 80)
	var b strings.Builder
	b.WriteString("\u2717 " + prefix + ":\n")
	for _, line := range wrapped {
		b.WriteString("  " + line + "\n")
	}
	return styles.StyleError.Render(strings.TrimSuffix(b.String(), "\n"))
}

// firstEnabledAction returns the index of the first enabled action, or 0 if all are disabled.
func (m *DBOpsModel) firstEnabledAction() int {
	for i, act := range m.actions {
		if act.enabled {
			return i
		}
	}
	return 0
}

// hasImportPrefix returns true when label matches any "Import" action.
func hasImportPrefix(label string) bool {
	return strings.HasPrefix(label, "Import")
}
