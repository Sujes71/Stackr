package whichkey

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/tui/styles"
)

// View renders the which-key floating panel. Returns empty string when idle.
func (m *WhichKeyModel) View(terminalWidth int) string {
	if m.state == stateIdle {
		return ""
	}

	w := terminalWidth
	if w == 0 {
		w = 80
	}

	var lines []string

	// Breadcrumb line for sub-menus
	if m.state == statePrefix && len(m.prefixPath) > 0 {
		breadcrumb := styles.StyleInputLabel.Render(strings.Join(m.prefixPath, " → ") + " →")
		lines = append(lines, "  "+breadcrumb)
	}

	// Calculate column layout
	totalItems := 0
	for _, group := range m.commands {
		totalItems += len(group.Items)
	}

	if totalItems == 0 {
		return ""
	}

	// Render groups side by side
	groupLines := make([][]string, len(m.commands))
	maxGroupLines := 0
	for gi, group := range m.commands {
		// Group label
		groupLines[gi] = append(groupLines[gi], "  "+styles.StyleInputLabel.Render(group.Label))

		for _, item := range group.Items {
			keyStr := item.Key
			if len(item.Children) > 0 {
				keyStr += " →"
			}
			styledKey := lipgloss.NewStyle().
				Foreground(styles.ColorAccent).
				Render(fmt.Sprintf("  %-6s", keyStr))
			desc := styles.StyleHelp.Render(item.Desc)
			icon := ""
			if item.Icon != "" {
				icon = item.Icon + " "
			}
			groupLines[gi] = append(groupLines[gi], styledKey+icon+desc)
		}

		if len(groupLines[gi]) > maxGroupLines {
			maxGroupLines = len(groupLines[gi])
		}
	}

	// Pad all groups to same height
	for gi := range groupLines {
		for len(groupLines[gi]) < maxGroupLines {
			groupLines[gi] = append(groupLines[gi], "")
		}
	}

	// Calculate column widths
	groupWidths := make([]int, len(m.commands))
	for gi := range m.commands {
		// Find widest line in this group
		maxW := 0
		for _, line := range groupLines[gi] {
			lw := lipgloss.Width(line)
			if lw > maxW {
				maxW = lw
			}
		}
		groupWidths[gi] = maxW + 2 // padding
	}

	// Join groups horizontally
	for row := 0; row < maxGroupLines; row++ {
		parts := make([]string, len(groupLines))
		for gi := range groupLines {
			line := groupLines[gi][row]
			// Pad to group width
			padNeeded := groupWidths[gi] - lipgloss.Width(line)
			if padNeeded > 0 {
				line += strings.Repeat(" ", padNeeded)
			}
			parts[gi] = line
		}
		lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top, parts...))
	}

	content := strings.Join(lines, "\n")

	// Wrap in a bordered panel at the bottom
	panelStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.ColorMuted).
		Padding(0, 1).
		Width(min(w-2, lipgloss.Width(content)+4))

	return panelStyle.Render(content)
}
