package views

import (
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/docker"
	"github.com/stackr/stackr/internal/hub"
)

// NavTarget identifies a screen to navigate to.
type NavTarget int

const (
	NavHome NavTarget = iota
	NavNewProfile
	NavProfileView
	NavAddService
	NavDBOps
	NavQueueOps
	NavMockOps
	NavRequestOps
	NavErrors
	NavGitOps
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

// queueOpsDoneMsg is sent when a queue operation action completes.
type queueOpsDoneMsg struct {
	result string
	err    error
}

// mockOpsDoneMsg is sent when a MockServer operation action completes.
type mockOpsDoneMsg struct {
	result string
	err    error
}

// requestOpsDoneMsg is sent when a Request Ops operation completes.
type requestOpsDoneMsg struct {
	result string
	err    error
}

// ErrorMsg carries a structured error to surface in the footer and register
// in the ErrorRegistry. Context tags the source (e.g. "add_syncenv").
type ErrorMsg struct {
	Context string
	Message string
}

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

// statusLoadedMsg carries Docker status results for an environment.
// Used by the profile view to refresh container state after operations.
type statusLoadedMsg struct {
	statuses        []docker.ServiceStatus
	err             string
	daemonAvailable bool
	envName         string // which env these statuses belong to; "" means "current active env"
	fromLaunch      bool   // true when this result is from launchDockerCmd() completion
}

// ── Action-clear Messages ──

// ClearActionMsg clears transient result state (lastResult / err) across views.
// Sent by ClearActionAfter after a delay.
type ClearActionMsg struct{}

// ── Service removal Messages ──

// svcRmDoneMsg is sent when a service is removed from a profile environment.
type svcRmDoneMsg struct {
	envName string
	svcName string
}

// ── Add Service validation Messages ──

// portConflictMsg carries a port conflict error back to AddServiceModel
// so it renders inside the review panel, not in the global footer.
type portConflictMsg struct {
	err string
}

// ── Compose Start Failure Messages ──

// composeStartFailedMsg is sent when docker.UpService fails (e.g. image not found).
// The profile view uses this to show the error and mark the container as failed.
type composeStartFailedMsg struct {
	envName   string
	compName  string
	err       error
	rawDetail string // full raw Docker output for the detail view
}

// ── Spinner Messages ──

type SpinnerTickMsg struct{}

// ── Profile import/export (home) ──

// importResultMsg is sent when a profile import operation completes.
type importResultMsg struct {
	profiles []config.Profile
	err      string
}

// ── Add Service (hub search / tag fetch / save) ──

// hubSearchResultMsg carries the results of a Docker Hub search.
type hubSearchResultMsg struct {
	results []hub.SearchResult
	err     string
}

// hubTagsResultMsg carries the tags fetched for a selected image.
type hubTagsResultMsg struct {
	tags []hub.Tag
	err  string
}

// svcSavedMsg is sent when a service or local process is saved.
type svcSavedMsg struct{}

// ── Add Sync Env wizard (dbops) ──

// wizardSavedMsg is sent by add_syncenv save() when the user confirms the
// review step. editMode/origName let the DBOps parent migrate dump dirs
// when a sync env is renamed.
type wizardSavedMsg struct {
	env      *config.SyncEnv
	editMode bool
	origName string
}
