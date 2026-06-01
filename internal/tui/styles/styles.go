// Package styles provides shared lipgloss styles for all TUI views.
package styles

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/portforward"
)

// ── Colors (Kanagawa-inspired) ────────────────────────────────────────────────

var (
	ColorBg        = lipgloss.Color("#1F1F28")
	ColorFg        = lipgloss.Color("#DCD7BA")
	ColorPrimary   = lipgloss.Color("#7E9CD8") // blue
	ColorSecondary = lipgloss.Color("#A6DA95") // green
	ColorAccent    = lipgloss.Color("#FF9E64") // orange
	ColorError     = lipgloss.Color("#E46876") // red
	ColorMuted     = lipgloss.Color("#54546D") // gray
	ColorCode      = lipgloss.Color("#C0A36E") // yellow
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

	StylePanelError = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorError).
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

	StyleDiscoveryItem = lipgloss.NewStyle()

	StyleDiscoveryItemSelected = lipgloss.NewStyle().
					Foreground(ColorPrimary).
					Bold(true)
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// HelpItemResult holds a rendered key and its description separately
// so that HelpBar can align keys within a group.
type HelpItemResult struct {
	keyStyled string
	desc      string
	keyWidth  int
}

// Render returns the formatted "key + desc" string.
func (h HelpItemResult) Render() string {
	return h.keyStyled + " " + h.desc
}

// HelpItem formats a key + description pair for the help bar.
// Keys are rendered in accent color but NOT padded here — padding
// is applied by HelpBar per-group for alignment.
func HelpItem(key, desc string) HelpItemResult {
	styledKey := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Render(key)
	return HelpItemResult{
		keyStyled: styledKey,
		desc:      desc,
		keyWidth:  lipgloss.Width(styledKey),
	}
}

// InlineHelp renders a single-line help hint using the shared help style.
func InlineHelp(items ...HelpItemResult) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.Render())
	}
	return StyleHelp.Render(strings.Join(parts, "  "))
}

// BackHelp renders the shared back hint plus any additional inline hints.
func BackHelp(items ...HelpItemResult) string {
	base := []HelpItemResult{HelpItem("esc", "back")}
	return InlineHelp(append(base, items...)...)
}

// HelpGroup defines a labeled section of help items for HelpBar.
type HelpGroup struct {
	Label string
	Items []HelpItemResult
}

// helpPanelStyle is used for each HelpBar section box.
var helpPanelStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(ColorMuted).
	Padding(0, 1)

// helpLabelStyle is used for the section label inside each box.
var helpLabelStyle = lipgloss.NewStyle().Foreground(ColorMuted)

// HelpBar renders labeled help sections as bordered boxes side by side.
// Items within each group are padded to equal width so columns align
// vertically across rows.
func HelpBar(groups ...HelpGroup) string {
	var cols []string
	for _, g := range groups {
		if len(g.Items) == 0 {
			continue
		}

		// 1. Pad keys within the group so descriptions align.
		maxKeyW := 0
		for _, item := range g.Items {
			if item.keyWidth > maxKeyW {
				maxKeyW = item.keyWidth
			}
		}
		rendered := make([]string, len(g.Items))
		for i, item := range g.Items {
			pad := maxKeyW - item.keyWidth
			rendered[i] = item.keyStyled + strings.Repeat(" ", pad) + " " + item.desc
		}

		// 2. Pad all rendered items to the same total width so
		//    rows have equal column positions.
		maxW := 0
		for _, r := range rendered {
			if w := lipgloss.Width(r); w > maxW {
				maxW = w
			}
		}
		for i, r := range rendered {
			if d := maxW - lipgloss.Width(r); d > 0 {
				rendered[i] = r + strings.Repeat(" ", d)
			}
		}

		lines := []string{helpLabelStyle.Render(g.Label), ""}
		mid := (len(rendered) + 1) / 2
		if mid > 0 {
			lines = append(lines, strings.Join(rendered[:mid], "  "))
		}
		if mid < len(rendered) {
			lines = append(lines, strings.Join(rendered[mid:], "  "))
		}

		cols = append(cols, helpPanelStyle.Render(
			lipgloss.JoinVertical(lipgloss.Left, lines...),
		))
	}
	if len(cols) == 0 {
		return ""
	}
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

// ── Layout Helpers ─────────────────────────────────────────────────────────────

// Separator returns a horizontal separator line at the given width.
func Separator(width int) string {
	return lipgloss.NewStyle().
		Foreground(ColorMuted).
		Render(strings.Repeat("─", width))
}

// EmptyState renders a centered empty-state message inside a panel.
func EmptyState(msg string) string {
	return StylePanel.Width(60).Render(StyleSubtitle.Render(msg))
}

// PadLeft returns n spaces for consistent indentation.
func PadLeft(n int) string {
	return strings.Repeat(" ", n)
}

// ForwardDot returns a colored state indicator for port-forward states.
func ForwardDot(state portforward.ForwardState) string {
	var c lipgloss.Color
	switch state {
	case portforward.ForwardRunning:
		c = ColorSecondary // green
	case portforward.ForwardStarting:
		c = ColorAccent // orange
	case portforward.ForwardFailed:
		c = ColorError // red
	case portforward.ForwardStopped:
		c = ColorMuted // gray
	default:
		c = ColorMuted // gray
	}
	return lipgloss.NewStyle().
		Foreground(c).
		Render(fmt.Sprintf("● %s", state))
}
