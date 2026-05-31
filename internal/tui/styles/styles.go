// Package styles provides shared lipgloss styles for all TUI views.
package styles

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ── Colors (Kanagawa-inspired) ────────────────────────────────────────────────

var (
	ColorBg       = lipgloss.Color("#1F1F28")
	ColorFg       = lipgloss.Color("#DCD7BA")
	ColorPrimary  = lipgloss.Color("#7E9CD8") // blue
	ColorSecondary = lipgloss.Color("#A6DA95") // green
	ColorAccent   = lipgloss.Color("#FF9E64") // orange
	ColorError    = lipgloss.Color("#E46876") // red
	ColorMuted    = lipgloss.Color("#54546D") // gray
	ColorCode     = lipgloss.Color("#C0A36E") // yellow
)

// ── Common Styles ─────────────────────────────────────────────────────────────

var (
	StyleTitle = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true).
			Padding(0, 1)

	StyleSubtitle = lipgloss.NewStyle().
			Foreground(ColorMuted)

	StyleHeader = lipgloss.NewStyle().
			Foreground(ColorFg).
			Bold(true)

	StyleCode = lipgloss.NewStyle().
			Foreground(ColorCode).
			Background(lipgloss.Color("#2A2A37")).
			Padding(0, 1)

	StylePanel = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorMuted).
			Padding(0, 1)

	StylePanelFocused = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorPrimary).
				Padding(0, 1)

	StyleInputLabel = lipgloss.NewStyle().
			Foreground(ColorSecondary).
			Bold(true)

	StyleListItem = lipgloss.NewStyle().
			Padding(0, 2)

	StyleListItemSelected = lipgloss.NewStyle().
				Foreground(ColorPrimary).
				Bold(true).
				Padding(0, 2)

	StyleListItemDisabled = lipgloss.NewStyle().
				Foreground(ColorMuted).
				Padding(0, 2)

	StyleBadgeEnv = lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorPrimary).
			Bold(true).
			Padding(0, 1)

	StyleButton = lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorPrimary).
			Bold(true).
			Padding(0, 2)

	// Inactive env tab — solid background, muted
	StyleBadgeEnvInactive = lipgloss.NewStyle().
				Foreground(ColorFg).
				Background(lipgloss.Color("#363646")).
				Padding(0, 1)

	StyleButtonSecondary = lipgloss.NewStyle().
				Foreground(ColorMuted).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorMuted).
				Padding(0, 1)

	StyleHelp = lipgloss.NewStyle().
			Foreground(ColorMuted).
			Italic(true)

	StyleError = lipgloss.NewStyle().
			Foreground(ColorError).
			Bold(true)

	StyleSuccess = lipgloss.NewStyle().
			Foreground(ColorSecondary).
			Bold(true)
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// HelpItem formats a key + description pair for the help bar.
func HelpItem(key, desc string) string {
	return lipgloss.NewStyle().
		Foreground(ColorAccent).
		Render(key) + " " + desc
}

// HelpGroup defines a labeled section of help items for HelpBar.
type HelpGroup struct {
	Label string
	Items []string // pre-rendered via HelpItem()
}

// labelStyle is used for section labels in HelpBar.
var labelStyle = lipgloss.NewStyle().Foreground(ColorMuted)

// HelpBar renders labeled help sections side by side as columns.
// Each section shows a dimmed label on top, then items distributed in 2 rows.
func HelpBar(groups ...HelpGroup) string {
	var cols []string
	for _, g := range groups {
		if len(g.Items) == 0 {
			continue
		}
		lines := []string{labelStyle.Render("── " + g.Label + " ──")}
		mid := (len(g.Items) + 1) / 2
		if mid > 0 {
			lines = append(lines, strings.Join(g.Items[:mid], "  "))
		}
		if mid < len(g.Items) {
			lines = append(lines, strings.Join(g.Items[mid:], "  "))
		}
		cols = append(cols, lipgloss.JoinVertical(lipgloss.Left, lines...))
	}
	if len(cols) == 0 {
		return ""
	}
	// Add gap between columns
	gapStyle := lipgloss.NewStyle().PaddingRight(4)
	for i := 0; i < len(cols)-1; i++ {
		cols[i] = gapStyle.Render(cols[i])
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols...)
}

// EnvBadge returns a styled environment name badge.
func EnvBadge(name string) string {
	return StyleBadgeEnv.Render(" " + name + " ")
}

// Dot returns a colored circle indicator based on container state.
func Dot(state string) string {
	var c lipgloss.Color
	switch state {
	case "running":
		c = ColorSecondary // green
	case "stopped", "exited":
		c = ColorError // red
	case "paused":
		c = ColorAccent // orange
	default:
		c = ColorMuted // gray
	}
	return lipgloss.NewStyle().
		Foreground(c).
		Render(fmt.Sprintf("● %s", state))
}
