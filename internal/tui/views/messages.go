package views

import "github.com/stackr/stackr/internal/config"

// NavTarget identifies a screen to navigate to.
type NavTarget int

const (
	NavHome NavTarget = iota
	NavNewProfile
	NavProfileView
	NavAddService
	NavDBOps
)

// NavigateTo is a tea.Msg that tells the root App to switch screens.
type NavigateTo struct {
	Screen      NavTarget
	ProfileName string
	EnvName     string
	ServiceName string // optional: used to restore selection for service-specific screens
}

// ServiceKey returns a unique key for service-level screens (DBOps).
func (n NavigateTo) ServiceKey() string {
	return n.ProfileName + "/" + n.EnvName + "/" + n.ServiceName
}

// ProfileSavedMsg is sent when a profile is created or updated.
type ProfileSavedMsg struct {
	Config *config.Config
}

// ErrorMsg carries an error string to surface in the footer.
type ErrorMsg string

// ── Discovery Messages ──

type discoveryContextsMsg struct {
	contexts []string
}

type discoveryNamespacesMsg struct {
	namespaces []string
}

type discoveryServicesMsg struct {
	services []config.PortForward
}

type discoveryErrorMsg struct {
	err string
}

// ── Stats Messages ──

type statsTickMsg struct {
	container string
}

type statsResultMsg struct {
	data string
	err  string
}

// ── Status Refresh Messages ──

// statusRefreshTickMsg is sent periodically (every 3s) to refresh Docker
// service status for the current environment while the profile view is active.
type statusRefreshTickMsg struct{}

// ── Spinner Messages ──

type SpinnerTickMsg struct{}
