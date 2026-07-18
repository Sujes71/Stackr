package views

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/tui/styles"
)

// AppError represents a single registered error with metadata.
type AppError struct {
	ID        int
	Timestamp time.Time
	Context   string // e.g. "add_syncenv", "add_service"
	Message   string
}

// ErrorRegistry is a thread-safe, FIFO-bounded store for application errors.
// It lives on the App model and is written to by tea.Cmd goroutines (hence
// the mutex) and read by the Update loop and views.
type ErrorRegistry struct {
	mu      sync.Mutex
	entries []AppError
	nextID  int
	cap     int
}

// NewErrorRegistry creates a registry with the default capacity of 200.
func NewErrorRegistry() *ErrorRegistry {
	return &ErrorRegistry{
		cap: 200,
	}
}

// Add registers a new error with the given context tag and message.
// If the registry is at capacity, the oldest entry is evicted (FIFO).
func (r *ErrorRegistry) Add(context, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	entry := AppError{
		ID:        r.nextID,
		Timestamp: time.Now(),
		Context:   context,
		Message:   message,
	}

	if len(r.entries) >= r.cap {
		// Evict oldest (front of slice)
		r.entries = r.entries[1:]
	}
	r.entries = append(r.entries, entry)
}

// All returns a defensive copy of all registered errors, sorted newest-first.
// Mutating the returned slice does not affect the registry.
func (r *ErrorRegistry) All() []AppError {
	r.mu.Lock()
	defer r.mu.Unlock()

	result := make([]AppError, len(r.entries))
	copy(result, r.entries)

	// Sort newest-first by ID (higher ID = newer)
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID > result[j].ID
	})
	return result
}

// Len returns the current number of registered errors.
func (r *ErrorRegistry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// ErrorListModel displays registered errors in a table with expandable detail.
type ErrorListModel struct {
	errors     []AppError
	cursor     int
	width      int
	height     int
	viewport   viewport.Model
	showDetail bool
}

// NewErrorList creates an error list view from a snapshot of errors.
func NewErrorList(errors []AppError) *ErrorListModel {
	vp := viewport.New(0, 0) // sized later via SetSize
	vp.Style = lipgloss.NewStyle().PaddingLeft(2)
	return &ErrorListModel{
		errors:   errors,
		viewport: vp,
	}
}

var errorKeys = struct {
	up, down, enter, esc key.Binding
}{
	up:    key.NewBinding(key.WithKeys("up")),
	down:  key.NewBinding(key.WithKeys("down")),
	enter: key.NewBinding(key.WithKeys("enter")),
	esc:   key.NewBinding(key.WithKeys("esc")),
}

func (m *ErrorListModel) Init() tea.Cmd { return nil }

func (m *ErrorListModel) SetSize(w, h int) {
	m.width = w
	m.height = max(0, h-5) // App chrome
	// Viewport takes bottom 40% of available space when showing detail
	vpHeight := int(float64(m.height) * 0.4)
	m.viewport.Width = w - 4
	m.viewport.Height = max(0, vpHeight-2)
}

func (m *ErrorListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, errorKeys.esc):
			return m, func() tea.Msg { return NavigateTo{Screen: NavHome} }

		case key.Matches(msg, errorKeys.up):
			if m.showDetail {
				m.showDetail = false
			} else if m.cursor > 0 {
				m.cursor--
			}
			return m, nil

		case key.Matches(msg, errorKeys.down):
			if m.showDetail {
				m.showDetail = false
			} else if m.cursor < len(m.errors)-1 {
				m.cursor++
			}
			return m, nil

		case key.Matches(msg, errorKeys.enter):
			if len(m.errors) > 0 {
				m.showDetail = !m.showDetail
				if m.showDetail && m.cursor < len(m.errors) {
					err := m.errors[m.cursor]
					detail := fmt.Sprintf("[%s] %s\n\n%s",
						err.Timestamp.Format("2006-01-02 15:04:05"),
						err.Context,
						err.Message)
					m.viewport.SetContent(detail)
				}
			}
			return m, nil
		}
	}

	// Handle viewport scrolling when detail is shown
	if m.showDetail {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m *ErrorListModel) View() string {
	if len(m.errors) == 0 {
		return styles.StyleHelp.Render("\n  No errors yet\n")
	}

	var b strings.Builder

	// Table header
	header := fmt.Sprintf("  %-20s %-20s %s", "DATE", "CONTEXT", "MESSAGE")
	b.WriteString(lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true).Render(header))
	b.WriteString("\n")
	b.WriteString(styles.StyleHelp.Render("  " + strings.Repeat("─", m.width-4)))
	b.WriteString("\n")

	// Calculate visible rows (reserve space for detail if shown)
	tableHeight := m.height
	if m.showDetail {
		tableHeight = m.height - m.viewport.Height - 3
	}
	if tableHeight < 3 {
		tableHeight = 3
	}

	// Render error rows
	startRow := 0
	if m.cursor >= tableHeight {
		startRow = m.cursor - tableHeight + 1
	}

	for i := startRow; i < len(m.errors) && i-startRow < tableHeight; i++ {
		err := m.errors[i]
		cursor := "  "
		if i == m.cursor {
			cursor = "▸ "
		}

		date := err.Timestamp.Format("15:04:05")
		context := err.Context
		msg := err.Message
		if len(msg) > 50 {
			msg = msg[:47] + "..."
		}

		row := fmt.Sprintf("%s%-20s %-20s %s", cursor, date, context, msg)
		if i == m.cursor {
			b.WriteString(lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true).Render(row))
		} else {
			b.WriteString(row)
		}
		b.WriteString("\n")
	}

	// Show detail viewport if expanded
	if m.showDetail && m.cursor < len(m.errors) {
		b.WriteString("\n")
		b.WriteString(styles.StyleHelp.Render("  ── Error Detail ──"))
		b.WriteString("\n")
		b.WriteString(m.viewport.View())
	}

	// Footer help
	b.WriteString("\n")
	b.WriteString(styles.InlineHelp(
		styles.HelpItem("↑ ↓", "navigate"),
		styles.HelpItem("↵", "expand"),
		styles.HelpItem("esc", "back"),
	))

	return b.String()
}
