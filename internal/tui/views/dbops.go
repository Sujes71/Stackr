package views

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stackr/stackr/internal/config"
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
	selectedEnv  string
	actionCursor int
	actions      []actionItem // cached for view rendering

	// Running state (shared across screens)
	running    bool
	lastResult string

	// Wizard (delegated)
	wizardModel *AddSyncEnvModel
}

// actionItem describes a single export/import action in the actions screen.
type actionItem struct {
	label           string
	enabled         bool
	disabledReason  string
	execute         func() tea.Cmd
}

// ── Key bindings ──────────────────────────────────────────────────────────────

var (
	dbOpsUp     = key.NewBinding(key.WithKeys("up", "k"))
	dbOpsDown   = key.NewBinding(key.WithKeys("down", "j"))
	dbOpsEnter  = key.NewBinding(key.WithKeys("enter"))
	dbOpsBack   = key.NewBinding(key.WithKeys("esc"))
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
	if env == nil || m.serviceIdx >= len(env.Services) {
		return nil
	}
	return &env.Services[m.serviceIdx]
}

func (m *DBOpsModel) dumpDir(se *config.SyncEnv) string {
	return se.DumpDir(m.profile.Name, m.envName, m.serviceName)
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m *DBOpsModel) Init() tea.Cmd {
	return nil
}

// ── Update dispatch ───────────────────────────────────────────────────────────

func (m *DBOpsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		svc := m.currentService()
		if svc == nil {
			return m, nil
		}
		syncEnvs := svc.SyncEnvs

		switch {
		case key.Matches(msg, dbOpsBack):
			return m, func() tea.Msg {
				return NavigateTo{Screen: NavProfileView, ProfileName: m.profile.Name}
			}

		case key.Matches(msg, dbOpsUp) && m.listCursor > 0 && !m.deleteArmed:
			m.listCursor--

		case key.Matches(msg, dbOpsDown) && m.listCursor < len(syncEnvs)-1 && !m.deleteArmed:
			m.listCursor++

		case key.Matches(msg, dbOpsEnter):
			if len(syncEnvs) > 0 {
				se := &syncEnvs[m.listCursor]
				m.selectedEnv = se.Name
				m.actionCursor = 0
				m.actions = m.buildActionsList(se)
				m.screen = dbScreenSyncEnvActions
			}

		case key.Matches(msg, dbOpsEdit):
			if len(syncEnvs) > 0 {
				// Edit mode
				se := &syncEnvs[m.listCursor]
				m.wizardModel = NewAddSyncEnv(m.cfg, m.profile, m.envName, m.serviceName, false, se)
			} else {
				// Add mode
				m.wizardModel = NewAddSyncEnv(m.cfg, m.profile, m.envName, m.serviceName, true, nil)
			}
			m.screen = dbScreenWizard

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
					m.lastResult = styles.StyleError.Render("✗ " + err.Error())
				} else {
					m.lastResult = styles.StyleSuccess.Render("✓ Deleted " + name)
					if m.listCursor >= len(svc.SyncEnvs) && m.listCursor > 0 {
						m.listCursor--
					}
				}
				return m, ClearActionAfter(3 * time.Second)
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
		b.WriteString(styles.StyleSubtitle.Render("  Service not found.\n"))
		return b.String()
	}
	syncEnvs := svc.SyncEnvs

	if len(syncEnvs) == 0 {
		b.WriteString(styles.StyleSubtitle.Render("  No sync-from environments configured."))
		b.WriteString("\n")
		b.WriteString(styles.StyleSubtitle.Render("  Press e to add one."))
		b.WriteString("\n")
	} else {
		for i, se := range syncEnvs {
			prefix := "    "
			if i == m.listCursor {
				prefix = "  > "
			}
			dumpIndicator := ""
			if m.hasDumpsFor(&se) {
				dumpIndicator = "  \U0001F4E6"
			}
			line := fmt.Sprintf("%s%s  %s  %s:%s%s",
				prefix, se.Name, se.DBType, se.Host, se.Port, dumpIndicator)
			if i == m.listCursor {
				b.WriteString(styles.StyleListItemSelected.Render(line))
			} else {
				b.WriteString(styles.StyleListItem.Render(line))
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

	b.WriteString("\n\n")
	helpItems := []string{styles.HelpItem("\u2191\u2193", "select")}
	svc = m.currentService()
	if svc != nil && len(svc.SyncEnvs) > 0 {
		helpItems = append(helpItems, styles.HelpItem("\u21B5", "actions"))
		helpItems = append(helpItems, styles.HelpItem("d d", "delete"))
	}
	helpItems = append(helpItems, styles.HelpItem("e", "add/edit"))
	helpItems = append(helpItems, styles.HelpItem("esc", "back"))
	b.WriteString(styles.StyleHelp.Render(strings.Join(helpItems, "   ")))
	return b.String()
}

// ── Task 4.3: SyncEnv Actions screen ─────────────────────────────────────────

func (m *DBOpsModel) updateActions(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, dbOpsBack):
			if m.running {
				return m, nil
			}
			m.screen = dbScreenSyncEnvList
			m.selectedEnv = ""
			m.actions = nil

		case key.Matches(msg, dbOpsUp) && m.actionCursor > 0 && !m.running:
			m.actionCursor--

		case key.Matches(msg, dbOpsDown) && m.actionCursor < len(m.actions)-1 && !m.running:
			m.actionCursor++

		case key.Matches(msg, dbOpsEnter) && !m.running:
			if m.actionCursor < len(m.actions) {
				act := m.actions[m.actionCursor]
				if act.enabled {
					m.running = true
					m.lastResult = ""
					return m, act.execute()
				}
			}
		}

	case dbOpsDoneMsg:
		m.running = false
		if msg.err != nil {
			m.lastResult = styles.StyleError.Render("\u2717 " + msg.err.Error())
		} else {
			m.lastResult = styles.StyleSuccess.Render("\u2713 " + msg.result)
			// Re-scan dump states — imports/exports affect dump availability
			svc := m.currentService()
			if svc != nil {
				if se := svc.FindSyncEnv(m.selectedEnv); se != nil {
					m.actions = m.buildActionsList(se)
				}
			}
		}
		return m, ClearActionAfter(3 * time.Second)

	case ClearActionMsg:
		m.lastResult = ""
	}
	return m, nil
}

func (m *DBOpsModel) viewActions() string {
	syncEnv := m.currentService().FindSyncEnv(m.selectedEnv)
	if syncEnv == nil {
		m.screen = dbScreenSyncEnvList
		return m.View()
	}

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.StyleTitle.Render(
		fmt.Sprintf(" Actions for: %s ", m.selectedEnv)))
	b.WriteString("\n")
	b.WriteString(styles.StyleSubtitle.Render(
		fmt.Sprintf("  %s  %s:%s  %s@%s", syncEnv.DBType, syncEnv.Host, syncEnv.Port, syncEnv.User, syncEnv.Database)))
	b.WriteString("\n\n")

	actions := m.actions
	for i, act := range actions {
		prefix := "    "
		if i == m.actionCursor {
			prefix = "  > "
		}

		if act.enabled && !m.running {
			line := prefix + act.label
			if i == m.actionCursor {
				b.WriteString(styles.StyleListItemSelected.Render(line))
			} else {
				b.WriteString(styles.StyleListItem.Render(line))
			}
		} else {
			// Disabled or running — dimmed
			display := act.label
			if !act.enabled && act.disabledReason != "" {
				display = act.label + " (" + act.disabledReason + ")"
			}
			b.WriteString(styles.StyleSubtitle.Render(prefix + display))
		}
		b.WriteString("\n")
	}

	if m.running {
		b.WriteString("\n" + styles.StyleSubtitle.Render("  Running\u2026"))
	}

	if m.lastResult != "" {
		b.WriteString("\n  " + m.lastResult + "\n")
	}

	b.WriteString("\n")
	help := styles.HelpItem("\u2191\u2193", "select") + "   "
	if !m.running {
		help += styles.HelpItem("\u21B5", "run") + "   "
	}
	help += styles.HelpItem("esc", "back")
	b.WriteString(styles.StyleHelp.Render(help))
	return b.String()
}

// buildActionsList builds the list of available actions for the given sync-env,
// checking dump file existence at render time.
func (m *DBOpsModel) buildActionsList(se *config.SyncEnv) []actionItem {
	dDir := m.dumpDir(se)

	switch se.DBType {
	case "postgres":
		hasSchema := docker.HasDumpFile(dDir, "schema-*.sql")
		hasData := docker.HasDumpFile(dDir, "data-*.sql")
		return []actionItem{
			{label: "Export schema", enabled: true, execute: func() tea.Cmd { return m.exportSchema(se) }},
			{label: "Export data", enabled: true, execute: func() tea.Cmd { return m.exportData(se) }},
			{label: "Import schema", enabled: hasSchema, disabledReason: "no schema dump", execute: func() tea.Cmd { return m.importSchema(se) }},
			{label: "Import data", enabled: hasData, disabledReason: "no data dump", execute: func() tea.Cmd { return m.importData(se) }},
		}

	case "mongo":
		hasData := m.hasMongoDump(dDir)
		return []actionItem{
			{label: "Export", enabled: true, execute: func() tea.Cmd { return m.exportData(se) }},
			{label: "Import", enabled: hasData, disabledReason: "no dump", execute: func() tea.Cmd { return m.importData(se) }},
		}

	case "mysql":
		hasData := docker.HasDumpFile(dDir, "data-*.sql")
		return []actionItem{
			{label: "Export", enabled: true, execute: func() tea.Cmd { return m.exportData(se) }},
			{label: "Import", enabled: hasData, disabledReason: "no dump file", execute: func() tea.Cmd { return m.importData(se) }},
		}

	case "mssql":
		hasData := docker.HasDumpFile(dDir, "data-*.bacpac")
		return []actionItem{
			{label: "Export", enabled: true, execute: func() tea.Cmd { return m.exportData(se) }},
			{label: "Import", enabled: hasData, disabledReason: "no .bacpac file", execute: func() tea.Cmd { return m.importData(se) }},
		}
	}
	return nil
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
