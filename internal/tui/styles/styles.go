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
	ColorPurple    = lipgloss.Color("#938AA9") // purple — parameter labels
	ColorCyan      = lipgloss.Color("#66CCCC") // cyan
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

	StyleHelp = lipgloss.NewStyle().
			Foreground(ColorMuted)

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

// BackHelp renders the shared back hint plus any additional inline hints,
// indented 2 spaces to match content alignment.
func BackHelp(items ...HelpItemResult) string {
	base := []HelpItemResult{HelpItem("esc", "back")}
	return "  " + InlineHelp(append(base, items...)...)
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
	case "restarting":
		c = ColorAccent // orange — visible alert that something is wrong
	case "failed":
		return lipgloss.NewStyle().
			Foreground(ColorError).
			Render("● failed")
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

// TruncateLines hard-truncates every line in s so none exceeds maxW visible chars.
// Uses lipgloss.Width() for correct Unicode + ANSI measurement.
// Overflow lines get "..." appended. Returns s unchanged if maxW <= 0.
func TruncateLines(s string, maxW int) string {
	if maxW <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > maxW {
			// Truncate rune by rune until we fit.
			runes := []rune(line)
			var truncated []rune
			width := 0
			for _, r := range runes {
				w := lipgloss.Width(string(r))
				if width+w > maxW-3 {
					break
				}
				truncated = append(truncated, r)
				width += w
			}
			lines[i] = string(truncated) + "..."
		}
	}
	return strings.Join(lines, "\n")
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
		c = ColorError // red — match Docker Dot("stopped")
	default:
		c = ColorMuted // gray
	}
	return lipgloss.NewStyle().
		Foreground(c).
		Render(fmt.Sprintf("● %s", state))
}

// DockerOffDot returns a dimmed dot for "docker off" state.
func DockerOffDot() string {
	return lipgloss.NewStyle().
		Foreground(ColorMuted).
		Render("⚫ docker off")
}

// LocalProcessDot returns a colored state indicator for local processes.
// Uses the same colors as Dot() for running/stopped, but a different glyph (▶)
// so users can distinguish Docker services from host/local processes at a glance.
func LocalProcessDot(running bool) string {
	var c lipgloss.Color
	state := "stopped"
	if running {
		c = ColorSecondary // green
		state = "running"
	} else {
		c = ColorError // red
	}
	return lipgloss.NewStyle().
		Foreground(c).
		Render(fmt.Sprintf("▶ %s", state))
}

// ── Shared state helpers (XC-006, XC-007, AUDIT-020) ──────────────────────────

// emptyStatePanel is the subtle panel used by EmptyState.
var emptyStatePanel = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(ColorMuted).
	Padding(0, 2)

// EmptyState renders a centered, muted message inside a subtle panel.
// Used by views to render "no items" placeholders consistently.
func EmptyState(msg string, width int) string {
	rendered := StyleSubtitle.Render(msg)
	panelW := lipgloss.Width(rendered) + 6
	if panelW < 40 {
		panelW = 40
	}
	if width > 0 && panelW > width-4 {
		panelW = width - 4
	}
	panel := emptyStatePanel.Width(panelW).Render(rendered)
	if width > 0 {
		return lipgloss.PlaceHorizontal(width, lipgloss.Center, panel)
	}
	return panel
}

// errorBannerPrefix is the standardized error-line prefix ("  ✗ ") used by
// ErrorBanner and ErrorLine so all views share the same indent and glyph.
const errorBannerPrefix = "  ✗ "

// wrapSimple splits s into lines of at most width characters at word
// boundaries. Words longer than width are placed on their own line.
func wrapSimple(s string, width int) []string {
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

// ErrorBanner renders an error message with the standardized "  ✗ " prefix
// and word-wraps long messages so they don't overflow the viewport.
// Used by inline error displays and the app footer (XC-007).
func ErrorBanner(msg string, width int) string {
	wrapped := wrapSimple(msg, 80)
	if len(wrapped) <= 1 {
		return StyleError.Render(errorBannerPrefix + msg)
	}
	var b strings.Builder
	b.WriteString(errorBannerPrefix + wrapped[0] + "\n")
	for _, line := range wrapped[1:] {
		b.WriteString("  " + line + "\n")
	}
	return StyleError.Render(strings.TrimSuffix(b.String(), "\n"))
}

// ErrorLine renders a single-line error with the standardized "  ✗ " prefix
// matching the content indent used across views (AUDIT-020).
func ErrorLine(msg string) string {
	return StyleError.Render(errorBannerPrefix + msg)
}
