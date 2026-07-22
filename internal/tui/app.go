package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/tui/styles"
	"github.com/stackr/stackr/internal/tui/views"
	"github.com/stackr/stackr/internal/tui/whichkey"
)

// Screen identifies which view is currently rendered.
type Screen int

const (
	ScreenHome Screen = iota
	ScreenProfileView
	ScreenNewProfile
	ScreenAddService
	ScreenDBOps
	ScreenQueueOps
	ScreenMockOps
	ScreenRequestOps
	ScreenErrors
	ScreenGitOps
)

// App is the root Bubble Tea model.
type App struct {
	cfg             *config.Config
	cwd             string // launching directory for git ops
	screen          Screen
	width           int
	height          int
	profileNavState map[string]views.ProfileNavState
	homeCursor      int

	// child views
	home        *views.HomeModel
	profileView *views.ProfileViewModel
	newProfile  *views.NewProfileModel
	addService  *views.AddServiceModel
	dbOps       *views.DBOpsModel
	queueOps    *views.QueueOpsModel
	mockOps       *views.MockOpsModel
	requestOps    *views.RequestOpsModel
	err           string
	errorRegistry *views.ErrorRegistry
	errFlash      string
	errorView     *views.ErrorListModel
	gitOps        *views.GitOpsModel
	whichKey      *whichkey.WhichKeyModel
}

func New(cfg *config.Config, cwd string) *App {
	errorRegistry := views.NewErrorRegistry()
	home := views.NewHome(cfg, errorRegistry)
	return &App{
		cfg:             cfg,
		cwd:             cwd,
		screen:          ScreenHome,
		home:            home,
		profileNavState: make(map[string]views.ProfileNavState),
		errorRegistry:   errorRegistry,
		whichKey:        whichkey.New(),
	}
}

// ── Init ─────────────────────────────────────────────────────────────────────

func (a *App) Init() tea.Cmd {
	return a.home.Init()
}

// ── Update ───────────────────────────────────────────────────────────────────

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.whichKey.SetSize(msg.Width, msg.Height)
		// fall through to updateActive so child views also receive dimensions

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			if a.profileView != nil {
				a.profileView.StopAllForwards()
				a.profileView.StopAllLocalProcesses()
			}
			return a, tea.Quit
		}
		// ── Which-key interception ──
		if a.whichKey.IsOpen() {
			handled, cmd := a.whichKey.Update(msg)
			if !handled {
				a.whichKey.Close()
			}
			return a, cmd
		}
		if msg.String() == "tab" {
			if provider := a.activeProvider(); provider != nil && !provider.IsTextInputActive() {
				a.whichKey.Open(provider.WhichKeyCommands())
				return a, nil
			}
		}

	// Navigation messages from child views
	case views.NavigateTo:
		return a.handleNav(msg)

	case views.ProfileSavedMsg:
		a.cfg = msg.Config
		a.homeCursor = a.home.Cursor()
		a.home = views.NewHome(a.cfg, a.errorRegistry)
		a.home.SetCursor(a.homeCursor)
		a.screen = ScreenHome
		return a, a.home.Init()

	case views.ErrorMsg:
		a.errorRegistry.Add(msg.Context, msg.Message)
		a.errFlash = msg.Message
		return a, views.ClearActionAfter(5 * time.Second)

	case views.ClearActionMsg:
		a.err = ""
		a.errFlash = ""

	case views.SpinnerTickMsg:
		// Route spinner to active screen AND to non-active models that are busy
		m, cmd := a.updateActive(msg)
		var extra []tea.Cmd
		if a.screen != ScreenProfileView && a.profileView != nil && a.profileView.IsBusy() {
			_, c := a.profileView.Update(msg)
			extra = append(extra, c)
		}
		if a.screen != ScreenDBOps && a.dbOps != nil && a.dbOps.IsBusy() {
			_, c := a.dbOps.Update(msg)
			extra = append(extra, c)
		}
		if a.screen != ScreenQueueOps && a.queueOps != nil && a.queueOps.IsBusy() {
			_, c := a.queueOps.Update(msg)
			extra = append(extra, c)
		}
		if a.screen != ScreenMockOps && a.mockOps != nil && a.mockOps.IsBusy() {
			_, c := a.mockOps.Update(msg)
			extra = append(extra, c)
		}
		if a.screen != ScreenRequestOps && a.requestOps != nil && a.requestOps.IsBusy() {
			_, c := a.requestOps.Update(msg)
			extra = append(extra, c)
		}
		if a.screen != ScreenGitOps && a.gitOps != nil && a.gitOps.IsBusy() {
			_, c := a.gitOps.Update(msg)
			extra = append(extra, c)
		}
		if len(extra) > 0 {
			return m, tea.Batch(append([]tea.Cmd{cmd}, extra...)...)
		}
		return m, cmd
	}

	// Delegate to active screen
	return a.updateActive(msg)
}

func (a *App) handleNav(nav views.NavigateTo) (tea.Model, tea.Cmd) {
	a.err = ""
	a.errFlash = ""
	a.captureProfileNavState(nav)
	if a.screen == ScreenHome {
		a.homeCursor = a.home.Cursor()
	}
	switch nav.Screen {

	case views.NavHome:
		a.screen = ScreenHome
		a.home = views.NewHome(a.cfg, a.errorRegistry)
		a.home.SetSize(a.width, a.height)
		a.home.SetCursor(a.homeCursor)
		return a, a.home.Init()

	case views.NavNewProfile:
		a.screen = ScreenNewProfile
		a.newProfile = views.NewNewProfile(a.cfg)
		a.newProfile.SetSize(a.width, a.height)
		return a, a.newProfile.Init()

	case views.NavProfileView:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenProfileView
			if a.profileView == nil || a.profileView.ProfileName() != nav.ProfileName {
				// Different profile or first time — create new (leaving state
				// already captured by captureProfileNavState at top of handleNav)
				a.profileView = views.NewProfileView(a.cfg, p, a.errorRegistry)
				if state, ok := a.profileNavState[nav.ProfileName]; ok {
					a.profileView.RestoreNavState(state)
				}
				a.profileView.SetSize(a.width, a.height)
				return a, a.profileView.Init()
			}
			// Same profile — keep model alive (pending ops survive)
			a.profileView.RestoreSelection(nav.EnvName, nav.ServiceName)
			a.profileView.SetSize(a.width, a.height)
			return a, a.profileView.ReShow(a.cfg)
		}

	case views.NavAddService:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenAddService
			a.addService = views.NewAddService(a.cfg, p, nav.EnvName)
			a.addService.SetSize(a.width, a.height)
			return a, a.addService.Init()
		}

	case views.NavDBOps:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenDBOps
			if a.dbOps == nil || a.dbOps.ServiceKey() != nav.ServiceKey() {
				a.dbOps = views.NewDBOps(a.cfg, p, nav.EnvName, nav.ServiceName)
				a.dbOps.SetSize(a.width, a.height)
				return a, a.dbOps.Init()
			}
			// Same service — keep model alive
			a.dbOps.ReShow(a.cfg)
			a.dbOps.SetSize(a.width, a.height)
			return a, nil
		}

	case views.NavQueueOps:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenQueueOps
			if a.queueOps == nil || a.queueOps.ServiceKey() != nav.ServiceKey() {
				a.queueOps = views.NewQueueOps(a.cfg, p, nav.EnvName, nav.ServiceName)
				a.queueOps.SetSize(a.width, a.height)
				return a, a.queueOps.Init()
			}
			// Same service — keep model alive
			a.queueOps.ReShow(a.cfg)
			a.queueOps.SetSize(a.width, a.height)
			return a, nil
		}

	case views.NavMockOps:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenMockOps
			if a.mockOps == nil || a.mockOps.ServiceKey() != nav.ServiceKey() {
				a.mockOps = views.NewMockOps(a.cfg, p, nav.EnvName, nav.ServiceName)
				a.mockOps.SetSize(a.width, a.height)
				return a, a.mockOps.Init()
			}
			// Same service — keep model alive
			a.mockOps.ReShow(a.cfg)
			a.mockOps.SetSize(a.width, a.height)
			return a, nil
		}

	case views.NavRequestOps:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenRequestOps
			envName := nav.EnvName
			// Default to first RequestEnv if no env specified or not found
			if envName == "" {
				if len(p.RequestEnvs) > 0 {
					envName = p.RequestEnvs[0].Name
				} else {
					envName = "local"
				}
			}
			if a.requestOps == nil || a.requestOps.EnvName() != envName {
				a.requestOps = views.NewRequestOps(a.cfg, p, envName)
				a.requestOps.SetSize(a.width, a.height)
				return a, a.requestOps.Init()
			}
			a.requestOps.ReShow(a.cfg)
			a.requestOps.SetSize(a.width, a.height)
			return a, nil
		}

	case views.NavErrors:
		a.screen = ScreenErrors
		a.errorView = views.NewErrorList(a.errorRegistry.All(), nav.ProfileName, nav.EnvName)
		a.errorView.SetSize(a.width, a.height)
		return a, a.errorView.Init()

	case views.NavGitOps:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenGitOps
			a.gitOps = views.NewGitOps(a.cfg, p, a.cwd)
			a.gitOps.SetSize(a.width, a.height)
			return a, a.gitOps.Init()
		}
	}
	return a, nil
}

func (a *App) captureProfileNavState(nav views.NavigateTo) {
	if a.profileView == nil {
		return
	}
	if a.screen != ScreenProfileView {
		return
	}
	profileName := a.profileView.ProfileName()
	if profileName == "" {
		return
	}
	a.profileNavState[profileName] = a.profileView.SnapshotNavState()
}

// activeProvider returns the KeyContextProvider for the current screen,
// or nil for wizard screens that should not open which-key.
func (a *App) activeProvider() whichkey.KeyContextProvider {
	switch a.screen {
	case ScreenHome:
		return a.home
	case ScreenProfileView:
		return a.profileView
	case ScreenDBOps:
		return a.dbOps
	case ScreenQueueOps:
		return a.queueOps
	case ScreenMockOps:
		return a.mockOps
	case ScreenRequestOps:
		return a.requestOps
	case ScreenGitOps:
		return a.gitOps
	}
	// Wizard screens (NewProfile, AddService, Errors) — no which-key
	return nil
}

func (a *App) updateActive(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch a.screen {
	case ScreenHome:
		m, c := a.home.Update(msg)
		a.home = m.(*views.HomeModel)
		cmd = c
	case ScreenProfileView:
		m, c := a.profileView.Update(msg)
		a.profileView = m.(*views.ProfileViewModel)
		cmd = c
	case ScreenNewProfile:
		m, c := a.newProfile.Update(msg)
		a.newProfile = m.(*views.NewProfileModel)
		cmd = c
	case ScreenAddService:
		m, c := a.addService.Update(msg)
		a.addService = m.(*views.AddServiceModel)
		cmd = c
	case ScreenDBOps:
		m, c := a.dbOps.Update(msg)
		a.dbOps = m.(*views.DBOpsModel)
		cmd = c
	case ScreenQueueOps:
		m, c := a.queueOps.Update(msg)
		a.queueOps = m.(*views.QueueOpsModel)
		cmd = c
	case ScreenMockOps:
		m, c := a.mockOps.Update(msg)
		a.mockOps = m.(*views.MockOpsModel)
		cmd = c
	case ScreenRequestOps:
		if a.requestOps != nil {
			m, c := a.requestOps.Update(msg)
			a.requestOps = m.(*views.RequestOpsModel)
			cmd = c
		}
	case ScreenErrors:
		if a.errorView != nil {
			m, c := a.errorView.Update(msg)
			a.errorView = m.(*views.ErrorListModel)
			cmd = c
		}
	case ScreenGitOps:
		if a.gitOps != nil {
			m, c := a.gitOps.Update(msg)
			a.gitOps = m.(*views.GitOpsModel)
			cmd = c
		}
	}
	return a, cmd
}

// ── View ─────────────────────────────────────────────────────────────────────

func (a *App) View() string {
	header := a.renderHeader()
	body := a.renderBody()
	footer := a.renderFooter()
	sep := styles.Separator(a.width)
	// Safety net: truncate any line that exceeds terminal width.
	body = styles.TruncateLines(body, a.width)

	// Build the full output and pad with blank lines to fill the terminal.
	// Bubbletea uses a diff renderer — lines not overwritten by the new
	// render stay visible, causing ghost artifacts when switching views.
	// Padding ensures every terminal line is written on each frame.
	var out strings.Builder
	out.WriteString(header)
	out.WriteString("\n")
	out.WriteString(sep)
	out.WriteString("\n")
	out.WriteString(body)
	// Count how many lines we've written so far.
	lines := strings.Count(out.String(), "\n") + 1
	// Footer will add ~3 more lines (sep + quit + possible error).
	footerLines := 3
	// Which-key panel takes ~5-8 lines when open.
	whichKeyLines := 0
	if a.whichKey.IsOpen() {
		whichKeyPanel := a.whichKey.View(a.width)
		out.WriteString("\n")
		out.WriteString(whichKeyPanel)
		whichKeyLines = strings.Count(whichKeyPanel, "\n") + 1
	}
	needed := a.height - footerLines - whichKeyLines
	if needed > lines {
		for i := 0; i < needed-lines; i++ {
			out.WriteString("\n")
		}
	}
	out.WriteString("\n")
	out.WriteString(footer)
	return out.String()
}

func (a *App) renderHeader() string {
	logo := styles.StyleTitle.Render(" ⬡ Stackr ")
	version := styles.StyleSubtitle.Render("v0.1.0")
	breadcrumb := styles.StyleSubtitle.Render(a.breadcrumb())
	right := lipgloss.JoinHorizontal(lipgloss.Center, breadcrumb, "  ", version)

	w := a.width
	if w == 0 {
		w = 80
	}
	gap := w - lipgloss.Width(logo) - lipgloss.Width(right) - 2
	if gap < 0 {
		gap = 0
	}
	spacer := fmt.Sprintf("%*s", gap, "")
	return lipgloss.JoinHorizontal(lipgloss.Center, logo, spacer, right)
}

func (a *App) breadcrumb() string {
	switch a.screen {
	case ScreenHome:
		return "profiles"
	case ScreenProfileView:
		if a.profileView != nil {
			return "profiles / " + a.profileView.ProfileName()
		}
	case ScreenNewProfile:
		return "profiles / new"
	case ScreenAddService:
		if a.addService != nil {
			return "profiles / " + a.addService.ProfileName() + " / Add Service"
		}
	case ScreenDBOps:
		if a.dbOps != nil {
			return "profiles / " + a.dbOps.ProfileName() + " / " + a.dbOps.EnvName() + " / " + a.dbOps.ServiceName() + " / DB Ops"
		}
	case ScreenQueueOps:
		if a.queueOps != nil {
			return "profiles / " + a.queueOps.ProfileName() + " / " + a.queueOps.EnvName() + " / " + a.queueOps.ServiceName() + " / Queue Ops"
		}
	case ScreenMockOps:
		if a.mockOps != nil {
			return "profiles / " + a.mockOps.ProfileName() + " / " + a.mockOps.EnvName() + " / " + a.mockOps.ServiceName() + " / MockServer Ops"
		}
	case ScreenRequestOps:
		if a.requestOps != nil {
			return "profiles / " + a.requestOps.ProfileName() + " / " + a.requestOps.EnvName() + " / Request Ops"
		}
	case ScreenErrors:
		return "errors"
	case ScreenGitOps:
		if a.gitOps != nil {
			return "profiles / " + a.gitOps.ProfileName() + " / Git Ops"
		}
	}
	return ""
}

func (a *App) renderBody() string {
	switch a.screen {
	case ScreenHome:
		return a.home.View()
	case ScreenProfileView:
		return a.profileView.View()
	case ScreenNewProfile:
		return a.newProfile.View()
	case ScreenAddService:
		return a.addService.View()
	case ScreenDBOps:
		return a.dbOps.View()
	case ScreenQueueOps:
		return a.queueOps.View()
	case ScreenMockOps:
		return a.mockOps.View()
	case ScreenRequestOps:
		if a.requestOps != nil {
			return a.requestOps.View()
		}
	case ScreenErrors:
		if a.errorView != nil {
			return a.errorView.View()
		}
	case ScreenGitOps:
		if a.gitOps != nil {
			return a.gitOps.View()
		}
	}
	return ""
}

func (a *App) renderFooter() string {
	sep := styles.Separator(a.width)
	quitLine := styles.StyleHelp.Render("ctrl+c quit")
	if a.err != "" {
		errLine := styles.StyleError.Render("  ✗ " + a.err)
		return lipgloss.JoinVertical(lipgloss.Left, sep, errLine, quitLine)
	}
	return lipgloss.JoinVertical(lipgloss.Left, sep, quitLine)
}
