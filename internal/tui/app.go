package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/tui/styles"
	"github.com/stackr/stackr/internal/tui/views"
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
)

// App is the root Bubble Tea model.
type App struct {
	cfg             *config.Config
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
	mockOps     *views.MockOpsModel
	err string
}

func New(cfg *config.Config) *App {
	home := views.NewHome(cfg)
	return &App{
		cfg:             cfg,
		screen:          ScreenHome,
		home:            home,
		profileNavState: make(map[string]views.ProfileNavState),
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
		// fall through to updateActive so child views also receive dimensions

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			if a.profileView != nil {
				a.profileView.StopAllForwards()
				a.profileView.StopAllLocalProcesses()
			}
			return a, tea.Quit
		}

	// Navigation messages from child views
	case views.NavigateTo:
		return a.handleNav(msg)

	case views.ProfileSavedMsg:
		a.cfg = msg.Config
		a.homeCursor = a.home.Cursor()
		a.home = views.NewHome(a.cfg)
		a.home.SetCursor(a.homeCursor)
		a.screen = ScreenHome
		return a, a.home.Init()

	case views.ErrorMsg:
		a.err = string(msg)
		return a, nil

	case views.ClearActionMsg:
		a.err = ""

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
	a.captureProfileNavState(nav)
	if a.screen == ScreenHome {
		a.homeCursor = a.home.Cursor()
	}
	switch nav.Screen {

	case views.NavHome:
		a.screen = ScreenHome
		a.home = views.NewHome(a.cfg)
		a.home.SetSize(a.width, a.height)
		a.home.SetCursor(a.homeCursor)
		return a, tea.Sequence(tea.ClearScreen, a.home.Init())

	case views.NavNewProfile:
		a.screen = ScreenNewProfile
		a.newProfile = views.NewNewProfile(a.cfg)
		a.newProfile.SetSize(a.width, a.height-5)
		return a, tea.Sequence(tea.ClearScreen, a.newProfile.Init())

	case views.NavProfileView:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenProfileView
			if a.profileView == nil || a.profileView.ProfileName() != nav.ProfileName {
				// Different profile or first time — save old state, create new
				a.captureProfileNavState(nav)
				a.profileView = views.NewProfileView(a.cfg, p)
				if state, ok := a.profileNavState[nav.ProfileName]; ok {
					a.profileView.RestoreNavState(state)
				}
				a.profileView.SetSize(a.width, a.height)
				return a, tea.Sequence(tea.ClearScreen, a.profileView.Init())
			}
			// Same profile — keep model alive (pending ops survive)
			a.profileView.RestoreSelection(nav.EnvName, nav.ServiceName)
			a.profileView.SetSize(a.width, a.height)
			return a, tea.Sequence(tea.ClearScreen, a.profileView.ReShow(a.cfg))
		}

	case views.NavAddService:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenAddService
			a.addService = views.NewAddService(a.cfg, p, nav.EnvName)
			a.addService.SetSize(a.width, a.height)
			return a, tea.Sequence(tea.ClearScreen, a.addService.Init())
		}

	case views.NavDBOps:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenDBOps
			if a.dbOps == nil || a.dbOps.ServiceKey() != nav.ServiceKey() {
				a.dbOps = views.NewDBOps(a.cfg, p, nav.EnvName, nav.ServiceName)
				a.dbOps.SetSize(a.width, a.height-5)
				return a, tea.Sequence(tea.ClearScreen, a.dbOps.Init())
			}
			// Same service — keep model alive
			a.dbOps.ReShow(a.cfg)
			a.dbOps.SetSize(a.width, a.height-5)
			return a, tea.Sequence(tea.ClearScreen, nil)
		}

	case views.NavQueueOps:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenQueueOps
			if a.queueOps == nil || a.queueOps.ServiceKey() != nav.ServiceKey() {
				a.queueOps = views.NewQueueOps(a.cfg, p, nav.EnvName, nav.ServiceName)
				a.queueOps.SetSize(a.width, a.height-5)
				return a, tea.Sequence(tea.ClearScreen, a.queueOps.Init())
			}
			// Same service — keep model alive
			a.queueOps.ReShow(a.cfg)
			a.queueOps.SetSize(a.width, a.height-5)
			return a, tea.Sequence(tea.ClearScreen, nil)
		}

	case views.NavMockOps:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenMockOps
			if a.mockOps == nil || a.mockOps.ServiceKey() != nav.ServiceKey() {
				a.mockOps = views.NewMockOps(a.cfg, p, nav.EnvName, nav.ServiceName)
				a.mockOps.SetSize(a.width, a.height-5)
				return a, tea.Sequence(tea.ClearScreen, a.mockOps.Init())
			}
			// Same service — keep model alive
			a.mockOps.ReShow(a.cfg)
			a.mockOps.SetSize(a.width, a.height-5)
			return a, tea.Sequence(tea.ClearScreen, nil)
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
	}
	return a, cmd
}

// ── View ─────────────────────────────────────────────────────────────────────

func (a *App) View() string {
	header := a.renderHeader()
	body := a.renderBody()
	footer := a.renderFooter()
	sep := styles.Separator(a.width)
	return lipgloss.JoinVertical(lipgloss.Left, header, sep, body, "\n", footer)
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
			return "profiles / " + a.addService.ProfileName() + " / add service"
		}
	case ScreenDBOps:
		if a.dbOps != nil {
			return "profiles / " + a.dbOps.ProfileName() + " / " + a.dbOps.EnvName() + " / " + a.dbOps.ServiceName() + " / db ops"
		}
	case ScreenQueueOps:
		if a.queueOps != nil {
			return "profiles / " + a.queueOps.ProfileName() + " / " + a.queueOps.EnvName() + " / " + a.queueOps.ServiceName() + " / queue ops"
		}
	case ScreenMockOps:
		if a.mockOps != nil {
			return "profiles / " + a.mockOps.ProfileName() + " / " + a.mockOps.EnvName() + " / " + a.mockOps.ServiceName() + " / mock ops"
		}
	}
	return ""
}

func (a *App) renderBody() string {
	var body string
	switch a.screen {
	case ScreenHome:
		body = a.home.View()
	case ScreenProfileView:
		body = a.profileView.View()
	case ScreenNewProfile:
		body = a.newProfile.View()
	case ScreenAddService:
		body = a.addService.View()
	case ScreenDBOps:
		body = a.dbOps.View()
	case ScreenQueueOps:
		body = a.queueOps.View()
	case ScreenMockOps:
		body = a.mockOps.View()
	}
	// Every empty line is padded to full width with spaces so any old content
	// at that terminal position is overwritten (bare \n does not clear chars).
	// Extra lines are also added to fill the body area, ensuring the new frame
	// is at least as tall as the previous one.
	if a.width > 0 && a.height > 6 {
		body = fillBody(body, a.width, a.height-6)
	}
	return body
}

// fillBody ensures every line has at least w chars and the body reaches at
// least h lines. This prevents old frame content from persisting at
// positions where the new frame has empty or fewer lines.
func fillBody(s string, w, h int) string {
	lines := strings.Split(s, "\n")

	// Pad every empty line to full width with spaces
	for i, line := range lines {
		if line == "" {
			lines[i] = strings.Repeat(" ", w)
		}
	}

	// Pad to minimum height so the new frame covers the previous one
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}

	return strings.Join(lines, "\n")
}

func (a *App) renderFooter() string {
	sep := styles.Separator(a.width)
	var content string
	if a.err != "" {
		content = styles.StyleError.Render("  ✗ " + a.err)
	} else {
		content = styles.StyleHelp.Render("ctrl+c quit")
	}
	return lipgloss.JoinVertical(lipgloss.Left, sep, content)
}
