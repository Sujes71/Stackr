package whichkey

import tea "github.com/charmbracelet/bubbletea"

// CommandKey represents a single keybinding in the which-key panel.
// Children is non-empty for prefix keys (multi-key sequences like "d d").
type CommandKey struct {
	Key      string       // "a", "d d", "r v", "enter"
	Icon     string       // optional: "⚡", "🔧" (empty = no icon)
	Desc     string       // "add service", "delete svc"
	Children []CommandKey // non-empty = prefix key (sub-menu)
	Action   tea.Cmd      // nil for prefix-only keys
}

// CommandGroup is a labeled section of commands shown side-by-side.
type CommandGroup struct {
	Label string
	Items []CommandKey
}
