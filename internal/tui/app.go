package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/tui/views"
)

// ── Local styles (used in header/footer rendering) ──────────────────────────

var (
	StyleTitle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7E9CD8")).
			Bold(true).
			Padding(0, 1)

	StyleSubtitle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#54546D"))

	StyleError = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E46876")).
			Bold(true)

	StyleHelp = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#54546D")).
			Italic(true)
)

// Screen identifies which view is currently rendered.
type Screen int

const (
	ScreenHome Screen = iota
	ScreenProfileView
	ScreenNewProfile
	ScreenAddService
	ScreenDBOps
	ScreenLogs
)

// App is the root Bubble Tea model.
type App struct {
	cfg             *config.Config
	screen          Screen
	width           int
	height          int
	profileNavState map[string]views.ProfileNavState

	// child views
	home        *views.HomeModel
	profileView *views.ProfileViewModel
	newProfile  *views.NewProfileModel
	addService  *views.AddServiceModel
	dbOps       *views.DBOpsModel
	logs        *views.LogsModel

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
			}
			return a, tea.Quit
		}

	// Navigation messages from child views
	case views.NavigateTo:
		return a.handleNav(msg)

	case views.ProfileSavedMsg:
		a.cfg = msg.Config
		a.home = views.NewHome(a.cfg)
		a.screen = ScreenHome
		return a, a.home.Init()

	case views.ErrorMsg:
		a.err = string(msg)
		return a, views.ClearActionAfter(3 * time.Second)

	case views.ClearActionMsg:
		a.err = ""
	}

	// Delegate to active screen
	return a.updateActive(msg)
}

func (a *App) handleNav(nav views.NavigateTo) (tea.Model, tea.Cmd) {
	a.err = ""
	a.captureProfileNavState(nav)
	switch nav.Screen {

	case views.NavHome:
		a.screen = ScreenHome
		a.home = views.NewHome(a.cfg)
		a.home.SetSize(a.width, a.height)
		return a, a.home.Init()

	case views.NavNewProfile:
		a.screen = ScreenNewProfile
		a.newProfile = views.NewNewProfile(a.cfg)
		return a, a.newProfile.Init()

	case views.NavProfileView:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenProfileView
			a.profileView = views.NewProfileView(a.cfg, p)
			if state, ok := a.profileNavState[nav.ProfileName]; ok {
				a.profileView.RestoreNavState(state)
			}
			a.profileView.RestoreSelection(nav.EnvName, nav.ServiceName)
			a.profileView.SetSize(a.width, a.height)
			return a, a.profileView.Init()
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
			a.dbOps = views.NewDBOps(a.cfg, p, nav.EnvName, nav.ServiceName)
			return a, a.dbOps.Init()
		}

	case views.NavLogs:
		if p, ok := a.cfg.GetProfile(nav.ProfileName); ok {
			a.screen = ScreenLogs
			a.logs = views.NewLogs(p, nav.EnvName, nav.ServiceName)
			return a, a.logs.Init()
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
	case ScreenLogs:
		m, c := a.logs.Update(msg)
		a.logs = m.(*views.LogsModel)
		cmd = c
	}
	return a, cmd
}

// ── View ─────────────────────────────────────────────────────────────────────

func (a *App) View() string {
	header := a.renderHeader()
	body := a.renderBody()
	footer := a.renderFooter()

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (a *App) renderHeader() string {
	logo := StyleTitle.Render(" ⬡ Stackr ")
	version := StyleSubtitle.Render("v0.1.0")
	breadcrumb := StyleSubtitle.Render(a.breadcrumb())
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
	case ScreenLogs:
		return "logs"
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
	case ScreenLogs:
		return a.logs.View()
	}
	return ""
}

func (a *App) renderFooter() string {
	if a.err != "" {
		return StyleError.Render("  ✗ " + a.err)
	}
	return StyleHelp.Render("ctrl+c quit")
}
