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
	Message   string // short summary for table row
	Detail    string // full error text for detail view (empty = same as Message)
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
	r.AddWithDetail(context, message, "")
}

// AddWithDetail registers an error with both a short summary (for table rows)
// and a full detail message (for the expanded detail view).
func (r *ErrorRegistry) AddWithDetail(context, message, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	entry := AppError{
		ID:        r.nextID,
		Timestamp: time.Now(),
		Context:   context,
		Message:   message,
		Detail:    detail,
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
	profileName string // profile to return to on esc
	envName     string // env to return to on esc
}

// NewErrorList creates an error list view from a snapshot of errors.
func NewErrorList(errors []AppError, profileName, envName string) *ErrorListModel {
	vp := viewport.New(0, 0) // sized later via SetSize
	vp.Style = lipgloss.NewStyle().PaddingLeft(0)
	return &ErrorListModel{
		errors:      errors,
		viewport:    vp,
		profileName: profileName,
		envName:     envName,
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
	m.height = max(0, h-4) // App chrome: header + separator + footer
	m.viewport.Width = w
	m.viewport.Height = m.height
}

func (m *ErrorListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, errorKeys.esc):
			if m.showDetail {
				m.showDetail = false
				m.viewport.GotoTop()
				return m, nil
			}
			return m, func() tea.Msg {
				return NavigateTo{
					Screen:      NavProfileView,
					ProfileName: m.profileName,
					EnvName:     m.envName,
				}
			}

		case key.Matches(msg, errorKeys.up):
			if m.showDetail {
				m.showDetail = false
				m.viewport.GotoTop()
			} else if m.cursor > 0 {
				m.cursor--
			}
			m.scrollCursorIntoView()
			return m, nil

		case key.Matches(msg, errorKeys.down):
			if m.showDetail {
				m.showDetail = false
				m.viewport.GotoTop()
			} else if m.cursor < len(m.errors)-1 {
				m.cursor++
			}
			m.scrollCursorIntoView()
			return m, nil

		case key.Matches(msg, errorKeys.enter):
			if len(m.errors) > 0 {
				m.showDetail = !m.showDetail
				if m.showDetail {
					m.viewport.GotoTop()
				}
			}
			return m, nil
		}
	}

	return m, nil
}

// scrollCursorIntoView adjusts the viewport's Y offset so the cursor row is visible.
// Row layout: separator(1) + header(1) + separator(1) + error rows (1 per error).
func (m *ErrorListModel) scrollCursorIntoView() {
	cursorRow := m.cursor + 3 // +3 for the 3 header lines above the first error
	visible := m.height

	if cursorRow < m.viewport.YOffset {
		m.viewport.YOffset = cursorRow
	} else if cursorRow >= m.viewport.YOffset+visible {
		m.viewport.YOffset = cursorRow - visible + 1
	}
}

func (m *ErrorListModel) View() string {
	if len(m.errors) == 0 {
		return styles.StyleHelp.Render("\n  No errors recorded\n")
	}

	// Available content width (terminal width minus 2 indent on each side).
	avail := m.width - 4
	if avail < 20 {
		avail = 20
	}

	// Build the full content as a single string, every line truncated to avail.
	var content strings.Builder

	// Separator line.
	content.WriteString(strings.Repeat("─", avail))
	content.WriteString("\n")

	// Table header: DATE(10)  CONTEXT(up to 20)  MESSAGE(rest).
	dateW := 10
	ctxW := 20
	overhead := dateW + 2 + ctxW + 2 // column widths + gaps
	msgW := avail - overhead
	if msgW < 10 {
		msgW = 10
	}

	header := fmt.Sprintf("%-*s  %-*s  %s", dateW, "DATE", ctxW, "CONTEXT", "MESSAGE")
	content.WriteString(header)
	content.WriteString("\n")
	content.WriteString(strings.Repeat("─", avail))
	content.WriteString("\n")

	// Render error rows, each line truncated.
	for i, err := range m.errors {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}

		date := err.Timestamp.Format("15:04:05")

		ctx := err.Context
		if len(ctx) > ctxW {
			ctx = ctx[:ctxW-3] + "..."
		}

		msg := strings.ReplaceAll(err.Message, "\n", " ")
		msg = strings.Join(strings.Fields(msg), " ")
		if len(msg) > msgW {
			msg = msg[:msgW-3] + "..."
		}

		row := fmt.Sprintf("%s%-*s  %-*s  %s", cursor, dateW, date, ctxW, ctx, msg)
		// Hard-truncate the entire row to avail (safety net).
		if len(row) > avail {
			row = row[:avail-3] + "..."
		}

		if i == m.cursor {
			content.WriteString(lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true).Render(row))
		} else {
			content.WriteString(row)
		}
		content.WriteString("\n")
	}

	// If detail is shown, append it below the table.
	if m.showDetail && m.cursor < len(m.errors) {
		err := m.errors[m.cursor]
		detail := err.Message
		if err.Detail != "" {
			detail = err.Detail
		}
		content.WriteString("\n")
		content.WriteString(strings.Repeat("─", avail))
		content.WriteString("\n")
		content.WriteString(fmt.Sprintf("[%s] %s\n", err.Timestamp.Format("2006-01-02 15:04:05"), err.Context))
		content.WriteString(strings.Repeat("─", avail))
		content.WriteString("\n")
		// Truncate each detail line to avail.
		for _, line := range strings.Split(detail, "\n") {
			if len(line) > avail {
				line = line[:avail-3] + "..."
			}
			content.WriteString(line)
			content.WriteString("\n")
		}
	}

	m.viewport.SetContent(content.String())

	return m.viewport.View()
}
