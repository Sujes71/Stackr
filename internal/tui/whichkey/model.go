package whichkey

import tea "github.com/charmbracelet/bubbletea"

// WhichKeyModel manages the which-key floating panel state machine.
type WhichKeyModel struct {
	state      whichKeyState
	prefixPath []string       // breadcrumb for sub-menus (e.g. ["r"])
	commands   []CommandGroup // current level commands
	rootCmds   []CommandGroup // root level commands (for back navigation)
	prefixKey  string         // which prefix key was pressed (for breadcrumb label)
	width      int
	height     int
}

// New creates a which-key model in idle state.
func New() *WhichKeyModel {
	return &WhichKeyModel{
		state: stateIdle,
	}
}

// IsOpen returns true when the panel is visible.
func (m *WhichKeyModel) IsOpen() bool {
	return m.state != stateIdle
}

// Open transitions to root state and loads commands from the active view.
func (m *WhichKeyModel) Open(cmds []CommandGroup) {
	m.state = stateRoot
	m.commands = cmds
	m.rootCmds = cmds
	m.prefixPath = nil
	m.prefixKey = ""
}

// Close transitions back to idle state.
func (m *WhichKeyModel) Close() {
	m.state = stateIdle
	m.commands = nil
	m.rootCmds = nil
	m.prefixPath = nil
	m.prefixKey = ""
}

// SetSize updates the terminal dimensions for rendering.
func (m *WhichKeyModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

// Update processes a key message through the state machine.
// Returns (handled, cmd). If handled is false, the key was unrecognized
// and the caller should close the panel.
func (m *WhichKeyModel) Update(msg tea.Msg) (bool, tea.Cmd) {
	kMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return false, nil
	}

	k := kMsg.String()

	switch m.state {
	case stateRoot:
		return m.handleRoot(k)
	case statePrefix:
		return m.handlePrefix(k)
	}

	return false, nil
}

// handleRoot processes a key press at the root level.
func (m *WhichKeyModel) handleRoot(k string) (bool, tea.Cmd) {
	// Esc at root → close
	if k == "esc" {
		m.Close()
		return true, nil
	}

	// Search all groups for a matching key
	for _, group := range m.commands {
		for _, item := range group.Items {
			if item.Key == k {
				// Prefix key → dive into sub-menu
				if len(item.Children) > 0 {
					m.state = statePrefix
					m.commands = []CommandGroup{
						{Label: item.Desc, Items: item.Children},
					}
					m.prefixPath = append(m.prefixPath, item.Key)
					m.prefixKey = item.Key
					return true, nil
				}
				// Leaf key → close panel, return false so caller forwards the key
				m.Close()
				return false, nil
			}
		}
	}

	// Unrecognized key → close panel, return false so caller forwards the key
	m.Close()
	return false, nil
}

// handlePrefix processes a key press in a prefix sub-menu.
func (m *WhichKeyModel) handlePrefix(k string) (bool, tea.Cmd) {
	// Esc in sub-menu → go back one level
	if k == "esc" {
		if len(m.prefixPath) > 1 {
			// Navigate back through prefix chain (not implemented for 2-level max)
			m.prefixPath = m.prefixPath[:len(m.prefixPath)-1]
		} else {
			// Back to root
			m.state = stateRoot
			m.commands = m.rootCmds
			m.prefixPath = nil
			m.prefixKey = ""
		}
		return true, nil
	}

	// Search the current prefix group for a matching child key
	for _, group := range m.commands {
		for _, item := range group.Items {
			if item.Key == k {
				// Nested prefix → dive deeper
				if len(item.Children) > 0 {
					m.prefixPath = append(m.prefixPath, item.Key)
					m.prefixKey = item.Key
					m.commands = []CommandGroup{
						{Label: item.Desc, Items: item.Children},
					}
					return true, nil
				}
				// Leaf key → close panel, return false so caller forwards the key
				m.Close()
				return false, nil
			}
		}
	}

	// Unrecognized key → go back to root and swallow (user gets a fresh start)
	m.state = stateRoot
	m.commands = m.rootCmds
	m.prefixPath = nil
	m.prefixKey = ""
	return true, nil
}
