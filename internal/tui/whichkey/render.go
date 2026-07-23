package whichkey

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/tui/styles"
)

// itemsPerRow is the max number of key hints per line before wrapping.
const itemsPerRow = 5

// View renders the which-key floating panel. Returns empty string when idle.
// The panel is a compact, borderless, left-aligned row-major grid — keys wrap
// every itemsPerRow items for uniform density across all screens.
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
		if keyStr == "enter" {
			keyStr = "↵"
		}
		if keyStr == " " {
			keyStr = "⎵"
		}
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

	// Build each item as "key  desc" with consistent key alignment.
	type renderedItem struct {
		text  string
		width int
	}
	globalMaxW := 0
	var rendered []renderedItem
	for _, it := range items {
		styledKey := lipgloss.NewStyle().Foreground(styles.ColorAccent).Render(it.key)
		pad := maxKeyW - lipgloss.Width(styledKey)
		text := styledKey + strings.Repeat(" ", pad) + styles.StyleHelp.Render(it.desc)
		w := lipgloss.Width(text)
		if w > globalMaxW {
			globalMaxW = w
		}
		rendered = append(rendered, renderedItem{text: text, width: w})
	}
	// Pad all items to globalMaxW so columns align vertically across rows.
	for i := range rendered {
		if d := globalMaxW - rendered[i].width; d > 0 {
			rendered[i].text += strings.Repeat(" ", d)
		}
	}

	// Row-major layout: fill rows left-to-right, wrapping every itemsPerRow.
	// No intra-row padding — uniform gap only, so spacing is even regardless
	// of item width differences.
	gapW := 2 // fixed gap between items
	var lines []string
	for start := 0; start < len(rendered); start += itemsPerRow {
		end := start + itemsPerRow
		if end > len(rendered) {
			end = len(rendered)
		}
		row := rendered[start:end]
		var parts []string
		for _, it := range row {
			parts = append(parts, it.text)
		}
		lines = append(lines, "  "+strings.Join(parts, strings.Repeat(" ", gapW)))
	}

	content := strings.Join(lines, "\n")

	// Breadcrumb line at top when in sub-menu
	if breadcrumb != "" {
		bc := styles.StyleInputLabel.Render(strings.TrimRight(breadcrumb, " ") + " →")
		content = "  " + bc + "\n" + content
	}

	// Borderless, left-aligned — no full-width stretch.
	return content
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
