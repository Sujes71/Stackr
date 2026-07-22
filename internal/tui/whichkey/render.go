package whichkey

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/tui/styles"
)

// View renders the which-key floating panel. Returns empty string when idle.
// The panel is a flat multi-column grid without group labels — pure commands,
// Neovim which-key style.
func (m *WhichKeyModel) View(terminalWidth int) string {
	if m.state == stateIdle {
		return ""
	}

	w := terminalWidth
	if w == 0 {
		w = 80
	}

	// Flatten all items from all groups into a single list
	type flatItem struct {
		key  string
		desc string
	}
	var items []flatItem

	// Breadcrumb prefix for sub-menus
	breadcrumb := ""
	if m.state == statePrefix && len(m.prefixPath) > 0 {
		breadcrumb = strings.Join(m.prefixPath, " ") + " "
	}

	for _, group := range m.commands {
		for _, item := range group.Items {
			keyStr := item.Key
			if len(item.Children) > 0 {
				keyStr += " →"
			}
			items = append(items, flatItem{
				key:  breadcrumb + keyStr,
				desc: item.Desc,
			})
		}
	}

	if len(items) == 0 {
		return ""
	}

	// Calculate max key width for alignment
	maxKeyW := 0
	for _, it := range items {
		styled := lipgloss.NewStyle().Foreground(styles.ColorAccent).Render(it.key)
		if sw := lipgloss.Width(styled); sw > maxKeyW {
			maxKeyW = sw
		}
	}
	maxKeyW += 1 // gap between key and desc

	// Build each item as "key  desc" with consistent spacing
	type renderedItem struct {
		text string
		width int
	}
	var rendered []renderedItem
	for _, it := range items {
		styledKey := lipgloss.NewStyle().Foreground(styles.ColorAccent).Render(it.key)
		pad := maxKeyW - lipgloss.Width(styledKey)
		text := styledKey + strings.Repeat(" ", pad) + styles.StyleHelp.Render(it.desc)
		rendered = append(rendered, renderedItem{text: text, width: lipgloss.Width(text)})
	}

	// Calculate column layout — tight fixed gap, panel fills terminal width.
	gapW := 4 // spaces between columns (fixed, never stretched)
	maxColWidth := 0
	for _, r := range rendered {
		if r.width > maxColWidth {
			maxColWidth = r.width
		}
	}
	colWidth := maxColWidth + gapW

	// Available width for content (panel padding + border)
	availW := w - 6

	// Max columns that fit — never stretch spacing to fill.
	numCols := availW / colWidth
	if numCols < 1 {
		numCols = 1
	}
	if numCols > len(rendered) {
		numCols = len(rendered)
	}

	// Calculate number of rows needed
	numRows := (len(rendered) + numCols - 1) / numCols

	// Build rows
	var lines []string
	for row := 0; row < numRows; row++ {
		var parts []string
		for col := 0; col < numCols; col++ {
			idx := col*numRows + row // column-major ordering (like Neovim)
			if idx >= len(rendered) {
				parts = append(parts, "")
				continue
			}
			it := rendered[idx]
			// Pad to column width
			pad := colWidth - it.width
			if pad < 0 {
				pad = 0
			}
			parts = append(parts, it.text+strings.Repeat(" ", pad))
		}
		lines = append(lines, "  "+strings.Join(parts, ""))
	}

	content := strings.Join(lines, "\n")

	// Breadcrumb line at top when in sub-menu
	if breadcrumb != "" {
		bc := styles.StyleInputLabel.Render(strings.TrimRight(breadcrumb, " ") + " →")
		content = "  " + bc + "\n" + content
	}

	// Wrap in a bordered panel — always fill terminal width
	panelStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.ColorMuted).
		Padding(0, 1).
		Width(w)

	return panelStyle.Render(content)
}

// PanelHeight returns the number of terminal lines the panel will occupy
// at the given terminal width. Used by the caller to reserve space.
func (m *WhichKeyModel) PanelHeight(terminalWidth int) int {
	if m.state == stateIdle {
		return 0
	}
	panel := m.View(terminalWidth)
	if panel == "" {
		return 0
	}
	return strings.Count(panel, "\n") + 1
}
