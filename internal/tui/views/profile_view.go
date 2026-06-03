package views

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/docker"
	"github.com/stackr/stackr/internal/portforward"
	"github.com/stackr/stackr/internal/tui/styles"
)

type ProfileViewModel struct {
	cfg            *config.Config
	profile        *config.Profile
	activeEnv      int
	cursor         int
	envState       map[string]ProfileEnvState
	statuses       []docker.ServiceStatus
	statusErr      string
	width          int // terminal width, set via WindowSizeMsg
	height         int // terminal height, set via WindowSizeMsg
	deleteArmed    bool
	deleteEnvArmed bool
	resetArmed     bool
	inputtingEnv   bool
	envInput       textinput.Model

	keyPending string // for multi-key sequences: "" = none, "r" = waiting after r

	// ── Port-Forward ──
	forwardHandles map[string][]*portforward.PortForwardHandle

	localProcesses map[string]*exec.Cmd

	// ── Discovery State ──
	inDiscovery      bool
	discoveryStep    int
	discoveryCursor  int
	discoveryCtxs    []string
	discoveryNSs     []string
	discoverySvcs    []config.PortForward
	discoverySelCtx  string
	discoverySelNS   string
	discoveredPF     *config.PortForward
	discoveryEnvName string
	// Per-step cursor preservation for back navigation
	discoveryCtxCur int
	discoveryNSCur  int
	discoverySvcCur int

	// ── Port Editing ──
	editingPort      bool
	portInput        textinput.Model
	editFwdIdx       int
	viewingService   int    // -1 = not viewing
	viewingProcess   string // "" = not viewing
	showingShortcuts bool

	// ── Docker Stats ──
	showStats      bool
	statsContainer string
	statsData      string

	// ── Batch Spinner ──
	pendingOps   map[string]string // containerName → "start" | "stop"
	spinnerFrame int
}

type ProfileEnvState struct {
	Cursor      int
	ServiceName string
	ForwardName string
}

type ProfileNavState struct {
	ActiveEnvName string
	Environments  map[string]ProfileEnvState
}

func NewProfileView(cfg *config.Config, p *config.Profile) *ProfileViewModel {
	ti := textinput.New()
	ti.Placeholder = "env name"
	ti.CharLimit = 20
	ti.Width = 12

	portInput := textinput.New()
	portInput.Placeholder = "8080"
	portInput.CharLimit = 10
	portInput.Width = 8

	m := &ProfileViewModel{
		cfg:            cfg,
		profile:        p,
		envState:       make(map[string]ProfileEnvState),
		envInput:       ti,
		forwardHandles: make(map[string][]*portforward.PortForwardHandle),
		localProcesses: make(map[string]*exec.Cmd),
		portInput:      portInput,
		viewingService: -1,
		pendingOps:  make(map[string]string),
	}
	// set activeEnv to the profile's saved active env
	for i, env := range p.Environments {
		if env.Name == p.ActiveEnv {
			m.activeEnv = i
			break
		}
	}
	m.rememberCurrentEnvSelection()
	return m
}

func (m *ProfileViewModel) ProfileName() string {
	if m.profile != nil {
		return m.profile.Name
	}
	return ""
}

// SetSize propagates terminal dimensions so the viewport works before the first
// WindowSizeMsg arrives (e.g. when the model is created mid-session).
// h is the full terminal height; we subtract 5 for App chrome (header + sep + blank + footer sep + quit).
func (m *ProfileViewModel) SetSize(w, h int) {
	m.width = w
	m.height = max(0, h-5)
}

type statusLoadedMsg struct {
	statuses []docker.ServiceStatus
	err      string
}

// ── Container name helper ───────────────────────────────────────────────

func containerName(profile, env, compose string) string {
	return fmt.Sprintf("stackr-%s-%s-%s", profile, env, compose)
}

// ── Spinner ─────────────────────────────────────────────────────────────

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinnerCmd() tea.Cmd {
	return func() tea.Msg { return spinnerTickMsg{} }
}

func (m *ProfileViewModel) renderSpinnerDot(op string) string {
	c := styles.ColorAccent
	frame := spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
	return lipgloss.NewStyle().Foreground(c).Render(fmt.Sprintf("%s %s", frame, op))
}

type ClearActionMsg struct{}

func ClearActionAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return ClearActionMsg{}
	})
}

func (m *ProfileViewModel) loadStatus() tea.Cmd {
	return func() tea.Msg {
		return m.refreshStatus()
	}
}

// refreshStatus queries Docker status and returns a statusLoadedMsg.
// Used inside Cmd closures to refresh state after a docker operation completes.
func (m *ProfileViewModel) refreshStatus() tea.Msg {
	if len(m.profile.Environments) == 0 {
		return statusLoadedMsg{}
	}
	env := m.profile.Environments[m.activeEnv]
	ss, err := docker.Status(m.profile.Name, env.Name)
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	return statusLoadedMsg{statuses: ss, err: errStr}
}

var profileKeys = struct {
	back, up, down, enter, start, stop, addSvc, svcDelete, dbOps, switchEnvLeft, switchEnvRight, addEnv, envDelete, resetVol, k8sDiscover, editPort, viewConfig, stats, pull, shell key.Binding
}{
	back:           key.NewBinding(key.WithKeys("esc", "backspace")),
	up:             key.NewBinding(key.WithKeys("up")),
	down:           key.NewBinding(key.WithKeys("down", "j")),
	enter:          key.NewBinding(key.WithKeys("enter")),
	start:          key.NewBinding(key.WithKeys("s")),
	stop:           key.NewBinding(key.WithKeys("x")),
	addSvc:         key.NewBinding(key.WithKeys("a")),
	svcDelete:      key.NewBinding(key.WithKeys("d")),
	dbOps:          key.NewBinding(key.WithKeys("o")),
	switchEnvLeft:  key.NewBinding(key.WithKeys("left")),
	switchEnvRight: key.NewBinding(key.WithKeys("right")),
	addEnv:         key.NewBinding(key.WithKeys("n")),
	envDelete:      key.NewBinding(key.WithKeys("D")),
	resetVol:       key.NewBinding(key.WithKeys("r")),
	k8sDiscover:    key.NewBinding(key.WithKeys("k")),
	editPort:       key.NewBinding(key.WithKeys("e")),
	viewConfig:     key.NewBinding(key.WithKeys("v")),
	stats:          key.NewBinding(key.WithKeys("t")),
	pull:           key.NewBinding(key.WithKeys("p")),
	shell:          key.NewBinding(key.WithKeys("h")),
}

func (m *ProfileViewModel) Init() tea.Cmd {
	m.ensureValidSelection()
	m.recoverOrphanProcesses()
	var cmds []tea.Cmd
	cmds = append(cmds, m.loadStatus())
	if m.isPortForwardEnv() {
		m.buildForwardHandles()
		cmds = append(cmds, portforward.ForwardMonitorCmd())
	}
	return tea.Batch(cmds...)
}

func (m *ProfileViewModel) RestoreSelection(envName, serviceName string) {
	if strings.TrimSpace(envName) != "" {
		for i, env := range m.profile.Environments {
			if env.Name == envName {
				m.activeEnv = i
				break
			}
		}
	}
	m.restoreCurrentEnvSelection()
	if strings.TrimSpace(serviceName) != "" && !m.isPortForwardEnv() {
		for i, svc := range m.currentEnvServices() {
			if svc.Name == serviceName || svc.ComposeName() == serviceName {
				m.cursor = i
				break
			}
		}
	}
	m.ensureValidSelection()
	m.rememberCurrentEnvSelection()
}

func (m *ProfileViewModel) SnapshotNavState() ProfileNavState {
	state := ProfileNavState{
		Environments: make(map[string]ProfileEnvState, len(m.envState)),
	}
	m.rememberCurrentEnvSelection()
	if env := m.currentEnvUnsafe(); env != nil {
		state.ActiveEnvName = env.Name
	}
	for envName, envState := range m.envState {
		state.Environments[envName] = envState
	}
	return state
}

func (m *ProfileViewModel) RestoreNavState(state ProfileNavState) {
	if len(state.Environments) > 0 {
		m.envState = make(map[string]ProfileEnvState, len(state.Environments))
		for envName, envState := range state.Environments {
			m.envState[envName] = envState
		}
	}
	if strings.TrimSpace(state.ActiveEnvName) != "" {
		for i, env := range m.profile.Environments {
			if env.Name == state.ActiveEnvName {
				m.activeEnv = i
				break
			}
		}
	}
	m.restoreCurrentEnvSelection()
	m.ensureValidSelection()
	m.rememberCurrentEnvSelection()
}

func (m *ProfileViewModel) ensureValidSelection() {
	if len(m.profile.Environments) == 0 {
		m.activeEnv = 0
		m.cursor = 0
		return
	}
	if m.activeEnv < 0 {
		m.activeEnv = 0
	}
	if m.activeEnv >= len(m.profile.Environments) {
		m.activeEnv = len(m.profile.Environments) - 1
	}
	m.clampCursor()
}

func (m *ProfileViewModel) clampCursor() {
	total := m.totalServiceItems()
	if m.isPortForwardEnvUnsafe() {
		total = len(m.currentForwardsUnsafe())
	}
	if total <= 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= total {
		m.cursor = total - 1
	}
}

func (m *ProfileViewModel) rememberCurrentEnvSelection() {
	env := m.currentEnvUnsafe()
	if env == nil {
		return
	}
	m.clampCursor()
	state := ProfileEnvState{Cursor: m.cursor}
	if env.Name == "local" {
		services := env.Services
			svcIdx := m.selectedServiceIndex()
			if svcIdx >= 0 && svcIdx < len(services) {
				state.ServiceName = services[svcIdx].Name
		}
	} else {
		forwards := m.forwardHandles[env.Name]
			fwdIdx := m.selectedForwardIndex()
			if fwdIdx >= 0 && fwdIdx < len(forwards) {
				state.ForwardName = forwards[fwdIdx].Config.Name
		}
	}
	m.envState[env.Name] = state
}

func (m *ProfileViewModel) restoreCurrentEnvSelection() {
	env := m.currentEnvUnsafe()
	if env == nil {
		m.cursor = 0
		return
	}
	state, ok := m.envState[env.Name]
	if !ok {
		m.cursor = 0
		return
	}
	m.cursor = state.Cursor
	if env.Name == "local" && strings.TrimSpace(state.ServiceName) != "" {
		for i, svc := range env.Services {
			if svc.Name == state.ServiceName || svc.ComposeName() == state.ServiceName {
				m.cursor = i
				break
			}
		}
	}
	if env.Name != "local" && strings.TrimSpace(state.ForwardName) != "" {
		for i, h := range m.forwardHandles[env.Name] {
			if h.Config.Name == state.ForwardName {
				m.cursor = i
				break
			}
		}
	}
	m.clampCursor()
}

func (m *ProfileViewModel) currentEnvUnsafe() *config.Environment {
	if len(m.profile.Environments) == 0 || m.activeEnv < 0 || m.activeEnv >= len(m.profile.Environments) {
		return nil
	}
	return &m.profile.Environments[m.activeEnv]
}

func (m *ProfileViewModel) currentEnvServicesUnsafe() []config.Service {
	env := m.currentEnvUnsafe()
	if env == nil {
		return nil
	}
	return env.Services
}

func (m *ProfileViewModel) isPortForwardEnvUnsafe() bool {
	env := m.currentEnvUnsafe()
	if env == nil {
		return false
	}
	return env.Name != "local"
}

func (m *ProfileViewModel) currentForwardsUnsafe() []*portforward.PortForwardHandle {
	env := m.currentEnvUnsafe()
	if env == nil {
		return nil
	}
	return m.forwardHandles[env.Name]
}

func (m *ProfileViewModel) persistActiveEnv() {
	env := m.currentEnv()
	if env == nil || m.profile.ActiveEnv == env.Name {
		return
	}
	m.profile.ActiveEnv = env.Name
	_ = m.cfg.UpdateProfile(*m.profile)
}

func (m *ProfileViewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer m.rememberCurrentEnvSelection()

	switch msg := msg.(type) {
	case statusLoadedMsg:
		m.statuses = msg.statuses
		m.statusErr = msg.err
		m.ensureValidSelection()
		m.rebuildDisplayOrders()

		// Resolve pending ops against actual status
		statusMap := map[string]string{}
		for _, st := range m.statuses {
			statusMap[st.Name] = st.State
		}
		for key, op := range m.pendingOps {
			if strings.HasPrefix(key, "proc:") {
				procName := strings.TrimPrefix(key, "proc:")
				running := m.isLocalProcRunning(procName)
				if (op == "start" && running) || (op == "stop" && !running) {
					delete(m.pendingOps, key)
				}
			} else if strings.HasPrefix(key, "pf:") {
				fwdName := strings.TrimPrefix(key, "pf:")
				for _, h := range m.currentForwards() {
					if h.Config.Name == fwdName {
						if (op == "start" && (h.State == portforward.ForwardRunning || h.State == portforward.ForwardFailed)) ||
							(op == "stop" && (h.State == portforward.ForwardStopped || h.State == portforward.ForwardFailed)) {
							delete(m.pendingOps, key)
						}
						break
					}
				}
			} else {
				state := statusMap[key]
				if op == "start" && (state == "running" || state == "starting") {
					delete(m.pendingOps, key)
				}
				if op == "stop" && (state == "" || state == "stopped" || state == "exited") {
					delete(m.pendingOps, key)
				}
			}
		}

	case spinnerTickMsg:
		if len(m.pendingOps) > 0 {
			m.spinnerFrame++
			return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return spinnerTickMsg{}
			})
		}
		m.spinnerFrame = 0

	case ClearActionMsg:

	case statsTickMsg:
		if !m.showStats || m.statsContainer == "" {
			return m, nil
		}
		return m, func() tea.Msg {
			out, err := exec.Command("docker", "stats", "--no-stream",
				"--format", "{{.CPUPerc}}|{{.MemPerc}}|{{.NetIO}}|{{.BlockIO}}",
				msg.container).Output()
			if err != nil {
				return statsResultMsg{err: err.Error()}
			}
			return statsResultMsg{data: strings.TrimSpace(string(out))}
		}

	case statsResultMsg:
		if msg.err != "" {
			m.statsData = msg.err
		} else {
			m.statsData = msg.data
		}
		if m.showStats {
			return m, tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
				return statsTickMsg{container: m.statsContainer}
			})
		}
		return m, nil

	case portforward.ForwardMonitorTick:
		m.ensureValidSelection()
		if m.isPortForwardEnv() {
			envName := m.profile.Environments[m.activeEnv].Name
			for _, h := range m.forwardHandles[envName] {
				if h.State == portforward.ForwardRunning {
					h.CheckAlive()
				}
			}
			return m, portforward.ForwardMonitorCmd()
		}
		return m, nil

	case discoveryContextsMsg:
		delete(m.pendingOps, "discovery")
		m.discoveryCtxs = msg.contexts
		m.discoveryNSs = nil
		m.discoverySvcs = nil
		m.discoverySelCtx = ""
		m.discoverySelNS = ""
		if len(msg.contexts) == 0 {
			m.inDiscovery = false
			m.discoveryStep = 0
			return m, nil
		}
		m.inDiscovery = true
		m.discoveryStep = 0
		m.discoveryCursor = 0
		m.ensureValidSelection()

	case discoveryNamespacesMsg:
		delete(m.pendingOps, "discovery")
		m.discoveryNSs = msg.namespaces
		m.discoverySvcs = nil
		m.discoveryCursor = 0

	case discoveryServicesMsg:
		delete(m.pendingOps, "discovery")
		m.discoverySvcs = msg.services
		m.discoveryCursor = 0

	case discoveryErrorMsg:
		delete(m.pendingOps, "discovery")
		m.inDiscovery = false
		m.discoveryStep = 0
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = max(0, msg.Height-5) // App chrome (header + sep + blank + footer sep + quit)
		m.ensureValidSelection()

	case tea.KeyMsg:
		// Dismiss overlays on any key
		if m.viewingService >= 0 {
			m.viewingService = -1
			return m, nil
		}
		if m.viewingProcess != "" {
			m.viewingProcess = ""
			return m, nil
		}
		if m.showingShortcuts {
			m.showingShortcuts = false
			return m, nil
		}
		if m.showStats {
			m.showStats = false
			m.statsContainer = ""
			m.statsData = ""
			return m, nil
		}

		// ── Pending key sequence ────────────────────────────────
		if m.keyPending != "" && !m.inputtingEnv && !m.inDiscovery && !m.editingPort {
			pending := m.keyPending
			m.keyPending = ""
			m.resetArmed = false
			switch pending {
			case "r":
				env := m.currentEnv()
				switch msg.String() {
				case "r":
					// rr → reset all (delete all services/forwards)
					if m.isPortForwardEnv() {
						forwards := m.currentForwards()
						if len(forwards) == 0 {
							m.keyPending = ""
							return m, nil
						}
						envName := m.profile.Environments[m.activeEnv].Name
						for _, h := range forwards {
							_ = h.Stop()
						}
						updated, err := m.cfg.ClearPortForwards(m.profile.Name, envName)
						if err != nil {
							return m, nil
						}
						m.profile = updated
						m.buildForwardHandles()
						m.cursor = 0
						m.rememberCurrentEnvSelection()
						return m, nil
					}
					if len(m.currentEnvServices()) == 0 && len(env.LocalProcesses) == 0 {
						m.keyPending = ""
						return m, nil
					}
					go func() {
						_ = docker.Down(m.profile.Name, env.Name)
					}()
					m.StopAllLocalProcesses()
					updated, err := m.cfg.ClearServices(m.profile.Name, env.Name)
					if err != nil {
						return m, m.loadStatus()
					}
					m.profile = updated
					if _, err := config.GenerateCompose(*m.profile, env.Name); err != nil {
						return m, m.loadStatus()
					}
					m.cursor = 0
					m.rememberCurrentEnvSelection()
					return m, m.loadStatus()

				case "v":
					// rv → reset volumes (Docker only)
					if m.isPortForwardEnv() || len(m.currentEnvServices()) == 0 {
						break
					}
					return m, tea.Batch(
						func() tea.Msg {
							_ = docker.DownVolumes(m.profile.Name, env.Name)
							return m.refreshStatus()
						},
					)
				}
			}
			// Invalid sequence → let key fall through to normal handling below
		}

		// ── Input mode: add environment ──────────────────────────
		if m.inputtingEnv {
			switch {
			case msg.String() == "esc":
				m.inputtingEnv = false
				m.envInput.SetValue("")
				m.envInput.Blur()
				m.keyPending = ""
				m.resetArmed = false
				return m, nil

			case key.Matches(msg, profileKeys.enter):
				name := strings.TrimSpace(m.envInput.Value())
				m.inputtingEnv = false
				m.envInput.SetValue("")
				m.envInput.Blur()
				m.keyPending = ""
				m.resetArmed = false

				if name == "" || name == "." || name == ".." {
					return m, nil
				}

				// Check for duplicates
				for _, env := range m.profile.Environments {
					if env.Name == name {
						return m, nil
					}
				}

				// Sanitize for docker-compose safety
				sanitized := config.SanitizeComposeName(name)
				if sanitized != name {
					return m, nil
				}

				m.profile.Environments = append(m.profile.Environments, config.Environment{Name: name})
				if err := m.cfg.UpdateProfile(*m.profile); err != nil {
					return m, nil
				}
				// Generate empty compose for the new env
				if _, err := config.GenerateCompose(*m.profile, name); err != nil {
					return m, nil
				}
				m.envState[name] = ProfileEnvState{}
				return m, m.loadStatus()

			default:
				var cmd tea.Cmd
				m.envInput, cmd = m.envInput.Update(msg)
				return m, cmd
			}
		}

		// ── Discovery mode (captures ALL keys) ──
		if m.inDiscovery {
			return m.handleDiscoveryKey(msg)
		}

		// ── Port editing mode ──
		if m.editingPort {
			return m.handlePortEditKey(msg)
		}

		// ── Normal mode ──────────────────────────────────────────
		switch {
		case key.Matches(msg, profileKeys.back):
			m.persistActiveEnv()
			return m, func() tea.Msg { return NavigateTo{Screen: NavHome} }

		case key.Matches(msg, profileKeys.switchEnvLeft):
			if m.activeEnv > 0 {
				m.rememberCurrentEnvSelection()
				m.activeEnv--
				m.statuses = nil
					m.persistActiveEnv()
				m.restoreCurrentEnvSelection()
				if m.isPortForwardEnv() {
					m.buildForwardHandles()
					m.restoreCurrentEnvSelection()
					return m, tea.Batch(m.loadStatus(), portforward.ForwardMonitorCmd())
				}
				return m, m.loadStatus()
			}

		case key.Matches(msg, profileKeys.switchEnvRight):
			if m.activeEnv < len(m.profile.Environments)-1 {
				m.rememberCurrentEnvSelection()
				m.activeEnv++
				m.statuses = nil
					m.persistActiveEnv()
				m.restoreCurrentEnvSelection()
				if m.isPortForwardEnv() {
					m.buildForwardHandles()
					m.restoreCurrentEnvSelection()
					return m, tea.Batch(m.loadStatus(), portforward.ForwardMonitorCmd())
				}
				return m, m.loadStatus()
			}

		case key.Matches(msg, profileKeys.addEnv):
			// In portforward mode, 'e' edits its local port; 'n' remains add env.
			m.keyPending = ""
			m.resetArmed = false
			m.inputtingEnv = true
			m.envInput.Focus()
			m.envInput.SetValue("")
			return m, textinput.Blink

		case key.Matches(msg, profileKeys.up):
			if m.cursor > 0 {
				m.cursor--
			}

		case key.Matches(msg, profileKeys.down):
			total := m.totalServiceItems()
			if m.isPortForwardEnv() {
				total = len(m.currentForwards())
			}
			if m.cursor < total-1 {
				m.cursor++
			}

		case key.Matches(msg, profileKeys.editPort):
			if m.isPortForwardEnv() && len(m.currentForwards()) > 0 && m.cursor < len(m.currentForwards()) {
				return m.handlePortEditStart()
			}

		case key.Matches(msg, profileKeys.k8sDiscover):
			if m.isPortForwardEnv() && !m.inDiscovery {
				if !portforward.CheckKubectl() {
					return m, nil
				}
				m.inDiscovery = true
				m.discoveryStep = 0
				m.discoveryCursor = 0
				if knownCtx := m.knownForwardCtx(); knownCtx != "" {
					m.discoveryStep = 1
					m.discoveryNSs = nil
					m.discoverySvcs = nil
					m.discoverySelCtx = knownCtx
					m.discoverySelNS = ""
					m.pendingOps["discovery"] = "loading"
					return m, tea.Batch(
						func() tea.Msg {
							nss, err := portforward.DiscoverNamespaces(knownCtx)
							if err != nil {
								return discoveryErrorMsg{err: err.Error()}
							}
							if len(nss) == 0 {
								return discoveryErrorMsg{err: "no namespaces found in context " + knownCtx}
							}
							return discoveryNamespacesMsg{namespaces: nss}
						},
						spinnerCmd(),
					)
				}
				m.pendingOps["discovery"] = "loading"
				return m, tea.Batch(
					func() tea.Msg {
						ctxs, err := portforward.DiscoverContexts()
						if err != nil {
							return discoveryErrorMsg{err: err.Error()}
						}
						if len(ctxs) == 0 {
							return discoveryErrorMsg{err: "no K8s contexts found"}
						}
						return discoveryContextsMsg{contexts: ctxs}
					},
					spinnerCmd(),
				)
			}

		case key.Matches(msg, profileKeys.enter):
			if m.isPortForwardEnv() {
				return m.handleForwardToggle()
			}
			env := m.currentEnv()
			if env == nil {
				break
			}
			totalSvcs := len(env.Services)
			if m.cursor < totalSvcs {
				// Docker service toggle
				svcs := m.currentEnvServices()
				svcIdx := m.selectedServiceIndex()
				if svcIdx < len(svcs) {
					svc := svcs[svcIdx]
					envName := env.Name
					compName := svc.ComposeName()
					containerName := fmt.Sprintf("stackr-%s-%s-%s", m.profile.Name, envName, compName)
					isRunning := false
					for _, st := range m.statuses {
						if st.Name == containerName && st.State == "running" {
							isRunning = true
							break
						}
					}
					if isRunning {
						m.pendingOps[containerName] = "stop"
						return m, tea.Batch(
							func() tea.Msg {
								_ = docker.DownService(m.profile.Name, envName, compName)
								return m.refreshStatus()
							},
							m.loadStatus(),
							func() tea.Msg { return spinnerTickMsg{} },
						)
					}
					m.pendingOps[containerName] = "start"
					return m, tea.Batch(
						func() tea.Msg {
							_ = docker.UpService(m.profile.Name, envName, compName)
							return m.refreshStatus()
						},
						m.loadStatus(),
						func() tea.Msg { return spinnerTickMsg{} },
					)
				}
			} else {
				// Local process toggle
				procIdx := m.cursor - totalSvcs
				if procIdx < len(env.LocalProcesses) {
					proc := env.LocalProcesses[procIdx]
					if m.isLocalProcRunning(proc.Name) {
						m.pendingOps["proc:"+proc.Name] = "stop"
						cmd := m.localProcesses[proc.Name]
						return m, tea.Batch(
							func() tea.Msg {
								if cmd != nil && cmd.Process != nil {
									_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
									go func() { cmd.Process.Wait() }()
								}
								delete(m.localProcesses, proc.Name)
								m.cleanPIDFile(proc.Name)
								return m.refreshStatus()
							},
							m.loadStatus(),
							func() tea.Msg { return spinnerTickMsg{} },
						)
					}
					m.pendingOps["proc:"+proc.Name] = "start"
					return m, tea.Batch(
						func() tea.Msg {
							if m.isLocalProcRunning(proc.Name) {
								if cmd := m.localProcesses[proc.Name]; cmd != nil && cmd.Process != nil {
									_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
									go func() { cmd.Process.Wait() }()
								}
							}
							cmd := exec.Command("sh", "-c", proc.Command)
							cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
							if proc.WorkDir != "" {
								wd := proc.WorkDir
								if strings.HasPrefix(wd, "~") {
									wd = strings.Replace(wd, "~", os.Getenv("HOME"), 1)
								}
								cmd.Dir = wd
							}
							if err := cmd.Start(); err != nil {
								return m.refreshStatus()
							}
							m.localProcesses[proc.Name] = cmd
							m.writePIDFile(proc.Name, cmd.Process.Pid)
							return m.refreshStatus()
						},
						m.loadStatus(),
						func() tea.Msg { return spinnerTickMsg{} },
					)
				}
			}

		case key.Matches(msg, profileKeys.start):
			env := m.currentEnv()
			if env != nil {
				if m.isPortForwardEnv() {
					envName := m.profile.Environments[m.activeEnv].Name
					for _, h := range m.forwardHandles[envName] {
						if h.State != portforward.ForwardRunning && h.State != portforward.ForwardStarting {
							m.pendingOps["pf:"+h.Config.Name] = "start"
						}
					}
					cmds := m.startEnvForwards(envName)
					cmds = append(cmds, m.loadStatus(), func() tea.Msg { return spinnerTickMsg{} })
					return m, tea.Batch(cmds...)
				}
				// Populate pending ops for spinner
				for i := range env.Services {
					svc := env.Services[i]
					cn := containerName(m.profile.Name, env.Name, svc.ComposeName())
					m.pendingOps[cn] = "start"
				}
				for i := range env.LocalProcesses {
					m.pendingOps["proc:"+env.LocalProcesses[i].Name] = "start"
				}
				// Start ALL local processes at once (non-blocking)
				m.startAllLocalProcesses(env)
				// Launch ALL Docker services concurrently
				var cmds []tea.Cmd
				for i := range env.Services {
					svc := env.Services[i]
					cmds = append(cmds, func() tea.Msg {
						_ = docker.UpService(m.profile.Name, env.Name, svc.ComposeName())
						return m.refreshStatus()
					})
				}
				cmds = append(cmds, m.loadStatus(), func() tea.Msg { return spinnerTickMsg{} })
				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, profileKeys.stop):
			env := m.currentEnv()
			if env != nil {
				if m.isPortForwardEnv() {
					envName := m.profile.Environments[m.activeEnv].Name
					for _, h := range m.forwardHandles[envName] {
						if h.State == portforward.ForwardRunning || h.State == portforward.ForwardStarting {
							m.pendingOps["pf:"+h.Config.Name] = "stop"
						}
					}
					cmds := m.stopEnvForwards(envName)
					cmds = append(cmds, m.loadStatus(), func() tea.Msg { return spinnerTickMsg{} })
					return m, tea.Batch(cmds...)
				}
				// Populate pending ops for spinner
				for i := range env.Services {
					svc := env.Services[i]
					cn := containerName(m.profile.Name, env.Name, svc.ComposeName())
					m.pendingOps[cn] = "stop"
				}
				for i := range env.LocalProcesses {
					m.pendingOps["proc:"+env.LocalProcesses[i].Name] = "stop"
				}
				// Stop ALL local processes at once
				m.StopAllLocalProcesses()
				// Launch ALL Docker services concurrently
				var cmds []tea.Cmd
				for i := range env.Services {
					svc := env.Services[i]
					cmds = append(cmds, func() tea.Msg {
						_ = docker.DownService(m.profile.Name, env.Name, svc.ComposeName())
						return m.refreshStatus()
					})
				}
				cmds = append(cmds, m.loadStatus(), func() tea.Msg { return spinnerTickMsg{} })
				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, profileKeys.resetVol):
			env := m.currentEnv()
			if env == nil {
				break
			}
			if m.isPortForwardEnv() && len(m.currentForwards()) == 0 {
				break
			}
			if !m.isPortForwardEnv() && len(m.currentEnvServices()) == 0 {
				break
			}
			// First r: arm, wait for second key (r or v)
			m.keyPending = "r"
			m.resetArmed = true
			return m, nil

		case key.Matches(msg, profileKeys.addSvc):
			if m.isPortForwardEnv() {
				break
			}
			env := m.currentEnv()
			envName := "local"
			if env != nil {
				envName = env.Name
			}
			return m, func() tea.Msg {
				return NavigateTo{
					Screen:      NavAddService,
					ProfileName: m.profile.Name,
					EnvName:     envName,
				}
			}

		case key.Matches(msg, profileKeys.svcDelete):
			if m.isPortForwardEnv() {
				return m.handleForwardDelete()
			}
			env := m.currentEnv()
			svcs := m.currentEnvServices()

			if m.cursor < len(svcs) {
				// Docker service delete (existing logic)
				if env == nil || len(svcs) <= 1 || m.cursor >= len(svcs) {
					break
				}
				if !m.deleteArmed {
					m.deleteArmed = true
					return m, nil
				}
				m.deleteArmed = false
				svc := svcs[m.selectedServiceIndex()]
				go func() {
					_ = docker.Down(m.profile.Name, env.Name)
				}()
				updated, err := m.cfg.DeleteService(m.profile.Name, env.Name, svc.Name)
				if err != nil {
					return m, m.loadStatus()
				}
				m.profile = updated
				if _, err := config.GenerateCompose(*m.profile, env.Name); err != nil {
					return m, m.loadStatus()
				}
				remaining := m.totalServiceItems()
				if m.cursor >= remaining && m.cursor > 0 {
					m.cursor--
				}
				m.rememberCurrentEnvSelection()
				return m, m.loadStatus()
			} else {
				// Local process delete
				procIdx := m.cursor - len(svcs)
				if procIdx < 0 || procIdx >= len(env.LocalProcesses) {
					break
				}
				if !m.deleteArmed {
					m.deleteArmed = true
					return m, nil
				}
				m.deleteArmed = false
				proc := env.LocalProcesses[procIdx]
				if cmd, ok := m.localProcesses[proc.Name]; ok && cmd != nil && cmd.Process != nil {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					go func() { cmd.Process.Wait() }()
					delete(m.localProcesses, proc.Name)
				}
				updated, err := m.cfg.DeleteLocalProcess(m.profile.Name, env.Name, proc.Name)
				if err != nil {
					return m, m.loadStatus()
				}
				m.profile = updated
				remaining := m.totalServiceItems()
				if m.cursor >= remaining && m.cursor > 0 {
					m.cursor--
				}
				m.rememberCurrentEnvSelection()
				return m, m.loadStatus()
			}

		case key.Matches(msg, profileKeys.envDelete):
			if len(m.profile.Environments) == 0 {
				break
			}
			if !m.deleteEnvArmed {
				m.deleteEnvArmed = true
				return m, nil
			}
			// Second D: delete current environment
			m.deleteEnvArmed = false
			envName := m.profile.Environments[m.activeEnv].Name

			// ⚠️ Prevent deletion of local environment
			if envName == "local" {
				return m, nil
			}

			// Stop running forwards for this env
			m.stopEnvForwardsNow(envName)
			delete(m.forwardHandles, envName)
			delete(m.envState, envName)

			envIdx := m.activeEnv
			// Stop containers for this env (fire and forget)
			_ = docker.Down(m.profile.Name, envName)
			// Remove env directory
			_ = os.RemoveAll(filepath.Join(config.ProfileDir(m.profile.Name), envName))
			// Remove env from profile
			m.profile.Environments = append(
				m.profile.Environments[:envIdx],
				m.profile.Environments[envIdx+1:]...,
			)
			if err := m.cfg.UpdateProfile(*m.profile); err != nil {
				return m, nil
			}
			// Adjust activeEnv if needed
			m.ensureValidSelection()
			m.restoreCurrentEnvSelection()
			m.persistActiveEnv()
			return m, m.loadStatus()

		case key.Matches(msg, profileKeys.dbOps):
			svcs := m.currentEnvServices()
			if m.cursor < len(svcs) && m.selectedDBType() != "" {
				env := m.currentEnv()
				svc := svcs[m.selectedServiceIndex()]
				return m, func() tea.Msg {
					return NavigateTo{
						Screen:      NavDBOps,
						ProfileName: m.profile.Name,
						EnvName:     env.Name,
						ServiceName: svc.Name,
					}
				}
			}

		case key.Matches(msg, profileKeys.viewConfig):
			env := m.currentEnv()
			if env != nil {
				totalSvcs := len(env.Services)
				if m.cursor < totalSvcs {
					m.viewingService = m.selectedServiceIndex()
					return m, nil
				}
				procIdx := m.cursor - totalSvcs
				if procIdx >= 0 && procIdx < len(env.LocalProcesses) {
					m.viewingProcess = env.LocalProcesses[procIdx].Name
					return m, nil
				}
			}

		case key.Matches(msg, profileKeys.stats):
			if m.isPortForwardEnv() {
				break
			}
			svcs := m.currentEnvServices()
			if m.cursor >= len(svcs) {
				break
			}
			svc := svcs[m.selectedServiceIndex()]
			envName := m.profile.Environments[m.activeEnv].Name
			compName := svc.ComposeName()
			containerName := fmt.Sprintf("stackr-%s-%s-%s", m.profile.Name, envName, compName)

			isRunning := false
			for _, st := range m.statuses {
				if st.Name == containerName && st.State == "running" {
					isRunning = true
					break
				}
			}
			if !isRunning {
				return m, nil
			}
			m.showStats = true
			m.statsContainer = containerName
			m.statsData = "Loading..."
			return m, tea.Tick(1*time.Millisecond, func(t time.Time) tea.Msg {
				return statsTickMsg{container: containerName}
			})

		case key.Matches(msg, profileKeys.pull):
			if m.isPortForwardEnv() {
				break
			}
			svcs := m.currentEnvServices()
			if m.cursor >= len(svcs) {
				break
			}
			svc := svcs[m.selectedServiceIndex()]
			env := m.currentEnv()
			return m, tea.Batch(
				func() tea.Msg {
					_ = docker.ComposePull(m.profile.Name, env.Name, svc.Name)
					return m.refreshStatus()
				},
			)

		case key.Matches(msg, profileKeys.shell):
			if m.isPortForwardEnv() {
				break
			}
			svcs := m.currentEnvServices()
			if m.cursor >= len(svcs) {
				break
			}
			svc := svcs[m.selectedServiceIndex()]
			env := m.currentEnv()
			compName := svc.ComposeName()
			containerName := fmt.Sprintf("stackr-%s-%s-%s", m.profile.Name, env.Name, compName)

			isRunning := false
			for _, st := range m.statuses {
				if st.Name == containerName && st.State == "running" {
					isRunning = true
					break
				}
			}
			if !isRunning {
				return m, nil
			}
			cmd, err := docker.ExecShell(containerName)
			if err != nil {
				return m, nil
			}
			return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
				return nil
			})

		case msg.String() == "?":
			m.showingShortcuts = true
			return m, nil
		}
	}
	m.ensureValidSelection()
	m.deleteArmed = false
	m.deleteEnvArmed = false
	m.resetArmed = false
	m.keyPending = ""
	return m, nil
}

func (m *ProfileViewModel) selectedDBType() string {
	svcs := m.currentEnvServices()
	svcIdx := m.selectedServiceIndex()
	if svcIdx < 0 || svcIdx >= len(svcs) {
		return ""
	}
	return svcs[svcIdx].DBType()
}

func (m *ProfileViewModel) isSelectedServiceRunning() bool {
	env := m.currentEnv()
	if env == nil {
		return false
	}
	svcIdx := m.selectedServiceIndex()
	if svcIdx < 0 || svcIdx >= len(env.Services) {
		return false
	}
	svc := env.Services[svcIdx]
	containerName := fmt.Sprintf("stackr-%s-%s-%s", m.profile.Name, env.Name, svc.ComposeName())
	for _, st := range m.statuses {
		if st.Name == containerName && st.State == "running" {
			return true
		}
	}
	return false
}

func (m *ProfileViewModel) rebuildDisplayOrders() {
	// Identity mapping — items appear in config order
}

// selectedServiceIndex returns the data index of the currently selected service.
// With identity mapping this is simply m.cursor.
func (m *ProfileViewModel) selectedServiceIndex() int {
	return m.cursor
}

// selectedForwardIndex returns the data index of the currently selected forward.
// With identity mapping this is simply m.cursor.
func (m *ProfileViewModel) selectedForwardIndex() int {
	return m.cursor
}

func (m *ProfileViewModel) currentEnv() *config.Environment {
	m.ensureValidSelection()
	if len(m.profile.Environments) == 0 {
		return nil
	}
	return &m.profile.Environments[m.activeEnv]
}

func (m *ProfileViewModel) currentEnvServices() []config.Service {
	m.ensureValidSelection()
	env := m.currentEnv()
	if env == nil {
		return nil
	}
	return env.Services
}

func (m *ProfileViewModel) totalServiceItems() int {
	env := m.currentEnvUnsafe()
	if env == nil {
		return 0
	}
	return len(env.Services) + len(env.LocalProcesses)
}

func (m *ProfileViewModel) isPortForwardEnv() bool {
	m.ensureValidSelection()
	return m.isPortForwardEnvUnsafe()
}

func (m *ProfileViewModel) currentForwards() []*portforward.PortForwardHandle {
	m.ensureValidSelection()
	if len(m.profile.Environments) == 0 {
		return nil
	}
	envName := m.profile.Environments[m.activeEnv].Name
	return m.forwardHandles[envName]
}

// knownForwardCtx returns the K8s context of the first existing forward,
// or empty string if there are none. This avoids re-selecting the context
// on subsequent discovery runs.
func (m *ProfileViewModel) knownForwardCtx() string {
	forwards := m.currentForwards()
	if len(forwards) > 0 {
		return forwards[0].Config.K8sContext
	}
	return ""
}

func (m *ProfileViewModel) buildForwardHandles() {
	m.ensureValidSelection()
	if len(m.profile.Environments) == 0 {
		return
	}
	envName := m.profile.Environments[m.activeEnv].Name
	env := m.currentEnv()
	if env == nil {
		return
	}

	// Build set of existing handle keys
	existing := make(map[string]*portforward.PortForwardHandle)
	for _, h := range m.forwardHandles[envName] {
		existing[h.Config.Name] = h
	}

	var handles []*portforward.PortForwardHandle
	for _, pf := range env.PortForwards {
		if h, ok := existing[pf.Name]; ok {
			h.Config = pf // update config (e.g. port changed)
			handles = append(handles, h)
		} else {
			handles = append(handles, portforward.NewHandle(pf))
		}
	}
	m.forwardHandles[envName] = handles
	m.clampCursor()
}

func (m *ProfileViewModel) isLocalProcRunning(name string) bool {
	cmd, ok := m.localProcesses[name]
	if !ok {
		return false
	}
	return cmd != nil && cmd.Process != nil && cmd.ProcessState == nil
}

func (m *ProfileViewModel) startEnvForwards(envName string) []tea.Cmd {
	var cmds []tea.Cmd
	for _, h := range m.forwardHandles[envName] {
		if h.State != portforward.ForwardRunning && h.State != portforward.ForwardStarting {
			h := h // capture
			cmds = append(cmds, func() tea.Msg {
				h.Start()
				return m.refreshStatus()
			})
		}
	}
	return cmds
}

func (m *ProfileViewModel) stopEnvForwards(envName string) []tea.Cmd {
	var cmds []tea.Cmd
	for _, h := range m.forwardHandles[envName] {
		if h.State == portforward.ForwardRunning || h.State == portforward.ForwardStarting {
			h := h // capture
			cmds = append(cmds, func() tea.Msg {
				h.Stop()
				return m.refreshStatus()
			})
		}
	}
	return cmds
}

func (m *ProfileViewModel) stopEnvForwardsNow(envName string) {
	for _, h := range m.forwardHandles[envName] {
		_ = h.Stop()
	}
}

func (m *ProfileViewModel) StopAllForwards() {
	for envName := range m.forwardHandles {
		m.stopEnvForwardsNow(envName)
	}
}

func (m *ProfileViewModel) StopAllLocalProcesses() {
	for name, cmd := range m.localProcesses {
		if cmd != nil && cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			go func(c *exec.Cmd) { c.Process.Wait() }(cmd)
		}
		m.cleanPIDFile(name)
		delete(m.localProcesses, name)
	}
}

// ── Local process PID persistence ─────────────────────────────────────────
// PID files allow Stackr to detect orphaned local processes that survived a
// Ctrl+C hard shutdown, so the UI shows "running" instead of "disconnected".

func (m *ProfileViewModel) procDir() string {
	env := m.currentEnv()
	if env == nil {
		return ""
	}
	return filepath.Join(config.ProfileDir(m.profile.Name), env.Name, "procs")
}

func (m *ProfileViewModel) pidFilePath(name string) string {
	return filepath.Join(m.procDir(), name+".pid")
}

func (m *ProfileViewModel) writePIDFile(name string, pid int) {
	dir := m.procDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	_ = os.WriteFile(m.pidFilePath(name), []byte(fmt.Sprintf("%d\n", pid)), 0644)
}

func (m *ProfileViewModel) cleanPIDFile(name string) {
	_ = os.Remove(m.pidFilePath(name))
}

// recoverOrphanProcesses checks PID files and re-populates localProcesses
// with any processes that survived a hard shutdown (Ctrl+C).
func (m *ProfileViewModel) recoverOrphanProcesses() {
	dir := m.procDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // dir doesn't exist — no orphans
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pid") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".pid")
		if _, ok := m.localProcesses[name]; ok {
			continue // already tracked
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			m.cleanPIDFile(name)
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			m.cleanPIDFile(name)
			continue
		}
		// Signal 0 checks if the process exists (Unix-only, no signal sent)
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			m.cleanPIDFile(name)
			continue
		}
		// Process is alive — restore tracking so isLocalProcRunning works
		m.localProcesses[name] = &exec.Cmd{Process: proc}
	}
}

// startLocalProcess starts a single local process and stores the handle.
// Returns true if the process was started, false on error.
func (m *ProfileViewModel) startLocalProcess(proc config.LocalProcess) bool {
	if m.isLocalProcRunning(proc.Name) {
		if cmd := m.localProcesses[proc.Name]; cmd != nil && cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			go func(c *exec.Cmd) { c.Process.Wait() }(cmd)
		}
	}
	cmd := exec.Command("sh", "-c", proc.Command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if proc.WorkDir != "" {
		wd := proc.WorkDir
		if strings.HasPrefix(wd, "~") {
			wd = strings.Replace(wd, "~", os.Getenv("HOME"), 1)
		}
		cmd.Dir = wd
	}
	if err := cmd.Start(); err != nil {
		return false
	}
	m.localProcesses[proc.Name] = cmd
	m.writePIDFile(proc.Name, cmd.Process.Pid)
	return true
}

// startAllLocalProcesses starts every local process in the given environment.
func (m *ProfileViewModel) startAllLocalProcesses(env *config.Environment) {
	for i := range env.LocalProcesses {
		m.startLocalProcess(env.LocalProcesses[i])
	}
}

func (m *ProfileViewModel) View() string {
	if m.showingShortcuts {
		return m.renderShortcuts()
	}

	var b strings.Builder
	b.WriteString("\n")

	// Env tabs
	tabsLine := m.renderEnvTabs()
	if !m.inDiscovery && m.viewingService < 0 && !m.showStats {
		var running, total int
		if m.isPortForwardEnv() {
			for _, h := range m.currentForwards() {
				total++
				if h.State == portforward.ForwardRunning || h.State == portforward.ForwardStarting {
					running++
				}
			}
		} else {
			env := m.currentEnv()
			if env != nil {
				total = len(env.Services) + len(env.LocalProcesses)
				for _, svc := range env.Services {
					containerName := fmt.Sprintf("stackr-%s-%s-%s", m.profile.Name, env.Name, svc.ComposeName())
					for _, st := range m.statuses {
						if st.Name == containerName && st.State == "running" {
							running++
							break
						}
					}
				}
				for _, proc := range env.LocalProcesses {
					if m.isLocalProcRunning(proc.Name) {
						running++
					}
				}
			}
		}
		if total > 0 {
			countStr := fmt.Sprintf("%d/%d running", running, total)
			tabsLine = tabsLine + "  " + styles.StyleSubtitle.Render(countStr)
		}
	}
	b.WriteString(tabsLine)
	b.WriteString("\n\n")

	env := m.currentEnv()
	if env == nil {
		b.WriteString(styles.StyleSubtitle.Render("  No environments configured."))
	} else if m.isPortForwardEnv() {
		if ctx := m.knownForwardCtx(); ctx != "" {
			b.WriteString("\n")
			b.WriteString(styles.StyleSubtitle.Render("  context: " + shortCtxName(ctx)))
			b.WriteString("\n\n")
		}
		if m.inDiscovery {
			b.WriteString(m.renderDiscovery())
		} else if m.editingPort {
			b.WriteString(m.renderPortForwards())
			b.WriteString("\n\n")
			b.WriteString(m.renderPortEditor())
		} else {
			b.WriteString(m.renderPortForwards())
		}
	} else {
		if m.showStats {
			b.WriteString(m.renderStats())
		} else if m.viewingService >= 0 && m.viewingService < len(env.Services) {
			b.WriteString(m.renderConfigPanel(env.Services[m.viewingService]))
		} else if m.viewingProcess != "" {
			for i := range env.LocalProcesses {
				if env.LocalProcesses[i].Name == m.viewingProcess {
					b.WriteString(m.renderLocalProcPanel(env.LocalProcesses[i]))
					break
				}
			}
		} else {
			b.WriteString(m.renderServices(env))
		}
	}

	if m.deleteArmed {
		b.WriteString("\n")
		b.WriteString(styles.StyleError.Render("  Press d again to confirm removal"))
		b.WriteString("\n")
	}

	if m.deleteEnvArmed {
		b.WriteString("\n")
		b.WriteString(styles.StyleError.Render("  Press D again to confirm environment removal"))
		b.WriteString("\n")
	}

	if m.resetArmed {
		b.WriteString("\n")
		if m.isPortForwardEnv() {
			b.WriteString(styles.StyleError.Render("  Press r again to delete all forwards"))
		} else {
			b.WriteString(styles.StyleError.Render("  Press r again to delete all, or v to reset volumes"))
		}
		b.WriteString("\n")
	}

	if m.inputtingEnv {
		b.WriteString("\n")
		focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		b.WriteString("  " + focusStyle.Render("New env name:") + " " + m.envInput.View())
		b.WriteString(styles.StyleSubtitle.Render("   ↵ add"))
		b.WriteString("\n")
	}

	if !m.inDiscovery && m.viewingService < 0 && m.viewingProcess == "" && !m.showStats {
		b.WriteString("\n\n")
	}

	// ── Help bar ──
	if m.inDiscovery || m.viewingService >= 0 || m.viewingProcess != "" || m.showStats {
		// No help bar needed — discovery/config/stats have their own hints
	} else if m.isPortForwardEnv() {
		b.WriteString(m.renderPFHelpBar())
	} else {
		b.WriteString(m.renderDockerHelpBar())
	}
	if !m.inDiscovery && m.viewingService < 0 && m.viewingProcess == "" && !m.showStats {
		b.WriteString("\n\n")
	}
	if m.inDiscovery {
		b.WriteString("\n\n")
		b.WriteString(styles.InlineHelp(styles.HelpItem("esc", "cancel")))
	} else if m.viewingService >= 0 || m.viewingProcess != "" || m.showStats {
		// Config/stats overlay has its own "press any key to return" hint
	} else if m.inputtingEnv {
		b.WriteString(styles.BackHelp())
	} else {
		b.WriteString(styles.BackHelp(
			styles.HelpItem("↑ ↓", "navigate"),
			styles.HelpItem("← →", "envs"),
			styles.HelpItem("?", "help"),
		))
	}

	return b.String()
}

func (m *ProfileViewModel) renderEnvTabs() string {
	tabs := make([]string, len(m.profile.Environments))
	for i, env := range m.profile.Environments {
		if i == m.activeEnv {
			tabs[i] = styles.StyleBadgeEnv.Render(" " + env.Name + " ")
		} else {
			tabs[i] = styles.StyleBadgeEnvInactive.Render(" " + env.Name + " ")
		}
	}
	return "  " + strings.Join(tabs, "  ")
}

// serviceCardHeight returns the actual height (in terminal lines) of a
// Docker service card, accounting for variable port counts.
func serviceCardHeight(ports int) int {
	return 4 + ports // border(2) + name(1) + image(1) + ports(N)
}

// serviceWindow computes the visible window for Docker service cards,
// using actual per-card heights instead of a fixed estimate.
func (m *ProfileViewModel) serviceWindow(total int, heights []int) itemWindow {
	w := itemWindow{start: 0, end: total}
	if total == 0 || m.height <= 0 {
		return w
	}
	cursor := min(m.cursor, total-1) // clamp in case cursor is from a different view
	overhead := m.estimateItemOverhead()
	avail := m.height - overhead
	if avail <= 0 {
		return itemWindow{start: cursor, end: min(cursor+1, total), hasAbove: cursor > 0, hasBelow: total-cursor > 1}
	}

	// Try to center the cursor: consume items up/down until available space fills
	used := heights[cursor] // cursor always shown
	start := cursor
	end := cursor + 1

	// Expand downwards first (the direction the user naturally reads)
	for end < total {
		next := heights[end]
		if used+next > avail {
			break
		}
		used += next
		end++
	}

	// Expand upwards if space remains
	for start > 0 {
		prev := heights[start-1]
		if used+prev > avail {
			break
		}
		used += prev
		start--
	}

	return itemWindow{
		start:    start,
		end:      end,
		hasAbove: start > 0,
		hasBelow: total-end > 0,
	}
}

func forwardCardHeight(h *portforward.PortForwardHandle) int {
	height := 4
	if h.Error != "" && h.State == portforward.ForwardFailed {
		height++
	}
	return height
}

func (m *ProfileViewModel) renderServices(env *config.Environment) string {
	// Identity mapping: items appear in config order

	// Build heights: service cards + local process cards
	totalItems := len(env.Services) + len(env.LocalProcesses)
	heights := make([]int, totalItems)
	for i := range env.Services {
		heights[i] = serviceCardHeight(len(env.Services[i].Ports))
	}
	for i := range env.LocalProcesses {
		h := 4 // border(2) + name(1) + command(1)
		if env.LocalProcesses[i].WorkDir != "" {
			h = 5 // + workdir line
		}
		heights[len(env.Services)+i] = h
	}

	win := m.serviceWindow(totalItems, heights)

	// build status map
	statusMap := map[string]docker.ServiceStatus{}
	for _, s := range m.statuses {
		statusMap[s.Name] = s
	}

	cardW := m.width - 4
	if cardW < 40 {
		cardW = 40
	}
	if cardW > 80 {
		cardW = 80
	}

	var rows []string
	for i := win.start; i < win.end; i++ {
		if i < len(env.Services) {
			// Docker service card
			svc := env.Services[i]
			containerName := fmt.Sprintf("stackr-%s-%s-%s", m.profile.Name, env.Name, svc.ComposeName())
			st := statusMap[containerName]

			var dot string
			if op, pending := m.pendingOps[containerName]; pending {
				dot = m.renderSpinnerDot(op)
			} else if st.State == "" {
				dot = styles.Dot("stopped")
			} else {
				dot = styles.Dot(st.State)
			}

			name := styles.StyleHeader.Render(svc.Name)

			line1 := lipgloss.JoinHorizontal(lipgloss.Center, dot, "  ", name)
			line2 := "   " + styles.StyleCode.Render(svc.FullImage())

			var portLines []string
			for _, p := range svc.Ports {
				portLines = append(portLines, styles.StyleSubtitle.Render("     "+p.String()))
			}
			portBlock := strings.Join(portLines, "\n")

			var row string
			if len(portLines) > 0 {
				row = lipgloss.JoinVertical(lipgloss.Left, line1, line2, portBlock)
			} else {
				row = lipgloss.JoinVertical(lipgloss.Left, line1, line2)
			}

			if i == m.cursor {
				rows = append(rows, styles.StylePanelFocused.Width(cardW).Render(row))
			} else {
				rows = append(rows, styles.StylePanel.Width(cardW).Render(row))
			}
		} else {
			// Local process card
			procIdx := i - len(env.Services)
			proc := env.LocalProcesses[procIdx]
			running := m.isLocalProcRunning(proc.Name)

			var dot string
			if op, pending := m.pendingOps["proc:"+proc.Name]; pending {
				dot = m.renderSpinnerDot(op)
			} else {
				dot = styles.LocalProcessDot(running)
			}
			name := styles.StyleHeader.Render(proc.Name)

			line1 := lipgloss.JoinHorizontal(lipgloss.Center, dot, "  ", name)
			line2 := "   " + styles.StyleCode.Render(proc.Command)

			var row string
			if proc.WorkDir != "" {
				line3 := "     " + styles.StyleSubtitle.Render("dir: "+proc.WorkDir)
				row = lipgloss.JoinVertical(lipgloss.Left, line1, line2, line3)
			} else {
				row = lipgloss.JoinVertical(lipgloss.Left, line1, line2)
			}

			if i == m.cursor {
				rows = append(rows, styles.StylePanelFocused.Width(cardW).Render(row))
			} else {
				rows = append(rows, styles.StylePanel.Width(cardW).Render(row))
			}
		}
	}

	if len(env.Services) == 0 && len(env.LocalProcesses) == 0 {
		return styles.StyleSubtitle.Render("  No services yet. Press 'a' to add one.")
	}

	result := strings.Join(rows, "\n")
	if win.hasAbove || win.hasBelow {
		result = m.decorateScroll(result, win, totalItems)
	}
	return result + "\n"
}

// ── Viewport helpers ──────────────────────────────────────────────────────────

type itemWindow struct {
	start, end int
	hasAbove   bool
	hasBelow   bool
}

// estimateItemOverhead returns the number of terminal lines consumed by
// everything rendered before and after the dynamic items section in View().
func (m *ProfileViewModel) estimateItemOverhead() int {
	// Before items
	oh := 1 + 1 + 2 // \n + env tabs + \n\n

	if m.isPortForwardEnv() {
		if ctx := m.knownForwardCtx(); ctx != "" {
			oh += 4 // \n + context line + \n\n
		}
	}

	// After items: newline + help bar + newline + bottom hints
	if !m.inDiscovery {
		oh += 2 // blank line before help bar (only when help bar is rendered)
		if m.isPortForwardEnv() {
			oh += 6 // renderPFHelpBar height (border 2 + label/blank 2 + max items 2)
		} else {
			oh += 6 // renderDockerHelpBar height (border 2 + label/blank 2 + max items 2)
		}
	}
	oh += 3 // \n\n after help bar + bottom hints

	// Optional status lines (armed states, input mode)
	if m.deleteArmed || m.deleteEnvArmed || m.resetArmed {
		oh += 1 // confirmation prompt
	}
	if m.inputtingEnv {
		oh += 4 // env input text
	}

	return oh
}

// shortCtxName extracts the cluster name from a K8s context ARN or path.
// "arn:aws:eks:eu-west-1:177734401659:cluster/EksTicketDevV1" → "EksTicketDevV1"
func shortCtxName(ctx string) string {
	if idx := strings.LastIndex(ctx, "cluster/"); idx >= 0 {
		return ctx[idx+8:]
	}
	parts := strings.Split(ctx, "/")
	return parts[len(parts)-1]
}

func renderDiscoveryItem(label string, selected bool) string {
	if selected {
		return styles.StyleDiscoveryItemSelected.Render("> " + label)
	}
	return styles.StyleDiscoveryItem.Render("  " + label)
}

// decorateScroll wraps rendered items with scroll indicators.
// "↑ N more" takes space above items, so we skip it for a single hidden item.
// "↓ N more" is at the bottom and doesn't push anything — always show it.
func (m *ProfileViewModel) decorateScroll(content string, win itemWindow, total int) string {
	var b strings.Builder
	if above := win.start; above > 0 {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", above)))
		b.WriteString("\n")
	}
	b.WriteString(content)
	if below := total - win.end; below > 0 {
		b.WriteString("\n")
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↓ %d more", below)))
	}
	return b.String()
}

func (m *ProfileViewModel) renderPortForwards() string {
	forwards := m.currentForwards()
	if len(forwards) == 0 {
		return styles.StyleSubtitle.Render("  No port-forwards configured. Press 'k' to discover K8s services.") + "\n"
	}

	heights := make([]int, len(forwards))
	for i := range forwards {
		heights[i] = forwardCardHeight(forwards[i])
	}
	win := m.serviceWindow(len(forwards), heights)

	cardW := m.width - 4 // 2 left margin + 2 right margin
	if cardW < 40 {
		cardW = 40
	}
	if cardW > 80 {
		cardW = 80 // max width for readability
	}

	var rows []string
	for i := win.start; i < win.end; i++ {
		h := forwards[i]
		fwd := h.Config

	// Line 1: state dot + name
	var stateLine string
	if op, pending := m.pendingOps["pf:"+fwd.Name]; pending {
		stateLine = m.renderSpinnerDot(op)
	} else {
		stateLine = styles.ForwardDot(h.State)
	}
	name := styles.StyleHeader.Render(fwd.Name)
		line1 := lipgloss.JoinHorizontal(lipgloss.Center, stateLine, "  ", name)

		// Line 2: local:remote  →  namespace/service
		addr := fmt.Sprintf("  %d:%d  →  %s/%s", fwd.LocalPort, fwd.RemotePort, fwd.K8sNamespace, fwd.K8sService)
		addrLine := styles.StyleSubtitle.Render(addr)

		// Error line (only for failed)
		row := lipgloss.JoinVertical(lipgloss.Left, line1, addrLine)
		if h.Error != "" && h.State == portforward.ForwardFailed {
			row = lipgloss.JoinVertical(lipgloss.Left, line1, addrLine, styles.StyleError.Render("     "+h.Error))
		}

		if i == m.cursor {
			if h.State == portforward.ForwardFailed {
				rows = append(rows, styles.StylePanelError.Width(cardW).Render(row))
			} else {
				rows = append(rows, styles.StylePanelFocused.Width(cardW).Render(row))
			}
		} else {
			rows = append(rows, styles.StylePanel.Width(cardW).Render(row))
		}
	}

	result := strings.Join(rows, "\n")
	if win.hasAbove || win.hasBelow {
		result = m.decorateScroll(result, win, len(forwards))
	}
	return result + "\n"
}

// discoveryItemWindow calculates the visible window for discovery list items.
// The discovery body has a fixed 4-line overhead (header + blanks + inline help),
// each item is 1 plain text line, and scroll indicators consume 1 line each when the
// window is clipped above or below.
func (m *ProfileViewModel) discoveryItemWindow(total int) itemWindow {
	w := itemWindow{start: 0, end: total}
	if total == 0 || m.height <= 0 {
		return w
	}

	cursor := min(m.discoveryCursor, total-1) // clamp for safety

	oh := m.estimateItemOverhead()
	stepOh := 5 // header + blanks + inline help (includes blank line before help)
	itemH := 1  // plain text line (no panel)
	avail := m.height - oh - stepOh
	if avail <= 0 {
		return itemWindow{start: cursor, end: min(cursor+1, total), hasAbove: cursor > 0, hasBelow: total-cursor > 1}
	}

	fit := avail / itemH
	if fit <= 0 {
		fit = 1
	}

	if total <= fit {
		return w
	}

	for fit > 0 {
		// Center around cursor.
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

		extra := 0
		if start > 0 {
			extra++
		}
		if end < total {
			extra++
		}

		if fit*itemH+extra <= avail || fit == 1 {
			return itemWindow{
				start:    start,
				end:      end,
				hasAbove: start > 0,
				hasBelow: total-end > 0,
			}
		}

		fit--
	}

	return itemWindow{start: cursor, end: min(cursor+1, total), hasAbove: cursor > 0, hasBelow: total-cursor > 1}
}

func (m *ProfileViewModel) renderDiscovery() string {
	var b strings.Builder

	switch m.discoveryStep {
	case 0:
		dTotal := len(m.discoveryCtxs)
		b.WriteString(styles.StyleHeader.Render("  Select K8s context:"))
		b.WriteString("\n\n")
		if dTotal == 0 {
			b.WriteString(m.renderSpinnerDot("Loading contexts..."))
			b.WriteString("\n")
			break
		}
		win := m.discoveryItemWindow(dTotal)
		var rows []string
		if above := win.start; above > 0 {
			rows = append(rows, styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", above)))
		}
		for i := win.start; i < win.end; i++ {
			ctx := m.discoveryCtxs[i]
			rows = append(rows, renderDiscoveryItem(ctx, i == m.discoveryCursor))
		}
		if below := dTotal - win.end; below > 0 {
			rows = append(rows, styles.StyleSubtitle.Render(fmt.Sprintf("  ↓ %d more", below)))
		}
		b.WriteString(strings.Join(rows, "\n"))
		b.WriteString("\n\n" + styles.InlineHelp(styles.HelpItem("↑ ↓", "navigate"), styles.HelpItem("↵", "select")))

	case 1:
		dTotal := len(m.discoveryNSs)
		b.WriteString(styles.StyleHeader.Render("  Select namespace for '" + shortCtxName(m.discoverySelCtx) + "':"))
		b.WriteString("\n\n")
		if dTotal == 0 {
			b.WriteString(m.renderSpinnerDot("Loading namespaces..."))
			b.WriteString("\n")
			break
		}
		win := m.discoveryItemWindow(dTotal)
		var rows []string
		if above := win.start; above > 0 {
			rows = append(rows, styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", above)))
		}
		for i := win.start; i < win.end; i++ {
			ns := m.discoveryNSs[i]
			rows = append(rows, renderDiscoveryItem(ns, i == m.discoveryCursor))
		}
		if below := dTotal - win.end; below > 0 {
			rows = append(rows, styles.StyleSubtitle.Render(fmt.Sprintf("  ↓ %d more", below)))
		}
		b.WriteString(strings.Join(rows, "\n"))
		b.WriteString("\n\n" + styles.InlineHelp(styles.HelpItem("↑ ↓", "navigate"), styles.HelpItem("↵", "select")))

	case 2:
		dTotal := len(m.discoverySvcs)
		b.WriteString(styles.StyleHeader.Render("  Select service in '" + shortCtxName(m.discoverySelCtx) + "/" + m.discoverySelNS + "':"))
		b.WriteString("\n\n")
		if dTotal == 0 {
			b.WriteString(m.renderSpinnerDot("Loading services..."))
			b.WriteString("\n")
			break
		}
		win := m.discoveryItemWindow(dTotal)
		var rows []string
		if above := win.start; above > 0 {
			rows = append(rows, styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", above)))
		}
		for i := win.start; i < win.end; i++ {
			svc := m.discoverySvcs[i]
			label := fmt.Sprintf("%s  %d/TCP", svc.Name, svc.RemotePort)
			rows = append(rows, renderDiscoveryItem(label, i == m.discoveryCursor))
		}
		if below := dTotal - win.end; below > 0 {
			rows = append(rows, styles.StyleSubtitle.Render(fmt.Sprintf("  ↓ %d more", below)))
		}
		b.WriteString(strings.Join(rows, "\n"))
		b.WriteString("\n\n" + styles.InlineHelp(styles.HelpItem("↑ ↓", "navigate"), styles.HelpItem("↵", "select")))

	case 3:
		b.WriteString(styles.StyleHeader.Render("  Confirm port-forward:"))
		b.WriteString("\n\n")
		b.WriteString("  Service:  " + styles.StyleCode.Render(m.discoveredPF.Name) + "\n")
		b.WriteString("  Remote:   " + styles.StyleCode.Render(fmt.Sprintf("%d", m.discoveredPF.RemotePort)) + "\n")
		b.WriteString("  Local:    " + m.portInput.View() + "\n")
		b.WriteString("\n\n" + styles.InlineHelp(styles.HelpItem("↵", "confirm")))
	}

	return b.String()
}

func (m *ProfileViewModel) renderPortEditor() string {
	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	return "  " + focusStyle.Render("Local port:") +
		" " + m.portInput.View() +
		"  " + styles.InlineHelp(styles.HelpItem("↵", "save"), styles.HelpItem("esc", "cancel"))
}

func (m *ProfileViewModel) renderDockerHelpBar() string {
	svcs := m.currentEnvServices()
	hasRunning := false
	for _, st := range m.statuses {
		if st.State == "running" {
			hasRunning = true
			break
		}
	}

	var envItems, svcItems, stackItems []styles.HelpItemResult
	envItems = append(envItems, styles.HelpItem("n", "new env"))
	if len(m.profile.Environments) > 0 && m.currentEnv() != nil && m.currentEnv().Name != "local" {
		envItems = append(envItems, styles.HelpItem("D D", "delete env"))
	}
	envItems = append(envItems, styles.HelpItem("a", "add service"))

	if len(svcs) > 0 || len(m.currentEnv().LocalProcesses) > 0 {
		svcItems = append(svcItems, styles.HelpItem("↵", "toggle"))
		if m.selectedDBType() != "" {
			svcItems = append(svcItems, styles.HelpItem("o", "db ops"))
		}
		if len(svcs) > 1 {
			svcItems = append(svcItems, styles.HelpItem("d d", "delete"))
		}
		svcItems = append(svcItems, styles.HelpItem("v", "view"))
		if len(svcs) > 0 && m.cursor < len(svcs) {
			svcItems = append(svcItems, styles.HelpItem("p", "pull"))
		}
		if hasRunning && m.isSelectedServiceRunning() {
			svcItems = append(svcItems, styles.HelpItem("h", "shell"))
			svcItems = append(svcItems, styles.HelpItem("t", "stats"))
		}
		stackItems = append(stackItems, styles.HelpItem("s", "stack all"))
		if hasRunning {
			stackItems = append(stackItems, styles.HelpItem("x", "stop all"))
		}
		stackItems = append(stackItems, styles.HelpItem("r r", "reset all"))
		stackItems = append(stackItems, styles.HelpItem("r v", "reset vols"))
	}

	return styles.HelpBar(
		styles.HelpGroup{Label: "Environment", Items: envItems},
		styles.HelpGroup{Label: "Service", Items: svcItems},
		styles.HelpGroup{Label: "Stack", Items: stackItems},
	)
}

func (m *ProfileViewModel) renderPFHelpBar() string {
	var envItems, pfItems, stackItems []styles.HelpItemResult
	envItems = append(envItems, styles.HelpItem("n", "new env"))
	if len(m.profile.Environments) > 0 && m.currentEnv() != nil && m.currentEnv().Name != "local" {
		envItems = append(envItems, styles.HelpItem("D D", "delete env"))
	}

	pfItems = append(pfItems, styles.HelpItem("k", "k8s discover"))
	forwards := m.currentForwards()
	if len(forwards) > 0 {
		pfItems = append(pfItems, styles.HelpItem("↵", "toggle"))
		pfItems = append(pfItems, styles.HelpItem("d d", "delete"))
		pfItems = append(pfItems, styles.HelpItem("e", "edit port"))

		hasRunning := false
		for _, h := range forwards {
			if h.State == portforward.ForwardRunning || h.State == portforward.ForwardStarting {
				hasRunning = true
				break
			}
		}
		stackItems = append(stackItems, styles.HelpItem("s", "start all"))
		if hasRunning {
			stackItems = append(stackItems, styles.HelpItem("x", "stop all"))
		}
		stackItems = append(stackItems, styles.HelpItem("r r", "reset all"))
	}

	return styles.HelpBar(
		styles.HelpGroup{Label: "Environment", Items: envItems},
		styles.HelpGroup{Label: "PortForward", Items: pfItems},
		styles.HelpGroup{Label: "Stack", Items: stackItems},
	)
}

// ── Config Panel ──────────────────────────────────────────────────────────

func (m *ProfileViewModel) renderConfigPanel(svc config.Service) string {
	cardW := m.width - 4
	if cardW < 40 {
		cardW = 40
	}
	if cardW > 80 {
		cardW = 80
	}

	var lines []string
	lines = append(lines, styles.StyleHeader.Render(svc.Name))
	lines = append(lines, "  "+styles.StyleCode.Render(svc.FullImage()))
	lines = append(lines, "")

	lines = append(lines, styles.StyleInputLabel.Render("Ports"))
	if len(svc.Ports) > 0 {
		for _, p := range svc.Ports {
			lines = append(lines, styles.StyleSubtitle.Render("  "+p.String()))
		}
	} else {
		lines = append(lines, styles.StyleSubtitle.Render("  (none)"))
	}
	lines = append(lines, "")

	lines = append(lines, styles.StyleInputLabel.Render("Env Vars"))
	if len(svc.EnvVars) > 0 {
		for _, ev := range svc.EnvVars {
			lines = append(lines, styles.StyleSubtitle.Render("  "+ev.Key+"="+ev.Value))
		}
	} else {
		lines = append(lines, styles.StyleSubtitle.Render("  (none)"))
	}
	lines = append(lines, "")

	lines = append(lines, styles.StyleInputLabel.Render("Volumes"))
	if len(svc.Volumes) > 0 {
		for _, v := range svc.Volumes {
			lines = append(lines, styles.StyleSubtitle.Render("  "+v.Source+" → "+v.Target))
		}
	} else {
		lines = append(lines, styles.StyleSubtitle.Render("  (none)"))
	}

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return styles.StylePanelFocused.Width(cardW).Render(content) + "\n\n  press any key to return"
}

func (m *ProfileViewModel) renderLocalProcPanel(proc config.LocalProcess) string {
	cardW := m.width - 4
	if cardW < 40 {
		cardW = 40
	}
	if cardW > 80 {
		cardW = 80
	}

	var lines []string
	lines = append(lines, styles.StyleHeader.Render(proc.Name))
	lines = append(lines, "  "+styles.StyleCode.Render(proc.Command))
	lines = append(lines, "")

	lines = append(lines, styles.StyleInputLabel.Render("Work Dir"))
	if proc.WorkDir != "" {
		lines = append(lines, styles.StyleSubtitle.Render("  "+proc.WorkDir))
	} else {
		lines = append(lines, styles.StyleSubtitle.Render("  (default)"))
	}
	lines = append(lines, "")

	lines = append(lines, styles.StyleInputLabel.Render("Type"))
	lines = append(lines, styles.StyleSubtitle.Render("  Host"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return styles.StylePanelFocused.Width(cardW).Render(content) + "\n\n  press any key to return"
}

// ── Shortcuts Reference ───────────────────────────────────────────────────

func (m *ProfileViewModel) renderShortcuts() string {
	cardW := m.width - 4
	if cardW < 40 {
		cardW = 40
	}
	if cardW > 80 {
		cardW = 80
	}

	type pair struct{ k, v string }
	col := func(title string, items []pair) string {
		var lines []string
		lines = append(lines, styles.StyleInputLabel.Render(title))
		for _, it := range items {
			lines = append(lines, styles.StyleSubtitle.Render(fmt.Sprintf("  %-5s %s", it.k, it.v)))
		}
		return lipgloss.JoinVertical(lipgloss.Left, lines...)
	}

	nav := col("Navigation", []pair{
		{"← →", "switch env"},
		{"↑ ↓", "navigate"},
		{"enter", "open / toggle"},
		{"esc", "back"},
	})

	if m.isPortForwardEnv() {
		pf := col("Port-Forward", []pair{
			{"↵", "toggle"},
			{"d d", "delete"},
			{"e", "edit port"},
			{"k", "discover"},
		})

		envCol := col("Env / Stack", []pair{
			{"s", "start all"},
			{"x", "stop all"},
			{"r r", "reset all"},
			{"n", "add env"},
			{"D D", "delete env"},
		})

		pad := "   "
		row1 := lipgloss.JoinHorizontal(lipgloss.Top, nav, pad, pf)
		row2 := lipgloss.JoinHorizontal(lipgloss.Top, envCol)
		content := lipgloss.JoinVertical(lipgloss.Left, row1, "", row2)
		return styles.StylePanelFocused.Width(cardW).Render(content) + "\n\n  press any key to return"
	}

	svc := col("Services", []pair{
		{"a", "add service"},
		{"d d", "delete svc"},
		{"o", "db ops"},
		{"v", "view config"},
		{"t", "stats"},
		{"p", "pull"},
		{"h", "shell"},
	})

	envCol := col("Env / Stack", []pair{
		{"s", "start all"},
		{"x", "stop all"},
		{"r r", "reset all"},
		{"r v", "reset volumes"},
		{"n", "add env"},
		{"D D", "delete env"},
	})

	pad := "   "
	row1 := lipgloss.JoinHorizontal(lipgloss.Top, nav, pad, svc)
	row2 := lipgloss.JoinHorizontal(lipgloss.Top, envCol)
	content := lipgloss.JoinVertical(lipgloss.Left, row1, "", row2)

	return styles.StylePanelFocused.Width(cardW).Render(content) + "\n\n  press any key to return"
}

// ── Discovery Key Handler ──────────────────────────────────────────────────

func (m *ProfileViewModel) handleDiscoveryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ── Step 3: text input mode for port confirmation ──
	// Handled separately so backspace edits the port instead of cancelling.
	if m.discoveryStep == 3 {
		switch {
		case msg.String() == "esc":
			m.portInput.Blur()
			m.discoveryStep = 2
			m.discoveredPF = nil
			return m, nil

		case key.Matches(msg, profileKeys.enter):
			portStr := strings.TrimSpace(m.portInput.Value())
			port, err := strconv.Atoi(portStr)
			if err != nil || port < 1 || port > 65535 {
				return m, nil
			}
			// Check port conflict
			for _, h := range m.currentForwards() {
				if h.Config.LocalPort == port {
					return m, nil
				}
			}

			envName := m.profile.Environments[m.activeEnv].Name
			m.discoveredPF.LocalPort = port
			pf := *m.discoveredPF

			updated, err := m.cfg.AddPortForward(m.profile.Name, envName, pf)
			if err == nil {
				m.profile = updated
				m.buildForwardHandles()
			}

			m.portInput.Blur()
			m.inDiscovery = false
			m.discoveryStep = 0
			m.discoveryCtxs = nil
			m.discoveryNSs = nil
			m.discoverySvcs = nil
			m.discoveredPF = nil
			m.discoveryCursor = 0
			return m, m.loadStatus()

		default:
			var cmd tea.Cmd
			m.portInput, cmd = m.portInput.Update(msg)
			return m, cmd
		}
	}

	// ── Steps 0-2: navigation mode ──
	switch {
	case key.Matches(msg, profileKeys.back):
		if m.discoveryStep > 1 {
			m.discoveryStep--
			// Restore cursor from previous step
			switch m.discoveryStep {
			case 1:
				m.discoveryCursor = m.discoveryNSCur
			case 2:
				m.discoveryCursor = m.discoverySvcCur
			}
			return m, nil
		}
		m.inDiscovery = false
		m.discoveryStep = 0
		m.discoveryCtxs = nil
		m.discoveryNSs = nil
		m.discoverySvcs = nil
		m.discoveredPF = nil
		m.discoveryCursor = 0
		return m, nil

	case key.Matches(msg, profileKeys.up):
		if m.discoveryCursor > 0 {
			m.discoveryCursor--
		}

	case key.Matches(msg, profileKeys.down):
		switch m.discoveryStep {
		case 0:
			if m.discoveryCursor < len(m.discoveryCtxs)-1 {
				m.discoveryCursor++
			}
		case 1:
			if m.discoveryCursor < len(m.discoveryNSs)-1 {
				m.discoveryCursor++
			}
		case 2:
			if m.discoveryCursor < len(m.discoverySvcs)-1 {
				m.discoveryCursor++
			}
		}

	case key.Matches(msg, profileKeys.enter):
		switch m.discoveryStep {
		case 0:
			if m.discoveryCursor < len(m.discoveryCtxs) {
				m.discoverySelCtx = m.discoveryCtxs[m.discoveryCursor]
				m.discoverySelNS = ""
				m.discoveryNSs = nil
				m.discoverySvcs = nil
				m.discoveryStep = 1
				m.discoveryCursor = 0
				m.pendingOps["discovery"] = "loading"
				return m, tea.Batch(
					func() tea.Msg {
						nss, err := portforward.DiscoverNamespaces(m.discoverySelCtx)
						if err != nil {
							return discoveryErrorMsg{err: err.Error()}
						}
						if len(nss) == 0 {
							return discoveryErrorMsg{err: "no namespaces found in context " + m.discoverySelCtx}
						}
						return discoveryNamespacesMsg{namespaces: nss}
					},
					spinnerCmd(),
				)
			}

		case 1:
			if m.discoveryCursor < len(m.discoveryNSs) {
				m.discoverySelNS = m.discoveryNSs[m.discoveryCursor]
				m.discoveryNSCur = m.discoveryCursor
				m.discoverySvcs = nil
				m.discoveryStep = 2
				m.discoveryCursor = 0
				m.discoverySvcCur = 0
				m.pendingOps["discovery"] = "loading"
				return m, tea.Batch(
					func() tea.Msg {
						svcs, err := portforward.DiscoverServices(m.discoverySelCtx, m.discoverySelNS)
						if err != nil {
							return discoveryErrorMsg{err: err.Error()}
						}
						if len(svcs) == 0 {
							return discoveryErrorMsg{err: "no services found in " + m.discoverySelCtx + "/" + m.discoverySelNS}
						}
						return discoveryServicesMsg{services: svcs}
					},
					spinnerCmd(),
				)
			}

		case 2:
			if m.discoveryCursor < len(m.discoverySvcs) {
				svc := m.discoverySvcs[m.discoveryCursor]
				m.discoverySvcCur = m.discoveryCursor
				m.discoveredPF = &svc
				nextPort := portforward.AssignPort(8080, m.currentForwards())
				m.discoveredPF.LocalPort = nextPort
				m.portInput.SetValue(fmt.Sprintf("%d", nextPort))
				m.portInput.Focus()
				m.discoveryStep = 3
				return m, textinput.Blink
			}
		}
	}

	return m, nil
}

// ── Forward Toggle ─────────────────────────────────────────────────────────

func (m *ProfileViewModel) handleForwardToggle() (tea.Model, tea.Cmd) {
	forwards := m.currentForwards()
	if m.cursor >= len(forwards) {
		return m, nil
	}
	h := forwards[m.selectedForwardIndex()]

	if h.State == portforward.ForwardRunning || h.State == portforward.ForwardStarting {
		m.pendingOps["pf:"+h.Config.Name] = "stop"
		return m, tea.Batch(
			func() tea.Msg {
				h.Stop()
				return m.refreshStatus()
			},
			m.loadStatus(),
			func() tea.Msg { return spinnerTickMsg{} },
		)
	}

	// Start (Stopped or Failed)
	m.pendingOps["pf:"+h.Config.Name] = "start"
	return m, tea.Batch(
		func() tea.Msg {
			h.Start()
			return m.refreshStatus()
		},
		m.loadStatus(),
		func() tea.Msg { return spinnerTickMsg{} },
	)
}

// ── Forward Delete ─────────────────────────────────────────────────────────

func (m *ProfileViewModel) handleForwardDelete() (tea.Model, tea.Cmd) {
	m.clampCursor()
	forwards := m.currentForwards()
	if m.cursor >= len(forwards) {
		return m, nil
	}
	if !m.deleteArmed {
		m.deleteArmed = true
		return m, nil
	}
	m.deleteArmed = false
	h := forwards[m.selectedForwardIndex()]
	fwdName := h.Config.Name
	envName := m.profile.Environments[m.activeEnv].Name

	// Stop if running
	_ = h.Stop()

	// Delete from config
	updated, err := m.cfg.DeletePortForward(m.profile.Name, envName, fwdName)
	if err != nil {
		return m, m.loadStatus()
	}
	m.profile = updated
	m.buildForwardHandles()
	m.clampCursor()
	return m, m.loadStatus()
}

// ── Port Editing ──────────────────────────────────────────────────────────

func (m *ProfileViewModel) handlePortEditStart() (tea.Model, tea.Cmd) {
	m.clampCursor()
	forwards := m.currentForwards()
	if m.cursor >= len(forwards) {
		return m, nil
	}
	m.editingPort = true
	m.editFwdIdx = m.selectedForwardIndex()
	h := forwards[m.editFwdIdx]
	m.portInput.SetValue(fmt.Sprintf("%d", h.Config.LocalPort))
	m.portInput.Focus()
	return m, textinput.Blink
}

func (m *ProfileViewModel) handlePortEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "esc":
		m.editingPort = false
		m.portInput.Blur()
		m.portInput.SetValue("")
		return m, nil

	case key.Matches(msg, profileKeys.enter):
		portStr := strings.TrimSpace(m.portInput.Value())
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return m, nil
		}
		// Check port conflict with other forwards
		fwdName := m.currentForwards()[m.editFwdIdx].Config.Name
		for i, h := range m.currentForwards() {
			if i != m.editFwdIdx && h.Config.LocalPort == port {
				return m, nil
			}
		}
		// Update config
		envName := m.profile.Environments[m.activeEnv].Name
		h := m.currentForwards()[m.editFwdIdx]
		updatedPF := h.Config
		updatedPF.LocalPort = port
		wasRunning := h.State == portforward.ForwardRunning
		if wasRunning {
			_ = h.Stop()
		}
		updated, err := m.cfg.UpdatePortForward(m.profile.Name, envName, fwdName, updatedPF)
		if err != nil {
			return m, nil
		}
		m.profile = updated
		m.buildForwardHandles()
		if wasRunning {
			// Restart with new port
			newHandle := m.currentForwards()[m.editFwdIdx]
			go func() { _ = newHandle.Start() }()
		}
		m.editingPort = false
		m.portInput.Blur()
		m.portInput.SetValue("")
		return m, nil

	default:
		var cmd tea.Cmd
		m.portInput, cmd = m.portInput.Update(msg)
		return m, cmd
	}
}

// wrapText word-wraps text to the given max width, preserving line breaks.
func wrapText(s string, maxWidth int) string {
	if maxWidth < 10 {
		maxWidth = 76 // fallback if terminal width unknown
	}
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		for len(line) > maxWidth {
			// Find last space within maxWidth
			idx := strings.LastIndex(line[:maxWidth], " ")
			if idx < 0 {
				idx = maxWidth
			}
			b.WriteString(line[:idx])
			b.WriteString("\n")
			line = strings.TrimLeft(line[idx:], " ")
		}
		b.WriteString(line)
	}
	return b.String()
}

// ── Stats Panel ────────────────────────────────────────────────────────────

func (m *ProfileViewModel) renderStats() string {
	cardW := m.width - 4
	if cardW < 40 {
		cardW = 40
	}
	if cardW > 80 {
		cardW = 80
	}

	var lines []string
	lines = append(lines, styles.StyleHeader.Render("Docker Stats"))
	lines = append(lines, styles.StyleSubtitle.Render(m.statsContainer))
	lines = append(lines, "")

	if strings.HasPrefix(m.statsData, "Loading") || strings.HasPrefix(m.statsData, "unavailable") {
		lines = append(lines, styles.StyleSubtitle.Render(m.statsData))
	} else {
		parts := strings.SplitN(m.statsData, "|", 4)
		if len(parts) == 4 {
			lines = append(lines, styles.StyleInputLabel.Render("  CPU:      ")+styles.StyleSubtitle.Render(parts[0]))
			lines = append(lines, styles.StyleInputLabel.Render("  Memory:   ")+styles.StyleSubtitle.Render(parts[1]))
			lines = append(lines, styles.StyleInputLabel.Render("  Net I/O:  ")+styles.StyleSubtitle.Render(parts[2]))
			lines = append(lines, styles.StyleInputLabel.Render("  Block I/O:")+styles.StyleSubtitle.Render(parts[3]))
		} else {
			lines = append(lines, styles.StyleError.Render("Error parsing stats"))
		}
	}

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return styles.StylePanelFocused.Width(cardW).Render(content) + "\n\n  press any key to dismiss"
}
