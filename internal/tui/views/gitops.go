package views

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/git"
	"github.com/stackr/stackr/internal/tui/styles"
)

// ── Internal screen enum ──────────────────────────────────────────────────────

type gitScreen int

const (
	gitScreenMain   gitScreen = iota // Two-panel: file list + diff viewer
	gitScreenCommit                  // Commit input overlay
	gitScreenBranch                  // Branch list overlay
)

// ── Message types ─────────────────────────────────────────────────────────────

type gitOpsStatusMsg struct {
	files []git.FileStatus
	err   error
}

type gitOpsDiffMsg struct {
	output string
	err    error
}

type gitOpsDoneMsg struct {
	result string
	err    error
}

type gitOpsBranchMsg struct {
	branches []git.BranchInfo
	err      error
}

type gitOpsRepoCheckMsg struct {
	isRepo   bool
	gitRoot  string
	gitFound bool
}

// ── Model ─────────────────────────────────────────────────────────────────────

// GitOpsModel implements a lazygit-style two-panel Git operations view.
type GitOpsModel struct {
	cfg      *config.Config
	profile  *config.Profile
	dir      string // launching directory for git ops
	isRepo   bool
	gitRoot  string
	gitFound bool

	// Screen state
	screen gitScreen

	// File list (merged staged + unstaged)
	files       []git.FileStatus
	fileCursor  int
	stagedStart int // index where staged files end / unstaged begin
	activePanel int // 0 = unstaged, 1 = staged

	// Diff viewer
	diffOutput string
	diffVP     viewport.Model
	diffFile   string // currently displayed diff path
	diffStaged bool   // is the current diff for a staged file?

	// Panel focus: 0 = left (file list), 1 = right (diff)
	focusPanel int

	// Commit wizard
	commitInput textinput.Model

	// Branch switch
	branches     []git.BranchInfo
	branchCursor int

	// Push state
	hasUnpushed bool

	// Stash state
	hasStash bool // true if there's at least one stash entry

	// Spinner / pending ops
	pendingOps   map[string]string
	pendingStart time.Time // when the current pending op started
	spinnerFrame int
	doneOp       string
	doneOpAt     time.Time

	// Dimensions
	width  int
	height int
}

// ── Constructor ───────────────────────────────────────────────────────────────

func NewGitOps(cfg *config.Config, p *config.Profile, cwd string) *GitOpsModel {
	ci := textinput.New()
	ci.Placeholder = "commit message"
	ci.CharLimit = 200
	ci.Width = 60

	return &GitOpsModel{
		cfg:          cfg,
		profile:      p,
		dir:          cwd,
		pendingOps:   make(map[string]string),
		pendingStart: time.Now(), // initialize so first spinner delay works
		commitInput:  ci,
		diffVP:       viewport.New(0, 0),
	}
}

// ── Getters ───────────────────────────────────────────────────────────────────

func (m *GitOpsModel) ProfileName() string { return m.profile.Name }

func (m *GitOpsModel) IsBusy() bool { return len(m.pendingOps) > 0 }

func (m *GitOpsModel) SetSize(w, h int) {
	m.width = w
	m.height = max(0, h-5)
	// Left panel: ~40%, Right panel: ~60%
	leftW := w * 40 / 100
	rightW := w - leftW
	m.diffVP.Width = max(0, rightW-4)
	m.diffVP.Height = max(8, m.height-13)
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m *GitOpsModel) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			if _, err := exec.LookPath("git"); err != nil {
				return gitOpsRepoCheckMsg{isRepo: false, gitFound: false}
			}
			isRepo := git.IsRepo(m.dir)
			root := ""
			if isRepo {
				root, _ = git.Root(m.dir)
			}
			return gitOpsRepoCheckMsg{isRepo: isRepo, gitRoot: root, gitFound: true}
		},
		func() tea.Msg {
			files, err := git.StatusPorcelain(m.dir)
			return gitOpsStatusMsg{files: files, err: err}
		},
		spinnerCmd(),
	)
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m *GitOpsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = max(0, msg.Height-5)
		leftW := msg.Width * 40 / 100
		rightW := msg.Width - leftW
		m.diffVP.Width = max(0, rightW-4)
		m.diffVP.Height = max(8, m.height-13)
		return m, nil

	case SpinnerTickMsg:
		if len(m.pendingOps) > 0 {
			// Only show spinner after 200ms delay — fast ops (stage/unstage) won't flash
			if time.Since(m.pendingStart) > 200*time.Millisecond {
				m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			}
			return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return SpinnerTickMsg{}
			})
		}
		m.spinnerFrame = 0

	case gitOpsRepoCheckMsg:
		m.isRepo = msg.isRepo
		m.gitRoot = msg.gitRoot
		m.gitFound = msg.gitFound

	case gitOpsStatusMsg:
		if msg.err != nil {
			m.doneOp = msg.err.Error()
			m.doneOpAt = time.Now()
		} else {
			m.files = msg.files
			m.buildFilePanels()
			m.hasUnpushed = git.HasUnpushedCommits(m.dir)
			stashes, _ := git.StashList(m.dir)
			m.hasStash = len(stashes) > 0
			m.doneOp = ""
			m.doneOpAt = time.Time{}
		}
		// Clean up whichever pending key triggered this status refresh
		delete(m.pendingOps, "stage")
		delete(m.pendingOps, "unstage")
		delete(m.pendingOps, "commit")
		delete(m.pendingOps, "push")
		delete(m.pendingOps, "pull")
		delete(m.pendingOps, "stash")
		delete(m.pendingOps, "status")
		// Auto-fetch diff for the currently selected file
		return m, m.cmdFetchDiff()

	case gitOpsDiffMsg:
		if msg.err != nil {
			m.diffOutput = ""
			m.diffVP.SetContent("")
		} else {
			m.diffOutput = colorDiff(msg.output)
			m.diffVP.SetContent(m.diffOutput)
			m.diffVP.GotoTop()
		}
		delete(m.pendingOps, "diff")

	case gitOpsDoneMsg:
		if msg.err != nil {
			m.doneOp = msg.err.Error()
			m.doneOpAt = time.Now()
		} else {
			m.doneOp = msg.result
			m.doneOpAt = time.Now()
		}
		m.pendingOps = make(map[string]string)
		m.pendingStart = time.Time{} // reset so next op shows spinner after 200ms

	case gitOpsBranchMsg:
		if msg.err != nil {
			m.doneOp = msg.err.Error()
			m.doneOpAt = time.Now()
		} else {
			m.branches = msg.branches
			if m.branchCursor >= len(m.branches) {
				m.branchCursor = max(0, len(m.branches)-1)
			}
		}
		delete(m.pendingOps, "branches")
	}

	// Route to active sub-screen
	switch m.screen {
	case gitScreenMain:
		return m.updateMain(msg)
	case gitScreenCommit:
		return m.updateCommit(msg)
	case gitScreenBranch:
		return m.updateBranch(msg)
	}

	return m, nil
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m *GitOpsModel) View() string {
	switch m.screen {
	case gitScreenMain:
		return m.viewMain()
	case gitScreenCommit:
		return m.viewCommit()
	case gitScreenBranch:
		return m.viewBranch()
	}
	return ""
}

// ── File panel builder ────────────────────────────────────────────────────────

// buildFilePanels splits the file list into staged first, then unstaged.
// It also adjusts the cursor to stay on a sensible file after list changes.
func (m *GitOpsModel) buildFilePanels() {
	// Remember what file was under cursor before re-sorting
	oldPath := ""
	oldStaged := false
	if f := m.currentFile(); f != nil {
		oldPath = f.Path
		oldStaged = f.Staged
	}

	// Separate staged and unstaged, then merge: staged first
	var staged, unstaged []git.FileStatus
	for _, f := range m.files {
		if f.Staged {
			staged = append(staged, f)
		} else {
			unstaged = append(unstaged, f)
		}
	}
	m.files = append(staged, unstaged...)
	m.stagedStart = len(staged)

	// Try to keep cursor on the same file
	if oldPath != "" {
		for i, f := range m.files {
			if f.Path == oldPath && f.Staged == oldStaged {
				m.fileCursor = i
				return
			}
		}
		// File gone (was staged and is now committed, or unstaged and now staged)
		// Try same path with flipped staged status
		for i, f := range m.files {
			if f.Path == oldPath {
				m.fileCursor = i
				return
			}
		}
	}

	// Fallback: clamp cursor
	if m.fileCursor >= len(m.files) {
		m.fileCursor = max(0, len(m.files)-1)
	}
}

// currentFile returns the file under the cursor, or nil.
func (m *GitOpsModel) currentFile() *git.FileStatus {
	if m.fileCursor < 0 || m.fileCursor >= len(m.files) {
		return nil
	}
	return &m.files[m.fileCursor]
}

// ── Diff auto-refresh ─────────────────────────────────────────────────────────

// cmdFetchDiff returns a tea.Cmd that fetches the diff for the currently
// selected file. If the file or cursor is invalid, returns nil.
func (m *GitOpsModel) cmdFetchDiff() tea.Cmd {
	f := m.currentFile()
	if f == nil {
		return nil
	}
	// Avoid refetching if already showing this file
	if f.Path == m.diffFile && f.Staged == m.diffStaged {
		return nil
	}
	m.diffFile = f.Path
	m.diffStaged = f.Staged
	path := f.Path
	staged := f.Staged
	statusCode := f.StatusCode
	m.pendingOps["diff"] = "loading diff"
	return tea.Batch(
		func() tea.Msg {
			var output string
			var err error
			switch {
			case staged:
				output, err = git.Diff(m.dir, path, true)
			case statusCode == '?':
				// Untracked file: show full content as added lines
				output, err = git.DiffNew(m.dir, path)
			default:
				output, err = git.DiffFile(m.dir, path)
			}
			return gitOpsDiffMsg{output: output, err: err}
		},
		spinnerCmd(),
	)
}

// ── Pending result display ────────────────────────────────────────────────────

func (m *GitOpsModel) renderPendingOrDone() string {
	for op := range m.pendingOps {
		if op == "diff" {
			continue // don't show spinner for diff (it's the right panel)
		}
		// Only show spinner after 200ms — fast ops (stage/unstage) won't flash
		if time.Since(m.pendingStart) > 200*time.Millisecond {
			return "\n" + opsRenderSpinnerDot(m.spinnerFrame, op+"...")
		}
		return "" // too fast, skip spinner — doneOp will show result
	}
	if m.doneOp != "" && time.Since(m.doneOpAt) < 4*time.Second {
		if strings.HasPrefix(m.doneOp, "✗") {
			return "\n" + formatErrorMsg(m.doneOp)
		}
		return "\n" + styles.StyleSuccess.Render("  " + m.doneOp)
	}
	return ""
}

// ── Main Screen Update ────────────────────────────────────────────────────────

func (m *GitOpsModel) updateMain(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {

		// Global keys (always work regardless of focus)
		case msg.String() == "esc", msg.String() == "q":
			return m, func() tea.Msg {
				return NavigateTo{Screen: NavProfileView, ProfileName: m.profile.Name}
			}

		case msg.String() == "left":
			m.focusPanel = 0
			return m, nil
		case msg.String() == "right":
			m.focusPanel = 1
			return m, nil

		// Space: stage/unstage (always in left panel context)
		case msg.String() == " ":
			f := m.currentFile()
			if f == nil {
				break
			}
			path := f.Path
			m.pendingStart = time.Now()
			if f.Staged {
				m.pendingOps["unstage"] = "unstaging"
				return m, tea.Batch(
					func() tea.Msg {
						err := git.Unstage(m.dir, path)
						if err != nil {
							return gitOpsDoneMsg{err: err}
						}
						files, err := git.StatusPorcelain(m.dir)
						return gitOpsStatusMsg{files: files, err: err}
					},
					spinnerCmd(),
				)
			}
			m.pendingOps["stage"] = "staging"
			return m, tea.Batch(
				func() tea.Msg {
					err := git.Stage(m.dir, path)
					if err != nil {
						return gitOpsDoneMsg{err: err}
					}
					files, err := git.StatusPorcelain(m.dir)
					return gitOpsStatusMsg{files: files, err: err}
				},
				spinnerCmd(),
			)

		// Option+Space: stage all or unstage all
		case msg.String() == "alt+ ":
			m.pendingStart = time.Now()
			allStaged := m.stagedStart > 0 && m.stagedStart >= len(m.files)
			if allStaged {
				m.pendingOps["unstage"] = "unstaging all"
				return m, tea.Batch(
					func() tea.Msg {
						err := git.UnstageAll(m.dir)
						if err != nil {
							return gitOpsDoneMsg{err: err}
						}
						files, err := git.StatusPorcelain(m.dir)
						return gitOpsStatusMsg{files: files, err: err}
					},
					spinnerCmd(),
				)
			}
			m.pendingOps["stage"] = "staging all"
			return m, tea.Batch(
				func() tea.Msg {
					err := git.StageAll(m.dir)
					if err != nil {
						return gitOpsDoneMsg{err: err}
					}
					files, err := git.StatusPorcelain(m.dir)
					return gitOpsStatusMsg{files: files, err: err}
				},
				spinnerCmd(),
			)

		case msg.String() == "c":
			if m.stagedStart == 0 {
				break // no staged changes — silently ignore
			}
			m.screen = gitScreenCommit
			m.commitInput.SetValue("")
			m.commitInput.Placeholder = "commit message"
			m.commitInput.Focus()
			return m, textinput.Blink

		case msg.String() == "b":
			m.screen = gitScreenBranch
			m.branchCursor = 0
			if len(m.branches) == 0 {
				m.pendingOps["branches"] = "loading"
				return m, tea.Batch(
					func() tea.Msg {
						branches, err := git.Branches(m.dir)
						return gitOpsBranchMsg{branches: branches, err: err}
					},
					spinnerCmd(),
				)
			}
			return m, nil

		case msg.String() == "p":
			if m.hasUnpushed {
				// Push
				m.pendingStart = time.Now()
				m.pendingOps["push"] = "pushing"
				return m, tea.Batch(
					func() tea.Msg {
						err := git.Push(m.dir)
						if err != nil {
							return gitOpsDoneMsg{err: err}
						}
						files, _ := git.StatusPorcelain(m.dir)
						return gitOpsStatusMsg{files: files}
					},
					spinnerCmd(),
				)
			}
			if len(m.files) == 0 {
				// Pull (only when working tree is clean)
				m.pendingStart = time.Now()
				m.pendingOps["pull"] = "pulling"
				return m, tea.Batch(
					func() tea.Msg {
						err := git.Pull(m.dir)
						if err != nil {
							return gitOpsDoneMsg{err: err}
						}
						files, _ := git.StatusPorcelain(m.dir)
						return gitOpsStatusMsg{files: files}
					},
					spinnerCmd(),
				)
			}

		case msg.String() == "s":
			if len(m.files) > 0 {
				// Stash changes
				m.pendingStart = time.Now()
				m.pendingOps["stash"] = "stashing"
				return m, tea.Batch(
					func() tea.Msg {
						err := git.StashCreate(m.dir, "")
						if err != nil {
							return gitOpsDoneMsg{err: err}
						}
						files, _ := git.StatusPorcelain(m.dir)
						return gitOpsStatusMsg{files: files}
					},
					spinnerCmd(),
				)
			}
			if m.hasStash {
				// Pop stash — all changes come back unstaged
				m.pendingStart = time.Now()
				m.pendingOps["stash"] = "popping stash"
				return m, tea.Batch(
					func() tea.Msg {
						err := git.StashPop(m.dir)
						if err != nil {
							return gitOpsDoneMsg{err: err}
						}
						// Ensure nothing stays staged after pop
						git.UnstageAll(m.dir)
						files, _ := git.StatusPorcelain(m.dir)
						return gitOpsStatusMsg{files: files}
					},
					spinnerCmd(),
				)
			}

		// Arrow keys: route to focused panel
		case msg.String() == "up":
			if m.focusPanel == 0 {
				// Left panel: navigate files
				if m.fileCursor > 0 {
					m.fileCursor--
					return m, m.cmdFetchDiff()
				}
			} else {
				// Right panel: scroll diff
				m.diffVP.LineUp(3)
			}
			return m, nil

		case msg.String() == "down":
			if m.focusPanel == 0 {
				// Left panel: navigate files
				if m.fileCursor < len(m.files)-1 {
					m.fileCursor++
					return m, m.cmdFetchDiff()
				}
			} else {
				// Right panel: scroll diff
				m.diffVP.LineDown(3)
			}
			return m, nil

		case opsIsJumpTop(msg):
			if m.focusPanel == 0 {
				m.fileCursor = 0
				return m, m.cmdFetchDiff()
			}
			m.diffVP.GotoTop()
			return m, nil

		case opsIsJumpBottom(msg):
			if m.focusPanel == 0 {
				if len(m.files) > 0 {
					m.fileCursor = len(m.files) - 1
				}
				return m, m.cmdFetchDiff()
			}
			m.diffVP.GotoBottom()
			return m, nil
		}
	}
	return m, nil
}

// ── Main Screen View ──────────────────────────────────────────────────────────

func (m *GitOpsModel) viewMain() string {
	w := m.width

	// Layout dimensions
	leftW := w * 40 / 100
	rightW := w - leftW

	// Header
	title := styles.StyleHeader.Render("  Git Ops — " + m.profile.Name)

	if !m.isRepo {
		var b strings.Builder
		b.WriteString("\n")
		b.WriteString(title)
		b.WriteString("\n\n")
		if !m.gitFound {
			b.WriteString("  " + styles.StyleError.Render("Git is not installed"))
		} else {
			b.WriteString("  " + styles.StyleError.Render("This directory is not a git repository"))
		}
		b.WriteString("\n\n")
		b.WriteString(styles.BackHelp())
		return b.String()
	}

	// Clean tree: simple centered message, no panels
	if len(m.files) == 0 {
		var b strings.Builder
		b.WriteString("\n")
		b.WriteString(title)
		b.WriteString("\n\n")
		b.WriteString("  " + styles.StyleSubtitle.Render("Working tree clean"))
		b.WriteString("\n\n")
		b.WriteString(m.renderCleanFooter())
		return b.String()
	}

	// Left panel: file list
	leftPanel := m.renderFileList(leftW - 2) // -2 for padding

	// Right panel: diff viewer
	rightPanel := m.renderDiffPanel(rightW - 2)

	// Vertical separator — match the tallest panel height
	leftH := lipgloss.Height(leftPanel)
	rightH := lipgloss.Height(rightPanel)
	sepH := leftH
	if rightH > sepH {
		sepH = rightH
	}
	sep := lipgloss.NewStyle().
		Foreground(styles.ColorMuted).
		Render(strings.Repeat("│\n", sepH))

	// Join panels horizontally
	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, sep, rightPanel)

	// Footer help
	// Footer help — adapts to current cursor state
	hasStaged := m.stagedStart > 0
	allStaged := hasStaged && m.stagedStart >= len(m.files) // no unstaged files
	cursorOnStaged := m.fileCursor < m.stagedStart

	helpItems := []styles.HelpItemResult{
		styles.HelpItem("↑↓", "nav"),
	}
	if cursorOnStaged {
		helpItems = append(helpItems, styles.HelpItem("space", "unstage"))
	} else {
		helpItems = append(helpItems, styles.HelpItem("space", "stage"))
	}
	if allStaged {
		helpItems = append(helpItems, styles.HelpItem("⌥space", "unstage all"))
	} else {
		helpItems = append(helpItems, styles.HelpItem("⌥space", "stage all"))
	}
	helpItems = append(helpItems, styles.HelpItem("←→", "switch"))
	if hasStaged {
		helpItems = append(helpItems, styles.HelpItem("c", "commit"))
	}
	if m.hasUnpushed {
		helpItems = append(helpItems, styles.HelpItem("p", "push"))
	}
	helpItems = append(helpItems, styles.HelpItem("s", "stash"))
	helpItems = append(helpItems, styles.HelpItem("b", "branch"))
	help := styles.BackHelp(helpItems...)

	return "\n" + title + "\n" + body + m.renderPendingOrDone() + "\n" + help
}

// ── Clean Tree Footer ─────────────────────────────────────────────────────────

func (m *GitOpsModel) renderCleanFooter() string {
	helpItems := []styles.HelpItemResult{}
	if m.hasUnpushed {
		helpItems = append(helpItems, styles.HelpItem("p", "push"))
	} else {
		helpItems = append(helpItems, styles.HelpItem("p", "pull"))
	}
	if m.hasStash {
		helpItems = append(helpItems, styles.HelpItem("s", "stash pop"))
	}
	helpItems = append(helpItems, styles.HelpItem("b", "branch"))
	return styles.BackHelp(helpItems...)
}

// ── File List Panel ───────────────────────────────────────────────────────────

func (m *GitOpsModel) renderFileList(panelW int) string {
	var b strings.Builder

	// Panel header with focus indicator
	if m.focusPanel == 0 {
		b.WriteString(styles.StylePanelFocused.Render("  Files ▸"))
	} else {
		b.WriteString(styles.StyleSubtitle.Render("  Files"))
	}
	b.WriteString("\n")

	if len(m.files) == 0 {
		empty := styles.StyleSubtitle.Render("  Working tree clean")
		b.WriteString(empty)
		return b.String()
	}

	// Section headers
	hasStaged := m.stagedStart > 0
	hasUnstaged := m.stagedStart < len(m.files)

	// Windowed rendering
	overhead := 4 // section headers + padding
	win := opsWindow(len(m.files), m.fileCursor, 1, m.height, overhead)

	for i := win.start; i < win.end; i++ {
		f := m.files[i]

		// Section header: Staged
		if i == 0 && hasStaged {
			b.WriteString(styles.StyleInputLabel.Render("  Staged"))
			b.WriteString("\n")
		}
		// Section header: Unstaged
		if i == m.stagedStart && hasUnstaged {
			b.WriteString("\n")
			b.WriteString(styles.StyleInputLabel.Render("  Unstaged"))
			b.WriteString("\n")
		}

		// Status indicator
		indicator := m.renderStatusIndicator(f)
		path := truncatePath(f.Path, panelW-8)

		if i == m.fileCursor {
			b.WriteString(" " + lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true).Render("▸") + " " + indicator + " " + styles.StyleCode.Render(path))
		} else {
			b.WriteString("   " + indicator + " " + styles.StyleCode.Render(path))
		}
		b.WriteString("\n")
	}

	// Scroll indicators
	if win.hasAbove {
		b.WriteString(styles.StyleSubtitle.Render("  ↑"))
		b.WriteString("\n")
	}
	if win.hasBelow {
		b.WriteString(styles.StyleSubtitle.Render("  ↓"))
	}

	return b.String()
}

// ── Diff Panel ────────────────────────────────────────────────────────────────

func (m *GitOpsModel) renderDiffPanel(panelW int) string {
	var b strings.Builder

	if len(m.files) == 0 {
		empty := styles.StyleSubtitle.Render("  No changes to display")
		b.WriteString(empty)
		return b.String()
	}

	// Diff header showing which file
	f := m.currentFile()
	if f != nil {
		header := f.Path
		if f.Staged {
			header += " (staged)"
		}
		if m.focusPanel == 1 {
			b.WriteString(styles.StylePanelFocused.Render("  " + header + " ▸"))
		} else {
			b.WriteString(styles.StyleSubtitle.Render("  " + header))
		}
		b.WriteString("\n")
	}

	// Diff content in viewport
	b.WriteString(m.diffVP.View())

	return b.String()
}

// ── Status indicator coloring ─────────────────────────────────────────────────

func (m *GitOpsModel) renderStatusIndicator(f git.FileStatus) string {
	code := string([]byte{f.StatusCode})
	switch f.StatusCode {
	case 'M':
		return lipgloss.NewStyle().Foreground(styles.ColorSecondary).Bold(true).Render(code)
	case 'A':
		return lipgloss.NewStyle().Foreground(styles.ColorSecondary).Bold(true).Render(code)
	case 'D':
		return lipgloss.NewStyle().Foreground(styles.ColorError).Bold(true).Render(code)
	case '?':
		return lipgloss.NewStyle().Foreground(styles.ColorMuted).Render(code)
	case 'C':
		return lipgloss.NewStyle().Foreground(styles.ColorCode).Bold(true).Render(code)
	case 'U':
		return lipgloss.NewStyle().Foreground(styles.ColorError).Bold(true).Render(code)
	case 'R':
		return lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true).Render(code)
	default:
		return lipgloss.NewStyle().Foreground(styles.ColorMuted).Render(code)
	}
}

// ── Commit Screen ─────────────────────────────────────────────────────────────

func (m *GitOpsModel) updateCommit(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			m.screen = gitScreenMain
			m.commitInput.Blur()
			return m, nil
		case "enter":
			subject := strings.TrimSpace(m.commitInput.Value())
			if subject == "" {
				return m, nil
			}
			// Execute commit
			m.screen = gitScreenMain
			m.pendingOps["commit"] = "committing"
			m.commitInput.Blur()
			msg := subject
			return m, tea.Batch(
				func() tea.Msg {
					err := git.Commit(m.dir, msg, "")
					if err != nil {
						return gitOpsDoneMsg{err: err}
					}
					files, _ := git.StatusPorcelain(m.dir)
					return gitOpsStatusMsg{files: files}
				},
				spinnerCmd(),
			)
		default:
			var cmd tea.Cmd
			m.commitInput, cmd = m.commitInput.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *GitOpsModel) viewCommit() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.StyleHeader.Render("  Commit"))
	b.WriteString("\n\n")
	b.WriteString("  " + styles.StyleInputLabel.Render("Message:") + "\n")
	b.WriteString("  " + m.commitInput.View())
	b.WriteString("\n\n")
	b.WriteString(styles.InlineHelp(
		styles.HelpItem("esc", "cancel"),
		styles.HelpItem("↵", "commit"),
	))
	return b.String()
}

// ── Branch Screen ─────────────────────────────────────────────────────────────

func (m *GitOpsModel) updateBranch(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			m.screen = gitScreenMain
			return m, nil

		case "up", "k":
			if m.branchCursor > 0 {
				m.branchCursor--
			}

		case "down", "j":
			if m.branchCursor < len(m.branches)-1 {
				m.branchCursor++
			}

		case "enter":
			// Checkout the selected branch
			if m.branchCursor >= len(m.branches) || len(m.branches) == 0 {
				break
			}
			br := m.branches[m.branchCursor]
			if br.Current {
				m.doneOp = "✓ Already on " + br.Name
				m.doneOpAt = time.Now()
				break
			}
			m.pendingOps["checkout"] = "checking out"
			name := br.Name
			return m, tea.Batch(
				func() tea.Msg {
					err := git.CheckoutBranch(m.dir, name)
					if err != nil {
						return gitOpsDoneMsg{err: err}
					}
					branches, _ := git.Branches(m.dir)
					return gitOpsBranchMsg{branches: branches}
				},
				spinnerCmd(),
			)
		}
	}
	return m, nil
}

func (m *GitOpsModel) viewBranch() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.StyleHeader.Render(fmt.Sprintf("  Branches — %d", len(m.branches))))
	b.WriteString("\n\n")

	if len(m.branches) == 0 {
		b.WriteString(styles.StyleSubtitle.Render("  No branches found"))
		b.WriteString(m.renderPendingOrDone())
		b.WriteString("\n\n")
		b.WriteString(styles.BackHelp())
		return b.String()
	}

	overhead := 6
	win := opsWindow(len(m.branches), m.branchCursor, 1, m.height, overhead)
	for i := win.start; i < win.end; i++ {
		br := m.branches[i]
		name := br.Name
		if br.Current {
			name = styles.StyleBadgeEnv.Render(" * " + name + " ")
		} else {
			name = "  " + name
		}
		if br.Tracking != "" {
			name += " " + styles.StyleSubtitle.Render("→ "+br.Tracking)
		}
		if i == m.branchCursor {
			b.WriteString(lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true).Render("▸ ") + name)
		} else {
			b.WriteString("  " + name)
		}
		b.WriteString("\n")
	}

	b.WriteString(m.renderPendingOrDone())
	b.WriteString("\n\n")
	b.WriteString(styles.InlineHelp(
		styles.HelpItem("↑↓", "nav"),
		styles.HelpItem("↵", "checkout"),
		styles.HelpItem("esc", "back"),
	))
	return b.String()
}

// ── Diff coloring ─────────────────────────────────────────────────────────────

// colorDiff applies Kanagawa-inspired coloring to unified diff output.
func colorDiff(raw string) string {
	if raw == "" {
		return styles.StyleSubtitle.Render("  No changes")
	}
	var b strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			b.WriteString(styles.StyleSubtitle.Render(line))
		case strings.HasPrefix(line, "@@"):
			b.WriteString(lipgloss.NewStyle().Foreground(styles.ColorCyan).Render(line))
		case strings.HasPrefix(line, "+"):
			b.WriteString(lipgloss.NewStyle().Foreground(styles.ColorSecondary).Render(line))
		case strings.HasPrefix(line, "-"):
			b.WriteString(lipgloss.NewStyle().Foreground(styles.ColorError).Render(line))
		default:
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// truncatePath truncates a file path to fit within maxW, showing the
// beginning and end with "..." in the middle if needed.
func truncatePath(path string, maxW int) string {
	if maxW <= 0 || lipgloss.Width(path) <= maxW {
		return path
	}
	runes := []rune(path)
	// Try to keep the filename (last component) visible
	lastSlash := -1
	for i, r := range runes {
		if r == '/' {
			lastSlash = i
		}
	}
	if lastSlash < 0 {
		// No slash — just truncate from the left
		start := len(runes) - maxW + 3
		if start < 0 {
			start = 0
		}
		return "..." + string(runes[start:])
	}
	// Keep dir prefix + "..." + filename
	dirPart := runes[:lastSlash+1]
	filePart := runes[lastSlash+1:]
	avail := maxW - 3 // for "..."
	if avail <= 0 {
		return "..."
	}
	dirBudget := avail / 3
	if dirBudget > len(dirPart) {
		dirBudget = len(dirPart)
	}
	fileBudget := avail - dirBudget
	if fileBudget > len(filePart) {
		fileBudget = len(filePart)
	}
	// Ensure we don't exceed the budget
	dirShow := dirPart[:dirBudget]
	fileShow := filePart[len(filePart)-fileBudget:]
	return string(dirShow) + "..." + string(fileShow)
}
