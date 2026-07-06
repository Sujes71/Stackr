package views

// Shared helpers for DBOps, QueueOps, and MockOps views.
// Extracted from dbops.go, queueops.go, and mockops.go to reduce duplication.

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/docker"
	"github.com/stackr/stackr/internal/tui/styles"
)

// ── Shared key bindings ──────────────────────────────────────────────────────

var (
	opsKeyUp     = key.NewBinding(key.WithKeys("up"))
	opsKeyDown   = key.NewBinding(key.WithKeys("down"))
	opsKeyEnter  = key.NewBinding(key.WithKeys("enter"))
	opsKeyBack   = key.NewBinding(key.WithKeys("esc"))
	opsKeyAdd    = key.NewBinding(key.WithKeys("a"))
	opsKeyEdit   = key.NewBinding(key.WithKeys("e"))
	opsKeyDelete = key.NewBinding(key.WithKeys("d"))
)

// ── Shared types ─────────────────────────────────────────────────────────────

// actionItem describes a single action in the actions screen.
type actionItem struct {
	label             string
	group             string // panel group label: "Export", "Import", "Functions", etc.
	enabled           bool
	disabledReason    string
	execute           tea.Cmd
	isCustom          bool
	isAddAction       bool
	customIdx         int  // index into CustomActions (valid when isCustom)
	requiresContainer bool // true when the action needs the local container running
}

// serviceStartedMsg is sent when docker.UpService completes for the prompt flow.
type serviceStartedMsg struct {
	err error
}

// daemonStartedMsg is sent when Docker daemon launch completes.
type daemonStartedMsg struct {
	available bool
}

// readyRetryMsg is sent on each retry while waiting for the container to be ready.
type readyRetryMsg struct {
	label string
	retry int
}

// ── Container / Docker helpers ────────────────────────────────────────────────

// opsIsContainerRunning checks if the given container is running via docker status.
func opsIsContainerRunning(profileName, envName, container string) bool {
	statuses, err := docker.Status(profileName, envName)
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

// opsLaunchDockerCmd tries to start the Docker daemon via Colima or Docker Desktop.
func opsLaunchDockerCmd() tea.Cmd {
	return func() tea.Msg {
		if _, err := exec.LookPath("colima"); err == nil {
			_ = exec.Command("colima", "start").Run()
		} else {
			_ = exec.Command("open", "-a", "Docker").Run()
		}
		time.Sleep(2 * time.Second)
		return daemonStartedMsg{available: docker.DaemonAvailable()}
	}
}

// ── Environment / Service helpers ─────────────────────────────────────────────

// opsCurrentEnv finds a sync env by name in a profile.
func opsCurrentEnv(profile *config.Profile, envName string) *config.Environment {
	for i := range profile.Environments {
		if profile.Environments[i].Name == envName {
			return &profile.Environments[i]
		}
	}
	return nil
}

// opsCurrentService finds a service by name in an environment, updating serviceIdx on match.
func opsCurrentService(env *config.Environment, serviceName string, serviceIdx *int) *config.Service {
	if env == nil {
		return nil
	}
	if *serviceIdx >= 0 && *serviceIdx < len(env.Services) {
		svc := &env.Services[*serviceIdx]
		if svc.Name == serviceName || svc.ComposeName() == serviceName {
			return svc
		}
	}
	for i := range env.Services {
		svc := &env.Services[i]
		if svc.Name == serviceName || svc.ComposeName() == serviceName {
			*serviceIdx = i
			return svc
		}
	}
	return nil
}

// ── Action helpers ────────────────────────────────────────────────────────────

// opsFirstEnabledAction returns the index of the first enabled action, or 0 if all are disabled.
func opsFirstEnabledAction(actions []actionItem) int {
	for i, act := range actions {
		if act.enabled {
			return i
		}
	}
	return 0
}

// ── Spinner helpers ───────────────────────────────────────────────────────────

// spinnerFrames is the Braille spinner animation sequence shared by all views.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerCmd returns a tea.Cmd that fires a SpinnerTickMsg to kick off the
// spinner loop. After this initial tick, the SpinnerTickMsg handler in each
// view uses tea.Tick to keep the spinner running.
func spinnerCmd() tea.Cmd {
	return func() tea.Msg { return SpinnerTickMsg{} }
}

// opsRenderSpinnerDot renders a spinner frame with an operation label.
func opsRenderSpinnerDot(spinnerFrame int, op string) string {
	frame := spinnerFrames[spinnerFrame%len(spinnerFrames)]
	return lipgloss.NewStyle().Foreground(styles.ColorAccent).Render(fmt.Sprintf("%s %s", frame, op))
}

// ── Error formatting ──────────────────────────────────────────────────────────

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

// ── Param extraction ──────────────────────────────────────────────────────────

var paramRe = regexp.MustCompile(`\{(\w+)\}`)

// autoVarNames are built-in placeholders replaced automatically (not prompted).
var autoVarNames = map[string]bool{
	"host": true, "user": true, "password": true,
	"database": true, "db": true, "env": true,
	"local_user": true, "local_pass": true, "local_db": true,
}

func extractParams(cmd string) []string {
	matches := paramRe.FindAllStringSubmatch(cmd, -1)
	var params []string
	seen := map[string]bool{}
	for _, m := range matches {
		name := m[1]
		if !seen[name] && !autoVarNames[name] {
			seen[name] = true
			params = append(params, name)
		}
	}
	return params
}

// ── Misc helpers ──────────────────────────────────────────────────────────────

// disabledIf returns reason when cond is true, or empty string when false.
func disabledIf(cond bool, reason string) string {
	if cond {
		return reason
	}
	return ""
}

// ── Windowing helper ──────────────────────────────────────────────────────────

// itemWindow is defined in profile_view.go and shared across all list views.

// opsWindow calculates the visible item range for a cursor-based list.
// total is the number of items, cursor is the current position, itemHeight is
// the estimated lines per item, height is the available viewport height, and
// overhead is the number of lines consumed by chrome (title, help, etc.).
// The returned window keeps the cursor centered when possible.
func opsWindow(total, cursor, itemHeight, height, overhead int) itemWindow {
	if total == 0 || height <= 0 {
		return itemWindow{}
	}
	avail := height - overhead
	if avail <= 0 {
		return itemWindow{
			start:    cursor,
			end:      min(cursor+1, total),
			hasAbove: cursor > 0,
			hasBelow: cursor+1 < total,
		}
	}
	if itemHeight < 1 {
		itemHeight = 1
	}
	fit := avail / itemHeight
	if fit <= 0 {
		fit = 1
	}
	if total <= fit {
		return itemWindow{start: 0, end: total}
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

// ── Container readiness helper ────────────────────────────────────────────────

// opsContainerStatus represents the result of checking whether the service
// container is running and ready for action execution.
type opsContainerStatus struct {
	Running      bool
	NeedsDaemon  bool
	NeedsService bool
}

// opsCheckContainer checks whether the service container is running.
// If it is running, status.Running is true. If it is not running and the
// Docker daemon is unavailable, status.NeedsDaemon is true. Otherwise
// status.NeedsService is true. Callers set their own prompt fields from
// the returned status, keeping the shared logic in one place.
func opsCheckContainer(profileName, envName, serviceName string) opsContainerStatus {
	container := docker.ContainerName(profileName, envName, serviceName)
	if opsIsContainerRunning(profileName, envName, container) {
		return opsContainerStatus{Running: true}
	}
	if !docker.DaemonAvailable() {
		return opsContainerStatus{NeedsDaemon: true}
	}
	return opsContainerStatus{NeedsService: true}
}

// applyContainerStatus sets the appropriate prompt field on the model based
// on the container check status. Returns true when the container is running
// and the action can proceed immediately.
//
// DBOpsModel, QueueOpsModel, and MockOpsModel each have a thin
// ensureContainerRunning wrapper that calls opsCheckContainer + apply with
// their own field-setting closures. The shared logic lives here; the per-model
// methods only wire model-specific fields (promptStartDaemon, etc.).
func (s opsContainerStatus) apply(setDaemon, setService func(bool), setLabel func(string), label string) bool {
	if s.Running {
		return true
	}
	if s.NeedsDaemon {
		setDaemon(true)
	} else {
		setService(true)
	}
	setLabel(label)
	return false
}
