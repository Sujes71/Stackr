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
	"github.com/stackr/stackr/internal/tui/whichkey"
)

// ── Internal screen enum ──────────────────────────────────────────────────────

type gitScreen int

const (
	gitScreenMain      gitScreen = iota // Two-panel: file list + diff viewer
	gitScreenCommit                     // Commit input overlay
	gitScreenBranch                     // Branch list overlay
	gitScreenLog                        // Full-screen commit log
	gitScreenLogDetail                  // Commit detail: files + diff
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

type gitOpsLogMsg struct {
	commits []git.CommitInfo
	err     error
}

type gitOpsLastCommitMsg struct {
	msg string
}

type gitOpsCommitFilesMsg struct {
	hash  string
	files []string
	err   error
}

type gitOpsCommitDiffMsg struct {
	output string
	err    error
}

type gitOpsCreateBranchMsg struct {
	branchName string
	err        error
}

type gitOpsMergeMsg struct {
	source string
	err    error
}

type gitOpsDoneExpiredMsg struct{}

// ── Model ─────────────────────────────────────────────────────────────────────

// GitOpsModel implements a lazygit-style two-panel Git operations view.
type GitOpsModel struct {
	cfg      *config.Config
	profile  *config.Profile
	dir      string // launching directory for git ops

	errorRegistry *ErrorRegistry
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
	amendMode   bool // true when editing commit input in amend mode

	// Branch switch
	branches           []git.BranchInfo
	branchCursor       int
	branchCreating     bool           // whether we're in "new branch" prompt mode
	branchInput        textinput.Model // input for new branch name
	branchMergeConfirm bool           // merge confirmation state
	armGeneration      int            // incremented on each arm; timer only fires if generation matches

	// Push state
	hasUnpushed bool

	// Stash state
	hasStash bool // true if there's at least one stash entry

	// Done message error flag (separate from doneOp text)
	doneOpIsErr bool

	// Log viewer
	commits      []git.CommitInfo // recent commits for log view
	logCursor    int              // cursor in log list
	lastCommitMsg string          // last commit message for amend

	// Log detail (drill-down into a commit)
	logDetailHash string          // hash of the commit being viewed
	logFiles      []string        // files changed in the commit
	logFileCursor int             // cursor in file list
	logFileDiff   string          // diff of selected file in commit
	logFileDiffVP viewport.Model  // viewport for commit file diff

	// Spinner / pending ops
	pendingOps    map[string]string
	pendingStart  time.Time // when the current pending op started
	spinnerFrame  int
	spinnerScreen gitScreen // which screen started the current spinner
	doneOp        string
	doneOpAt      time.Time

	// Dimensions
	width  int
	height int
}

// ── Constructor ───────────────────────────────────────────────────────────────

func NewGitOps(cfg *config.Config, p *config.Profile, cwd string, errorRegistry *ErrorRegistry) *GitOpsModel {
	ci := textinput.New()
	ci.Placeholder = "commit message"
	ci.CharLimit = 200
	ci.Width = 60

	bi := textinput.New()
	bi.Placeholder = "branch name"
	bi.CharLimit = 50
	bi.Width = 40

	return &GitOpsModel{
		cfg:            cfg,
		profile:        p,
		dir:            cwd,
		errorRegistry:  errorRegistry,
		pendingOps:     make(map[string]string),
		pendingStart:   time.Now(), // initialize so first spinner delay works
		commitInput:    ci,
		branchInput:    bi,
		diffVP:         viewport.New(0, 0),
	}
}

// ── Getters ───────────────────────────────────────────────────────────────────

func (m *GitOpsModel) ProfileName() string { return m.profile.Name }

func (m *GitOpsModel) IsBusy() bool { return len(m.pendingOps) > 0 }

// HasDirtyFiles returns true when the working tree has staged or unstaged changes.
func (m *GitOpsModel) HasDirtyFiles() bool { return len(m.files) > 0 }

// HasViewportPadding returns true when the current view renders a two-panel
// layout with a viewport that adds internal blank-line padding at the bottom.
// The caller (app.go) uses this to reduce the body→which-key gap from \n\n
// to \n, preventing double-spacing.
func (m *GitOpsModel) HasViewportPadding() bool {
	return (m.screen == gitScreenMain && len(m.files) > 0) || m.screen == gitScreenLogDetail
}

// WhichKeyCommands returns context-aware commands based on the current sub-screen.
func (m *GitOpsModel) WhichKeyCommands() []whichkey.CommandGroup {
	switch m.screen {
	case gitScreenMain:
		var gitItems []whichkey.CommandKey

		// Push / pull
		if m.hasUnpushed {
			gitItems = append(gitItems, whichkey.CommandKey{Key: "p", Desc: "push"})
		} else if len(m.files) == 0 {
			gitItems = append(gitItems, whichkey.CommandKey{Key: "p", Desc: "pull"})
		}

		// Stash / pop
		if len(m.files) > 0 {
			gitItems = append(gitItems, whichkey.CommandKey{Key: "s", Desc: "stash"})
		} else if m.hasStash {
			gitItems = append(gitItems, whichkey.CommandKey{Key: "s", Desc: "pop"})
		}

		gitItems = append(gitItems, whichkey.CommandKey{Key: "b", Desc: "branch"})
		gitItems = append(gitItems, whichkey.CommandKey{Key: "l", Desc: "log"})

		var changesItems []whichkey.CommandKey
		if f := m.currentFile(); f != nil && !f.Staged && f.StatusCode != '?' {
			changesItems = append(changesItems, whichkey.CommandKey{Key: "d", Desc: "discard"})
		}
		if m.stagedStart > 0 {
			changesItems = append(changesItems, whichkey.CommandKey{Key: "c", Desc: "commit"})
		}
		if m.lastCommitMsg != "" {
			changesItems = append(changesItems, whichkey.CommandKey{Key: "A", Desc: "amend"})
		}

		var groups []whichkey.CommandGroup
		groups = append(groups, whichkey.CommandGroup{Items: gitItems})
		if len(changesItems) > 0 {
			groups = append(groups, whichkey.CommandGroup{Items: changesItems})
		}
		return groups

	case gitScreenBranch:
		return []whichkey.CommandGroup{
			{Items: []whichkey.CommandKey{
				{Key: "enter", Desc: "checkout"},
				{Key: "n", Desc: "new branch"},
				{Key: "d", Desc: "delete branch"},
				{Key: "m", Desc: "merge"},
			}},
		}

	case gitScreenLog:
		return []whichkey.CommandGroup{
			{Items: []whichkey.CommandKey{
				{Key: "enter", Desc: "view detail"},
			}},
		}
	}

	return nil
}

// IsTextInputActive returns true when a text input has focus on this screen.
func (m *GitOpsModel) IsTextInputActive() bool {
	return m.commitInput.Focused() || m.branchInput.Focused()
}

func (m *GitOpsModel) SetSize(w, h int) {
	m.width = w
	m.height = max(0, h-5)
	// Left panel: ~40%, Right panel: ~60%
	leftW := w * 40 / 100
	rightW := w - leftW
	m.diffVP.Width = max(0, rightW-4)
	m.diffVP.Height = max(8, m.height-13)
	m.logFileDiffVP.Width = max(0, rightW-4)
	m.logFileDiffVP.Height = max(8, m.height-13)
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
		func() tea.Msg {
			// Fetch last commit message for amend feature
			commits, err := git.Log(m.dir, 1)
			if err == nil && len(commits) > 0 {
				return gitOpsLastCommitMsg{msg: commits[0].Message}
			}
			return gitOpsLastCommitMsg{msg: ""}
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
		m.logFileDiffVP.Width = max(0, rightW-4)
		m.logFileDiffVP.Height = max(8, m.height-13)
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

	case armDisarmMsg:
		if msg.generation == m.armGeneration {
			m.branchMergeConfirm = false
		}

	case gitOpsRepoCheckMsg:
		m.isRepo = msg.isRepo
		m.gitRoot = msg.gitRoot
		m.gitFound = msg.gitFound

	case gitOpsStatusMsg:
		if msg.err != nil {
			if m.errorRegistry != nil {
				m.errorRegistry.AddWithDetail("git_ops", msg.err.Error(), "")
			}
			m.doneOp = msg.err.Error()
			m.doneOpAt = time.Now()
		} else {
			m.files = msg.files
			m.buildFilePanels()
			m.hasUnpushed = git.HasUnpushedCommits(m.dir)
			stashes, _ := git.StashList(m.dir)
			m.hasStash = len(stashes) > 0
			m.doneOp = ""
			m.doneOpIsErr = false
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
		delete(m.pendingOps, "discard")
		delete(m.pendingOps, "amend")
		delete(m.pendingOps, "delete-branch")
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
			// Capture error in registry
			if m.errorRegistry != nil {
				m.errorRegistry.AddWithDetail("git_ops", msg.err.Error(), "")
			}
			m.doneOp = msg.err.Error()
			m.doneOpIsErr = true
			m.doneOpAt = time.Now()
		} else {
			m.doneOp = msg.result
			m.doneOpIsErr = false
			m.doneOpAt = time.Now()
		}
		m.pendingOps = make(map[string]string)
		m.pendingStart = time.Time{} // reset so next op shows spinner after 200ms
		// Schedule re-render so doneOp auto-disappears
		return m, doneExpireCmd()

	case gitOpsBranchMsg:
		if msg.err != nil {
			// Capture error in registry
			if m.errorRegistry != nil {
				m.errorRegistry.AddWithDetail("git_ops", msg.err.Error(), "")
			}
			m.doneOp = msg.err.Error()
			m.doneOpIsErr = true
			m.doneOpAt = time.Now()
		} else {
			m.branches = msg.branches
			m.clampBranchCursor()
			// Refresh hasUnpushed after branch change (checkout changes upstream)
			m.hasUnpushed = git.HasUnpushedCommits(m.dir)
		}
		// Clear all branch-related pending ops (checkout, delete, create, merge)
		delete(m.pendingOps, "branches")
		delete(m.pendingOps, "checkout")
		delete(m.pendingOps, "delete-branch")
		delete(m.pendingOps, "create-branch")
		delete(m.pendingOps, "merge-branch")

	case gitOpsLogMsg:
		if msg.err != nil {
			// Capture error in registry
			if m.errorRegistry != nil {
				m.errorRegistry.AddWithDetail("git_ops", msg.err.Error(), "")
			}
			m.doneOp = msg.err.Error()
			m.doneOpIsErr = true
			m.doneOpAt = time.Now()
			m.screen = gitScreenMain
		} else {
			m.commits = msg.commits
			m.logCursor = 0
		}
		delete(m.pendingOps, "log")

	case gitOpsLastCommitMsg:
		m.lastCommitMsg = msg.msg

	case gitOpsDoneExpiredMsg:
		// Re-render after doneOp timeout — message disappears automatically

	case gitOpsCommitFilesMsg:
		if msg.err != nil {
			if m.errorRegistry != nil {
				m.errorRegistry.AddWithDetail("git_ops", msg.err.Error(), "")
			}
			m.doneOp = msg.err.Error()
			m.doneOpIsErr = true
			m.doneOpAt = time.Now()
			delete(m.pendingOps, "commit-files")
			return m, doneExpireCmd()
		}
		m.logFiles = msg.files
		m.logFileCursor = 0
		delete(m.pendingOps, "commit-files")
		// Auto-fetch diff for first file
		if len(m.logFiles) > 0 {
			m.startPendingOpImmediate("commit-diff", "loading")
			hash := m.logDetailHash
			path := m.logFiles[0]
			return m, tea.Batch(
				func() tea.Msg {
					output, err := git.DiffCommitFile(m.dir, hash, path)
					return gitOpsCommitDiffMsg{output: output, err: err}
				},
				spinnerCmd(),
			)
		}

	case gitOpsCommitDiffMsg:
		if msg.err != nil {
			m.logFileDiff = ""
			m.logFileDiffVP.SetContent("")
		} else {
			m.logFileDiff = colorDiff(msg.output)
			m.logFileDiffVP.SetContent(m.logFileDiff)
			m.logFileDiffVP.GotoTop()
		}
		delete(m.pendingOps, "commit-diff")

	case gitOpsCreateBranchMsg:
		if msg.err != nil {
			if m.errorRegistry != nil {
				m.errorRegistry.AddWithDetail("git_ops", msg.err.Error(), "")
			}
			m.doneOp = msg.err.Error()
			m.doneOpIsErr = true
			m.doneOpAt = time.Now()
			delete(m.pendingOps, "create-branch")
			return m, doneExpireCmd()
		}
		branches, _ := git.Branches(m.dir)
		m.branches = branches
		m.branchCursor = 0
		delete(m.pendingOps, "create-branch")
		return m, nil

	case gitOpsMergeMsg:
		if msg.err != nil {
			if m.errorRegistry != nil {
				m.errorRegistry.AddWithDetail("git_ops", msg.err.Error(), "")
			}
			m.doneOp = msg.err.Error()
			m.doneOpIsErr = true
			m.doneOpAt = time.Now()
			delete(m.pendingOps, "merge-branch")
			return m, doneExpireCmd()
		}
		delete(m.pendingOps, "merge-branch")
		branches, _ := git.Branches(m.dir)
		m.branches = branches
		// Full status refresh to update hasUnpushed, file panels, stash, etc.
		return m, func() tea.Msg {
			files, _ := git.StatusPorcelain(m.dir)
			return gitOpsStatusMsg{files: files}
		}
	}

	// ── E key: open error list ──
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.String() == "E" && m.errorRegistry != nil && m.errorRegistry.Len() > 0 {
			return m, func() tea.Msg {
				return NavigateTo{Screen: NavErrors, ProfileName: m.profile.Name}
			}
		}
	}

	// Route to active sub-screen
	switch m.screen {
	case gitScreenMain:
		return m.updateMain(msg)
	case gitScreenCommit:
		return m.updateCommit(msg)
	case gitScreenBranch:
		return m.updateBranch(msg)
	case gitScreenLog:
		return m.updateLog(msg)
	case gitScreenLogDetail:
		return m.updateLogDetail(msg)
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
	case gitScreenLog:
		return m.viewLog()
	case gitScreenLogDetail:
		return m.viewLogDetail()
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
	m.startPendingOpImmediate("diff", "loading diff")
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
		// Only show spinner on the screen that started the operation
		if m.spinnerScreen != m.screen {
			return ""
		}
		// Only show spinner after 200ms — fast ops (stage/unstage) won't flash
		if time.Since(m.pendingStart) > 200*time.Millisecond {
			return "\n" + opsRenderSpinnerDot(m.spinnerFrame, op+"...")
		}
		return "" // too fast, skip spinner — doneOp will show result
	}
	if m.doneOp != "" && time.Since(m.doneOpAt) < 5*time.Second {
		if m.doneOpIsErr {
			return "\n" + formatErrorMsg(m.doneOp)
		}
		return "\n" + styles.StyleSuccess.Render("  " + m.doneOp)
	}
	return ""
}

// ── Pending Op Helpers ─────────────────────────────────────────────────────────

// startPendingOp sets a pending op with 200ms spinner delay.
func (m *GitOpsModel) startPendingOp(key, label string) {
	m.pendingStart = time.Now()
	m.spinnerScreen = m.screen
	m.pendingOps[key] = label
}

// startPendingOpImmediate sets a pending op that shows spinner immediately.
func (m *GitOpsModel) startPendingOpImmediate(key, label string) {
	m.spinnerScreen = m.screen
	m.pendingOps[key] = label
}

// ── Done Op Expiry ────────────────────────────────────────────────────────────

// doneExpireCmd returns a tea.Cmd that fires after the doneOp timeout
// to trigger a re-render and make the message disappear.
func doneExpireCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return gitOpsDoneExpiredMsg{}
	})
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
			if f.Staged {
				m.startPendingOp("unstage", "unstaging")
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
			m.startPendingOp("stage", "staging")
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
			allStaged := m.stagedStart > 0 && m.stagedStart >= len(m.files)
			if allStaged {
				m.startPendingOp("unstage", "unstaging all")
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
			m.startPendingOp("stage", "staging all")
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
				m.startPendingOpImmediate("branches", "loading")
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
				m.startPendingOp("push", "pushing")
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
				m.startPendingOp("pull", "pulling")
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
				m.startPendingOp("stash", "stashing")
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
				m.startPendingOp("stash", "popping stash")
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

		// d: Discard changes (direct — which-key handles multi-key via prefix children)
		case msg.String() == "d":
			if m.screen != gitScreenMain {
				break
			}
			f := m.currentFile()
			if f == nil || f.Staged || f.StatusCode == '?' {
				break // silently ignore — not a discardable file
			}
			path := f.Path
			m.startPendingOp("discard", "discarding")
			return m, tea.Batch(
				func() tea.Msg {
					err := git.DiscardChanges(m.dir, path)
					if err != nil {
						return gitOpsDoneMsg{err: err}
					}
					files, _ := git.StatusPorcelain(m.dir)
					return gitOpsStatusMsg{files: files}
				},
				spinnerCmd(),
			)

		// A: Amend last commit — opens input pre-filled with last message
		case msg.String() == "A":
			if m.screen != gitScreenMain {
				break
			}
			m.screen = gitScreenCommit
			m.amendMode = true
			m.commitInput.SetValue(m.lastCommitMsg)
			m.commitInput.Placeholder = "amend message"
			m.commitInput.Focus()
			return m, textinput.Blink

		// l: Log viewer (toggle)
		case msg.String() == "l":
			if m.screen != gitScreenMain {
				break
			}
			// Enter log screen
			m.screen = gitScreenLog
			m.logCursor = 0
			if len(m.commits) == 0 {
				m.startPendingOpImmediate("log", "loading")
				return m, tea.Batch(
					func() tea.Msg {
						commits, err := git.Log(m.dir, 30)
						return gitOpsLogMsg{commits: commits, err: err}
					},
					spinnerCmd(),
				)
			}
			return m, nil

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

	// Header with current branch
	currentBranch := ""
	for _, br := range m.branches {
		if br.Current {
			currentBranch = br.Name
			break
		}
	}
	headerText := "  Git Ops — " + m.profile.Name
	if currentBranch != "" {
		headerText += "  " + styles.StyleBadgeEnv.Render(" "+currentBranch+" ")
	}
	title := styles.StyleHeader.Render(headerText)

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
		return b.String()
	}

	// Clean tree: simple centered message, no panels
	if len(m.files) == 0 {
		var b strings.Builder
		b.WriteString("\n")
		b.WriteString(title)
		b.WriteString("\n\n")
		b.WriteString("  " + styles.StyleSubtitle.Render("Working tree clean"))
		b.WriteString(m.renderPendingOrDone())
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

	return "\n" + title + "\n" + body + m.renderPendingOrDone()
}

// ── Clean Tree Footer ─────────────────────────────────────────────────────────

// ── File List Panel ───────────────────────────────────────────────────────────

func (m *GitOpsModel) renderFileList(panelW int) string {
	var b strings.Builder

	// Panel header with focus indicator
	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	if m.focusPanel == 0 {
		b.WriteString("  " + focusStyle.Render("Files") + " ●")
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

		if i == m.fileCursor && m.focusPanel == 0 {
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
		focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		if m.focusPanel == 1 {
			b.WriteString("  " + focusStyle.Render(header) + " ●")
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
			m.amendMode = false
			m.commitInput.Blur()
			return m, nil
		case "enter":
			subject := strings.TrimSpace(m.commitInput.Value())
			if subject == "" {
				return m, nil
			}
			// Execute commit or amend
			m.screen = gitScreenMain
			m.commitInput.Blur()
			isAmend := m.amendMode
			m.amendMode = false
			m.startPendingOpImmediate("commit", "committing")
			msg := subject
			return m, tea.Batch(
				func() tea.Msg {
					var err error
					if isAmend {
						err = git.AmendCommit(m.dir, msg, "")
					} else {
						err = git.Commit(m.dir, msg, "")
					}
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
	if m.amendMode {
		b.WriteString(styles.StyleHeader.Render("  Amend Commit"))
	} else {
		b.WriteString(styles.StyleHeader.Render("  Commit"))
	}
	b.WriteString("\n\n")
	b.WriteString("  " + styles.StyleInputLabel.Render("Message:") + "\n")
	b.WriteString("  " + m.commitInput.View())
	b.WriteString("\n\n")
	if m.amendMode {
		b.WriteString(styles.InlineHelp(
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("↵", "amend"),
		))
	} else {
		b.WriteString(styles.InlineHelp(
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("↵", "commit"),
		))
	}
	return b.String()
}

// ── Branch helpers ────────────────────────────────────────────────────────────

// focusedBranchName returns the name of the branch at the current cursor, or "".
func (m *GitOpsModel) focusedBranchName() string {
	if m.branchCursor < 0 || m.branchCursor >= len(m.branches) {
		return ""
	}
	return m.branches[m.branchCursor].Name
}

// clampBranchCursor ensures branchCursor is within valid range.
func (m *GitOpsModel) clampBranchCursor() {
	total := len(m.branches)
	if m.branchCursor >= total {
		m.branchCursor = max(0, total-1)
	}
	if m.branchCursor < 0 {
		m.branchCursor = 0
	}
}

// ── Branch Screen Update ────────────────────────────────────────────────────

func (m *GitOpsModel) updateBranch(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:

		// ── Branch creating mode ───────────────────────────────────────
		if m.branchCreating {
			switch msg.String() {
			case "esc":
				m.branchCreating = false
				m.branchInput.Blur()
				return m, nil
			case "enter":
				name := strings.TrimSpace(m.branchInput.Value())
				if name == "" {
					return m, nil
				}
				m.branchCreating = false
				m.branchInput.Blur()
				m.startPendingOpImmediate("create-branch", "creating branch")
				return m, tea.Batch(
					func() tea.Msg {
						err := git.CreateBranch(m.dir, name)
						return gitOpsCreateBranchMsg{branchName: name, err: err}
					},
					spinnerCmd(),
				)
			default:
				var cmd tea.Cmd
				m.branchInput, cmd = m.branchInput.Update(msg)
				return m, cmd
			}
		}

		// ── Merge confirm mode ─────────────────────────────────────────
		if m.branchMergeConfirm {
			switch msg.String() {
			case "m":
				m.branchMergeConfirm = false
				name := m.focusedBranchName()
				if name == "" {
					return m, nil
				}
				m.startPendingOpImmediate("merge-branch", "merging")
				src := name
				return m, tea.Batch(
					func() tea.Msg {
						err := git.MergeBranch(m.dir, src)
						return gitOpsMergeMsg{source: src, err: err}
					},
					spinnerCmd(),
				)
			default:
				m.branchMergeConfirm = false
				return m, nil
			}
		}

		// ── Normal branch view ─────────────────────────────────────────
		totalBranches := len(m.branches)

		switch msg.String() {
		case "esc", "q":
			m.screen = gitScreenMain
			m.doneOp = ""
			m.doneOpIsErr = false
			m.doneOpAt = time.Time{}
			return m, nil

		case "up":
			if m.branchCursor > 0 {
				m.branchCursor--
			}
			return m, nil

		case "down":
			if m.branchCursor < totalBranches-1 {
				m.branchCursor++
			}
			return m, nil

		case "enter":
			// Checkout the focused branch
			name := m.focusedBranchName()
			if name == "" {
				return m, nil
			}
			if m.branchCursor < len(m.branches) && m.branches[m.branchCursor].Current {
				return m, nil
			}
			m.startPendingOpImmediate("checkout", "checking out")
			checkoutName := name
			return m, tea.Batch(
				func() tea.Msg {
					err := git.CheckoutBranch(m.dir, checkoutName)
					if err != nil {
						return gitOpsDoneMsg{err: err}
					}
					branches, _ := git.Branches(m.dir)
					return gitOpsBranchMsg{branches: branches}
				},
				spinnerCmd(),
			)

		case "n":
			m.branchCreating = true
			m.branchInput.SetValue("")
			m.branchInput.Focus()
			return m, textinput.Blink

		case "m":
			// Merge focused branch into current — silently ignore if not available
			name := m.focusedBranchName()
			if name == "" {
				return m, nil
			}
			if m.branchCursor >= 0 && m.branchCursor < len(m.branches) {
				br := m.branches[m.branchCursor]
				// Skip if same branch or same HEAD
				if br.Current {
					return m, nil
				}
				for _, other := range m.branches {
					if other.Current {
						focusHead, err1 := git.BranchHead(m.dir, br.Name)
						currentHead, err2 := git.BranchHead(m.dir, other.Name)
						if err1 == nil && err2 == nil && focusHead == currentHead {
							return m, nil
						}
						break
					}
				}
			}
			m.branchMergeConfirm = true
			m.armGeneration++
			return m, armDisarmAfter(m.armGeneration)

		case "d":
			// Delete branch (direct — which-key handles multi-key via prefix children)
			name := m.focusedBranchName()
			if name == "" || name == "main" {
				return m, nil
			}
			if m.branchCursor >= 0 && m.branchCursor < len(m.branches) && m.branches[m.branchCursor].Current {
				return m, nil
			}
			m.pendingOps["delete-branch"] = "deleting"
			deleteName := name
			return m, tea.Batch(
				func() tea.Msg {
					err := git.DeleteBranch(m.dir, deleteName)
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

// ── Branch Screen View ──────────────────────────────────────────────────────

func (m *GitOpsModel) viewBranch() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.StyleHeader.Render(fmt.Sprintf("  Branches — %d", len(m.branches))))
	b.WriteString("\n\n")

	if len(m.branches) == 0 {
		b.WriteString(styles.StyleSubtitle.Render("  No branches found"))
		b.WriteString(m.renderPendingOrDone())
		b.WriteString("\n")
		return b.String()
	}

	selStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(styles.ColorMuted)
	currentMarker := styles.StyleError.Render("*") // red asterisk for current branch

	overhead := 6
	win := opsWindow(len(m.branches), m.branchCursor, 1, m.height, overhead)

	for i := win.start; i < win.end; i++ {
		br := m.branches[i]
		isCursor := i == m.branchCursor

		// Branch name with current indicator
		name := br.Name
		if br.Current {
			name += " " + currentMarker
		}

		// Tracking info
		track := ""
		if br.Tracking != "" {
			track = mutedStyle.Render("→ " + br.Tracking)
		}

		// Ahead/behind
		ahead, behind := git.BranchAheadBehind(m.dir, br.Name, br.Tracking)
		sync := ""
		if ahead > 0 || behind > 0 {
			parts := []string{}
			if ahead > 0 {
				parts = append(parts, mutedStyle.Render(fmt.Sprintf("↑%d", ahead)))
			}
			if behind > 0 {
				parts = append(parts, mutedStyle.Render(fmt.Sprintf("↓%d", behind)))
			}
			sync = " " + strings.Join(parts, " ")
		}

		// Last commit message (truncated)
		lastMsg := git.BranchLastCommit(m.dir, br.Name)
		if len(lastMsg) > 40 {
			lastMsg = lastMsg[:37] + "..."
		}
		msgPart := ""
		if lastMsg != "" {
			msgPart = mutedStyle.Render(" " + lastMsg)
		}

		// Render line (request ops folder style — no badge)
		if isCursor {
			b.WriteString(selStyle.Render(fmt.Sprintf("  > ▸ %s", name)) + "  " + track + sync + msgPart + "\n")
		} else {
			b.WriteString(fmt.Sprintf("    ▸ %s", name) + "  " + track + sync + msgPart + "\n")
		}
	}

	// Scroll indicators
	if win.hasAbove {
		b.WriteString(styles.StyleSubtitle.Render("  ↑"))
		b.WriteString("\n")
	}
	if win.hasBelow {
		b.WriteString(styles.StyleSubtitle.Render("  ↓"))
		b.WriteString("\n")
	}

	b.WriteString(m.renderPendingOrDone())

	// Branch creating prompt
	if m.branchCreating {
		b.WriteString("\n")
		b.WriteString("  " + styles.StyleInputLabel.Render("New branch name:") + "\n")
		b.WriteString("  " + m.branchInput.View())
	}

	// Merge confirm prompt
	if m.branchMergeConfirm {
		focusBranch := m.focusedBranchName()
		b.WriteString("\n")
		b.WriteString(styles.StyleError.Render("  Press m again to merge "+focusBranch+" into current"))
	}

	b.WriteString("\n\n")

	// Dynamic footer: only show actions that make sense for the focused branch
	focusBr := m.focusedBranchName()
	canDelete := focusBr != "" && focusBr != "main"
	canMerge := focusBr != ""
	if m.branchCursor >= 0 && m.branchCursor < len(m.branches) {
		br := m.branches[m.branchCursor]
		canDelete = canDelete && !br.Current
		// Can't merge into yourself
		canMerge = canMerge && !br.Current
		// Can't merge if same HEAD (already up to date)
		if canMerge {
			for _, other := range m.branches {
				if other.Current {
					focusHead, err1 := git.BranchHead(m.dir, br.Name)
					currentHead, err2 := git.BranchHead(m.dir, other.Name)
					if err1 == nil && err2 == nil && focusHead == currentHead {
						canMerge = false
					}
					break
				}
			}
		}
	}

	return b.String()
}

// ── Log Screen (full-screen commit list) ────────────────────────────────────

func (m *GitOpsModel) updateLog(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q", "l":
			m.screen = gitScreenMain
			return m, nil

		case "up":
			if m.logCursor > 0 {
				m.logCursor--
			}
			return m, nil

		case "down":
			if m.logCursor < len(m.commits)-1 {
				m.logCursor++
			}
			return m, nil

		case "enter":
			// Drill down into commit
			if m.logCursor >= len(m.commits) || len(m.commits) == 0 {
				break
			}
			hash := m.commits[m.logCursor].Hash
			m.screen = gitScreenLogDetail
			m.logDetailHash = hash
			m.logFiles = nil
			m.logFileCursor = 0
			m.logFileDiff = ""
			m.logFileDiffVP.SetContent("")
			m.startPendingOpImmediate("commit-files", "loading")
			return m, tea.Batch(
				func() tea.Msg {
					files, err := git.CommitFiles(m.dir, hash)
					return gitOpsCommitFilesMsg{hash: hash, files: files, err: err}
				},
				spinnerCmd(),
			)
		}
	}
	return m, nil
}

func (m *GitOpsModel) viewLog() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.StyleHeader.Render(fmt.Sprintf("  Commit Log — %d commits", len(m.commits))))
	b.WriteString("\n\n")

	if len(m.commits) == 0 {
		b.WriteString(styles.StyleSubtitle.Render("  No commits"))
		b.WriteString("\n\n")
		return b.String()
	}

	// Windowed rendering
	overhead := 5 // header + blank + footer
	win := opsWindow(len(m.commits), m.logCursor, 1, m.height, overhead)

	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(styles.ColorMuted)

	for i := win.start; i < win.end; i++ {
		c := m.commits[i]
		hash := styles.StyleCode.Render(c.Hash)
		author := mutedStyle.Render(c.Author)
		date := mutedStyle.Render(c.Date)
		message := c.Message

		if i == m.logCursor {
			cursor := focusStyle.Render("▸")
			b.WriteString(fmt.Sprintf(" %s %s %s %s %s\n", cursor, hash, author, date, message))
		} else {
			b.WriteString(fmt.Sprintf("   %s %s %s %s\n", hash, author, date, message))
		}
	}

	b.WriteString(m.renderPendingOrDone())
	return b.String()
}

// ── Log Detail Screen (commit files + diff) ──────────────────────────────────

func (m *GitOpsModel) updateLogDetail(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Handle jump keys (return bool, can't be in string switch)
		if opsIsJumpTop(msg) {
			if m.focusPanel == 0 {
				m.logFileCursor = 0
				return m, m.cmdFetchCommitDiff()
			}
			m.logFileDiffVP.GotoTop()
			return m, nil
		}
		if opsIsJumpBottom(msg) {
			if m.focusPanel == 0 {
				if len(m.logFiles) > 0 {
					m.logFileCursor = len(m.logFiles) - 1
				}
				return m, m.cmdFetchCommitDiff()
			}
			m.logFileDiffVP.GotoBottom()
			return m, nil
		}
		switch msg.String() {
		case "esc", "q":
			m.screen = gitScreenLog
			return m, nil

		case "left":
			m.focusPanel = 0
			return m, nil

		case "right":
			m.focusPanel = 1
			return m, nil

		case "up":
			if m.focusPanel == 0 {
				if m.logFileCursor > 0 {
					m.logFileCursor--
					return m, m.cmdFetchCommitDiff()
				}
			} else {
				m.logFileDiffVP.LineUp(3)
			}
			return m, nil

		case "down":
			if m.focusPanel == 0 {
				if m.logFileCursor < len(m.logFiles)-1 {
					m.logFileCursor++
					return m, m.cmdFetchCommitDiff()
				}
			} else {
				m.logFileDiffVP.LineDown(3)
			}
			return m, nil
		}
	}
	return m, nil
}

// cmdFetchCommitDiff fetches the diff for the currently selected file in commit detail.
func (m *GitOpsModel) cmdFetchCommitDiff() tea.Cmd {
	if m.logFileCursor >= len(m.logFiles) {
		return nil
	}
	path := m.logFiles[m.logFileCursor]
	hash := m.logDetailHash
	m.startPendingOpImmediate("commit-diff", "loading")
	return tea.Batch(
		func() tea.Msg {
			output, err := git.DiffCommitFile(m.dir, hash, path)
			return gitOpsCommitDiffMsg{output: output, err: err}
		},
		spinnerCmd(),
	)
}

func (m *GitOpsModel) viewLogDetail() string {
	w := m.width
	leftW := w * 40 / 100
	rightW := w - leftW

	title := styles.StyleHeader.Render(fmt.Sprintf("  Commit %s", styles.StyleCode.Render(m.logDetailHash)))

	// Left panel: files changed
	leftPanel := m.renderLogFiles(leftW - 2)

	// Right panel: diff
	rightPanel := m.renderLogDiff(rightW - 2)

	// Separator
	leftH := lipgloss.Height(leftPanel)
	rightH := lipgloss.Height(rightPanel)
	sepH := leftH
	if rightH > sepH {
		sepH = rightH
	}
	sep := lipgloss.NewStyle().
		Foreground(styles.ColorMuted).
		Render(strings.Repeat("│\n", sepH))

	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, sep, rightPanel)

	return "\n" + title + "\n" + body + m.renderPendingOrDone()
}

func (m *GitOpsModel) renderLogFiles(panelW int) string {
	var b strings.Builder
	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)

	if m.focusPanel == 0 {
		b.WriteString("  " + focusStyle.Render("Files") + " ●")
	} else {
		b.WriteString(styles.StyleSubtitle.Render("  Files"))
	}
	b.WriteString("\n")

	if len(m.logFiles) == 0 {
		b.WriteString(styles.StyleSubtitle.Render("  No files"))
		return b.String()
	}

	overhead := 3
	win := opsWindow(len(m.logFiles), m.logFileCursor, 1, m.height, overhead)
	for i := win.start; i < win.end; i++ {
		path := truncatePath(m.logFiles[i], panelW-6)
		if i == m.logFileCursor && m.focusPanel == 0 {
			b.WriteString(" " + focusStyle.Render("▸") + " " + styles.StyleCode.Render(path))
		} else {
			b.WriteString("   " + styles.StyleCode.Render(path))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m *GitOpsModel) renderLogDiff(panelW int) string {
	var b strings.Builder
	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)

	if m.logFileCursor < len(m.logFiles) {
		header := m.logFiles[m.logFileCursor]
		if m.focusPanel == 1 {
			b.WriteString("  " + focusStyle.Render(header) + " ●")
		} else {
			b.WriteString(styles.StyleSubtitle.Render("  " + header))
		}
		b.WriteString("\n")
	} else {
		b.WriteString(styles.StyleSubtitle.Render("  Select a file"))
		b.WriteString("\n")
		return b.String()
	}

	b.WriteString(m.logFileDiffVP.View())
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
