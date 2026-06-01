package portforward

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ForwardMonitorTick is sent by the health monitor loop at each tick interval.
// ProfileViewModel listens for this message to check running forward health.
type ForwardMonitorTick struct{}

// ForwardMonitorCmd returns a tea.Cmd that fires a ForwardMonitorTick
// every 5 seconds. The tick naturally stops when the model is replaced
// (screen navigation), so no explicit cleanup is needed.
func ForwardMonitorCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return ForwardMonitorTick{}
	})
}
