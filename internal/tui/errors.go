package tui

import (
	"sort"
	"sync"
	"time"
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
