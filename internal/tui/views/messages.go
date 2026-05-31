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
	NavLogs
)

// NavigateTo is a tea.Msg that tells the root App to switch screens.
type NavigateTo struct {
	Screen      NavTarget
	ProfileName string
	EnvName     string
	ServiceName string // optional: for service-specific screens (logs, db ops)
}

// ProfileSavedMsg is sent when a profile is created or updated.
type ProfileSavedMsg struct {
	Config *config.Config
}

// ErrorMsg carries an error string to surface in the footer.
type ErrorMsg string
