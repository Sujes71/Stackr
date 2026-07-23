package views

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/tui/styles"
	"github.com/stackr/stackr/internal/tui/whichkey"
)

// Content-Type options for the circular selector in the KV builder.
var contentTypeOptions = []string{
	"application/json",
	"text/xml",
	"application/xml",
	"application/soap+xml",
	"application/x-www-form-urlencoded",
	"multipart/form-data",
	"text/plain",
	"application/octet-stream",
	"application/graphql",
}

// ── Internal screen enum ──────────────────────────────────────────────────────

type requestOpsScreen int

const (
	reqScreenList requestOpsScreen = iota
	reqScreenCreateForm
	reqScreenEditForm
	reqScreenVarPrompt
	reqScreenResult
	reqScreenAddEnv
	reqScreenConfirmDelete
)

// ── Request list item types ────────────────────────────────────────────────────

type requestListItemKind int

const (
	reqItemURL requestListItemKind = iota
	reqItemRequest
	reqItemAddURL
)

type requestListItem struct {
	kind     requestListItemKind
	label    string
	sublabel string
	urlIdx   int
	reqIdx   int
	enabled  bool
	// Tree hierarchy
	isChild   bool // true if this is a child request under a folder
	parentIdx int  // urlIdx of parent folder (for requests)
	depth     int  // 0 = top level, 1 = child
}

// ── Supported HTTP methods ────────────────────────────────────────────────────

var httpMethods = []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}

// ── Model ─────────────────────────────────────────────────────────────────────

type RequestOpsModel struct {
	cfg     *config.Config
	profile *config.Profile
	envName string

	errorRegistry *ErrorRegistry

	screen requestOpsScreen

	// Environment switching
	activeEnv int

	// List screen state
	listCursor       int
	deleteArmed      bool
	deleteArmedIsEnv bool // true when D (env delete) is armed, false for d (item delete)
	armGeneration    int  // incremented on each arm; timer only fires if generation matches
	listItems        []requestListItem

	// Tree state
	expandedURLs map[int]bool // tracks which URL folders are expanded (key = urlIdx)
	parentURLIdx int          // when creating a request from a folder context, pre-select this URL (-1 = none)

	// Create/Edit form state
	formFocus      int
	editingIdx     int
	isURLForm      bool // true = Base URL form, false = Request form
	methodName     string
	methodCursor   int
	baseURLIdx     int
	nameInput      textinput.Model
	pathInput      textinput.Model
	isLogin        bool
	tokenPathInput textinput.Model
	headerKV       []config.RequestKV
	headerCursor   int
	paramKV        []config.RequestKV
	paramCursor    int
	bodyInput      textarea.Model
	bodyEditMode   bool
	formError      string

	// KV builder state (headers or params)
	kvBuilding       bool
	kvPairs          [][2]string
	kvPhase          int
	kvTarget         string // "headers" or "params"
	kvOrigVal        string
	kvDeleteArmed    bool
	kvOverwriteIdx   int  // -1 = new pair, >=0 = overwrite existing pair at this index
	kvContentTypeIdx int  // selector position for Content-Type value
	kvContentTypeKey bool // true when current key is "Content-Type"

	// Variable prompt state
	varPromptIdx    int
	varPromptNames  []string          // ordered list of variable names (stable order)
	varPromptValues map[string]string // name -> value
	varPromptInput  textinput.Model

	// Running state
	pendingOps map[string]string
	doneOp     string
	doneOpAt   time.Time

	// Spinner
	spinnerFrame int

	// Viewport for results
	resultViewport  viewport.Model
	viewingResult   bool
	viewportContent string
	resultTitle     string
	resultRaw       string // unstyled result for clipboard copy
	copied          bool   // show "copied" feedback
	copyFeedback    string // brief feedback message in list view

	// Add environment screen
	newEnvInput textinput.Model

	// Delete environment confirmation
	deleting     string // env name pending confirmation
	confirmInput textinput.Model

	// Dimensions
	width  int
	height int
}

// ── Constructor ───────────────────────────────────────────────────────────────

func NewRequestOps(cfg *config.Config, p *config.Profile, envName string, errorRegistry *ErrorRegistry) *RequestOpsModel {
	m := &RequestOpsModel{
		cfg:            cfg,
		profile:        p,
		envName:        envName,
		errorRegistry:  errorRegistry,
		pendingOps:     make(map[string]string),
		expandedURLs:   make(map[int]bool),
		parentURLIdx:   -1,
		nameInput:      textinput.New(),
		pathInput:      textinput.New(),
		tokenPathInput: textinput.New(),
		varPromptInput: textinput.New(),
		bodyInput:      textarea.New(),
		resultViewport: viewport.New(0, 0),
		newEnvInput:    textinput.New(),
		confirmInput:   textinput.New(),
	}
	vp := &m.resultViewport
	vp.KeyMap.Up.SetKeys("up")
	vp.KeyMap.Down.SetKeys("down")
	vp.KeyMap.PageUp.SetKeys()
	vp.KeyMap.PageDown.SetKeys()
	vp.KeyMap.HalfPageUp.SetKeys()
	vp.KeyMap.HalfPageDown.SetKeys()
	m.nameInput.Placeholder = "Request name"
	m.pathInput.Placeholder = "/api/v1/users (appended to base)"
	m.tokenPathInput.Placeholder = "data.token"
	m.bodyInput.SetHeight(6)
	m.bodyInput.ShowLineNumbers = false
	m.bodyInput.Placeholder = "{\n  \"key\": \"value\"\n}"
	m.bodyInput.CharLimit = 0
	m.newEnvInput.Placeholder = "environment name"
	m.newEnvInput.CharLimit = 20

	m.ensureDefaultEnv()
	m.migrateToGlobal()

	// Find active env index
	m.activeEnv = 0
	found := false
	for i, env := range m.profile.RequestEnvs {
		if env.Name == envName {
			m.activeEnv = i
			found = true
			break
		}
	}
	if !found && len(m.profile.RequestEnvs) > 0 {
		m.envName = m.profile.RequestEnvs[0].Name
	}
	return m
}

// ── Getters ───────────────────────────────────────────────────────────────────

func (m *RequestOpsModel) ProfileName() string { return m.profile.Name }
func (m *RequestOpsModel) EnvName() string     { return m.envName }

func (m *RequestOpsModel) ServiceKey() string {
	return m.profile.Name + "/" + m.envName
}

func (m *RequestOpsModel) ReShow(cfg *config.Config) {
	if p, ok := cfg.GetProfile(m.profile.Name); ok {
		m.profile = p
	}
	m.deleteArmed = false
}

func (m *RequestOpsModel) IsBusy() bool {
	return len(m.pendingOps) > 0
}

// WhichKeyCommands returns the command catalog for Request Ops screen.
func (m *RequestOpsModel) WhichKeyCommands() []whichkey.CommandGroup {
	switch m.screen {
	case reqScreenList:
		var items []whichkey.CommandKey

		if m.listCursor >= 0 && m.listCursor < len(m.listItems) {
			item := m.listItems[m.listCursor]
			switch item.kind {
			case reqItemRequest:
				items = append(items, whichkey.CommandKey{Key: "enter", Desc: "run"})
				items = append(items, whichkey.CommandKey{Key: "a", Desc: "add request"})
				items = append(items, whichkey.CommandKey{Key: "c", Desc: "copy cURL"})
				items = append(items, whichkey.CommandKey{Key: "e", Desc: "edit"})
				items = append(items, whichkey.CommandKey{Key: "d", Desc: "delete"})
			case reqItemURL:
				expandDesc := "expand"
				if m.expandedURLs[item.urlIdx] {
					expandDesc = "collapse"
				}
				items = append(items, whichkey.CommandKey{Key: "enter", Desc: expandDesc})
				if len(m.profile.RequestFolders) > 0 {
					anyCollapsed := false
					for i := range m.profile.RequestFolders {
						if !m.expandedURLs[i] {
							anyCollapsed = true
							break
						}
					}
					if anyCollapsed {
						items = append(items, whichkey.CommandKey{Key: " ", Desc: "expand all"})
					} else {
						items = append(items, whichkey.CommandKey{Key: " ", Desc: "collapse all"})
					}
				}
				items = append(items, whichkey.CommandKey{Key: "a", Desc: "add request"})
				items = append(items, whichkey.CommandKey{Key: "e", Desc: "edit"})
				items = append(items, whichkey.CommandKey{Key: "d", Desc: "delete"})
			case reqItemAddURL:
				items = append(items, whichkey.CommandKey{Key: "enter", Desc: "create folder"})
			}
		}

		items = append(items, whichkey.CommandKey{Key: "n", Desc: "new env"})
		return []whichkey.CommandGroup{{Items: items}}
	}
	return nil
}

// IsTextInputActive returns true when a text input has focus on this screen.
func (m *RequestOpsModel) IsTextInputActive() bool {
	return m.nameInput.Focused() || m.pathInput.Focused() || m.tokenPathInput.Focused() ||
		m.varPromptInput.Focused() || m.newEnvInput.Focused() || m.confirmInput.Focused()
}

func (m *RequestOpsModel) SetSize(w, h int) {
	m.width = w
	m.height = max(0, h-5)
	m.resultViewport.Width = w - 4
	if m.viewingResult {
		lineCount := strings.Count(m.viewportContent, "\n") + 1
		m.resultViewport.Height = min(lineCount, max(8, m.height-13))
	} else {
		m.resultViewport.Height = max(8, m.height-10)
	}
	if m.screen == reqScreenCreateForm || m.screen == reqScreenEditForm {
		m.setBodyTextareaHeight()
	}
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m *RequestOpsModel) Init() tea.Cmd {
	m.listItems = m.buildListItems()
	return nil
}

// ── Environment helper ────────────────────────────────────────────────────────

func (m *RequestOpsModel) ensureDefaultEnv() {
	if len(m.profile.RequestEnvs) == 0 {
		m.profile.RequestEnvs = []config.RequestEnv{{Name: "local"}}
		m.cfg.UpdateProfile(*m.profile)
	}
}

func (m *RequestOpsModel) currentEnv() *config.RequestEnv {
	for i := range m.profile.RequestEnvs {
		if m.profile.RequestEnvs[i].Name == m.envName {
			return &m.profile.RequestEnvs[i]
		}
	}
	return nil
}

// ── Migration: per-env → global folders + requests ────────────────────────────

func (m *RequestOpsModel) migrateToGlobal() {
	// If global folders already exist, skip migration
	if len(m.profile.RequestFolders) > 0 {
		return
	}
	// Find "local" env or first env
	var sourceEnv *config.RequestEnv
	for i := range m.profile.RequestEnvs {
		if m.profile.RequestEnvs[i].Name == "local" {
			sourceEnv = &m.profile.RequestEnvs[i]
			break
		}
	}
	if sourceEnv == nil && len(m.profile.RequestEnvs) > 0 {
		sourceEnv = &m.profile.RequestEnvs[0]
	}
	if sourceEnv == nil {
		return
	}
	// Seed global folders from source env's BaseURLs
	for _, bu := range sourceEnv.BaseURLs {
		m.profile.RequestFolders = append(m.profile.RequestFolders, config.RequestFolder{Name: bu.Name})
	}
	// Seed global requests from source env's Requests
	m.profile.RequestDefs = append(m.profile.RequestDefs, sourceEnv.Requests...)
	// Clear source env's requests (they're now global)
	sourceEnv.Requests = nil
	// Ensure all envs have BaseURL entries for all global folders
	for i := range m.profile.RequestEnvs {
		env := &m.profile.RequestEnvs[i]
		existingNames := make(map[string]bool)
		for _, bu := range env.BaseURLs {
			existingNames[bu.Name] = true
		}
		for _, f := range m.profile.RequestFolders {
			if !existingNames[f.Name] {
				env.BaseURLs = append(env.BaseURLs, config.RequestBaseURL{Name: f.Name, URL: ""})
			}
		}
	}
	m.cfg.UpdateProfile(*m.profile)
}

// ── Sync helpers ──────────────────────────────────────────────────────────────

// syncFolderToAllEnvs ensures all envs have a BaseURL entry for the given folder name.
func (m *RequestOpsModel) syncFolderToAllEnvs(folderName string) {
	for i := range m.profile.RequestEnvs {
		env := &m.profile.RequestEnvs[i]
		found := false
		for _, bu := range env.BaseURLs {
			if bu.Name == folderName {
				found = true
				break
			}
		}
		if !found {
			env.BaseURLs = append(env.BaseURLs, config.RequestBaseURL{Name: folderName, URL: ""})
		}
	}
}

// removeFolderFromAllEnvs removes BaseURL entries matching the given folder name from all envs.
func (m *RequestOpsModel) removeFolderFromAllEnvs(folderName string) {
	for i := range m.profile.RequestEnvs {
		env := &m.profile.RequestEnvs[i]
		var newURLs []config.RequestBaseURL
		for _, bu := range env.BaseURLs {
			if bu.Name != folderName {
				newURLs = append(newURLs, bu)
			}
		}
		env.BaseURLs = newURLs
	}
}

// renameFolderInAllEnvs renames a folder in all envs' BaseURL entries.
func (m *RequestOpsModel) renameFolderInAllEnvs(oldName, newName string) {
	for i := range m.profile.RequestEnvs {
		env := &m.profile.RequestEnvs[i]
		for j, bu := range env.BaseURLs {
			if bu.Name == oldName {
				env.BaseURLs[j].Name = newName
			}
		}
	}
	// Also update RequestDefs.BaseURLName references
	for i := range m.profile.RequestDefs {
		if m.profile.RequestDefs[i].BaseURLName == oldName {
			m.profile.RequestDefs[i].BaseURLName = newName
		}
	}
}

// currentEnvURL returns the URL for a given folder name in the current env.
func (m *RequestOpsModel) currentEnvURL(folderName string) string {
	env := m.currentEnv()
	if env == nil {
		return ""
	}
	for _, bu := range env.BaseURLs {
		if bu.Name == folderName {
			return bu.URL
		}
	}
	return ""
}

// setCurrentEnvURL sets the URL for a given folder name in the current env.
func (m *RequestOpsModel) setCurrentEnvURL(folderName, url string) {
	env := m.currentEnv()
	if env == nil {
		return
	}
	for i, bu := range env.BaseURLs {
		if bu.Name == folderName {
			env.BaseURLs[i].URL = url
			return
		}
	}
}

// ── List items builder ────────────────────────────────────────────────────────

func (m *RequestOpsModel) buildListItems() []requestListItem {
	env := m.currentEnv()
	if env == nil {
		return nil
	}
	var items []requestListItem

	// Group global requests by BaseURLName
	reqsByURL := make(map[string][]int) // folderName -> []reqIdx into Profile.RequestDefs
	for i, req := range m.profile.RequestDefs {
		reqsByURL[req.BaseURLName] = append(reqsByURL[req.BaseURLName], i)
	}

	// Render each global folder
	for i, folder := range m.profile.RequestFolders {
		url := m.currentEnvURL(folder.Name)
		expanded := m.expandedURLs[i]
		items = append(items, requestListItem{
			kind: reqItemURL, label: folder.Name, sublabel: url,
			urlIdx: i, enabled: true, depth: 0,
		})
		if expanded && url != "" {
			for _, reqIdx := range reqsByURL[folder.Name] {
				req := m.profile.RequestDefs[reqIdx]
				sublabel := req.Method + " " + req.Path
				// Add {token} badge for login requests
				if req.IsLogin {
					tokenAvailable := false
					for _, ev := range env.Variables {
						if ev.Name == "_token" && ev.Value != "" {
							tokenAvailable = true
							break
						}
					}
					if tokenAvailable {
						sublabel += "  🔓 {token}"
					} else {
						sublabel += "  🔑 {token}"
					}
				}
				items = append(items, requestListItem{
					kind: reqItemRequest, label: req.Name,
					sublabel: sublabel,
					reqIdx:   reqIdx, enabled: true,
					isChild: true, parentIdx: i, depth: 1,
				})
			}
		}
	}

	// + Add Folder
	items = append(items, requestListItem{
		kind: reqItemAddURL, label: "+ Add Folder", enabled: true, depth: 0,
	})

	return items
}

// ── Update dispatch ───────────────────────────────────────────────────────────

func (m *RequestOpsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if wm, ok := msg.(tea.WindowSizeMsg); ok {
		m.SetSize(wm.Width, wm.Height)
		return m, nil
	}

	// ── Arm auto-dismiss ──
	if armMsg, ok := msg.(armDisarmMsg); ok {
		if armMsg.generation == m.armGeneration {
			m.deleteArmed = false
		}
		return m, nil
	}

	if _, ok := msg.(SpinnerTickMsg); ok {
		if len(m.pendingOps) > 0 {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return SpinnerTickMsg{}
			})
		}
		if m.doneOp != "" {
			if time.Since(m.doneOpAt) >= 5*time.Second {
				m.doneOp = ""
				m.doneOpAt = time.Time{}
			} else {
				m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
				return m, tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
					return SpinnerTickMsg{}
				})
			}
		}
		return m, nil
	}

	// Handle completion messages
	switch msg := msg.(type) {
	case requestOpsDoneMsg:
		var completedLabel string
		for label := range m.pendingOps {
			completedLabel = label
		}
		m.pendingOps = make(map[string]string)

		if msg.err != nil {
			// Capture error in registry
			if m.errorRegistry != nil {
				m.errorRegistry.AddWithDetail("request_ops", msg.err.Error(), completedLabel)
			}
			m.resultTitle = completedLabel
			m.resultRaw = msg.err.Error()
			m.copied = false
			m.viewportContent = formatErrorMsg(msg.err.Error())
			m.resultViewport.SetContent(m.viewportContent)
			m.resultViewport.Width = m.width - 4
			lineCount := strings.Count(m.viewportContent, "\n") + 1
			m.resultViewport.Height = min(lineCount, max(8, m.height-13))
			m.resultViewport.YPosition = 0
			m.resultViewport.GotoTop()
			m.screen = reqScreenResult
			m.viewingResult = true
			return m, spinnerCmd()
		}

		m.doneOp = completedLabel
		m.doneOpAt = time.Now()

		if msg.result == "" {
			m.screen = reqScreenList
			m.viewingResult = false
			return m, spinnerCmd()
		}

		m.resultTitle = completedLabel
		m.resultRaw = msg.result
		m.copied = false
		m.viewportContent = styles.StyleCode.Render(msg.result)
		m.resultViewport.SetContent(m.viewportContent)
		m.resultViewport.Width = m.width - 4
		lineCount := strings.Count(m.viewportContent, "\n") + 1
		m.resultViewport.Height = min(lineCount, max(8, m.height-13))
		m.resultViewport.YPosition = 0
		m.resultViewport.GotoTop()
		m.screen = reqScreenResult
		m.viewingResult = true
		return m, spinnerCmd()
	}

	// Handle result screen key presses
	if m.screen == reqScreenResult && m.viewingResult {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			if keyMsg.String() == "c" {
				// Copy response body to clipboard (strip HTTP status line)
				if m.resultRaw != "" {
					body := m.resultRaw
					if idx := strings.Index(body, "\n\n"); idx != -1 {
						body = body[idx+2:]
					}
					_ = clipboard.WriteAll(body)
					m.copied = true
				}
				return m, nil
			}
			// Any other key dismisses
			m.screen = reqScreenList
			m.viewingResult = false
			m.resultViewport.SetContent("")
			return m, nil
		}
	}

	// ── E key: open error list ──
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.String() == "E" && m.errorRegistry != nil && m.errorRegistry.Len() > 0 {
			return m, func() tea.Msg {
				return NavigateTo{Screen: NavErrors, ProfileName: m.profile.Name, EnvName: m.envName}
			}
		}
	}

	switch m.screen {
	case reqScreenCreateForm, reqScreenEditForm:
		return m.updateForm(msg)
	case reqScreenVarPrompt:
		return m.updateVarPrompt(msg)
	case reqScreenAddEnv:
		return m.updateAddEnv(msg)
	case reqScreenConfirmDelete:
		return m.updateConfirmDelete(msg)
	default:
		return m.updateList(msg)
	}
}

// ── List screen ───────────────────────────────────────────────────────────────

func (m *RequestOpsModel) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		m.copyFeedback = "" // clear feedback on any key
		switch {
		case key.Matches(msg, opsKeyBack):
			m.deleteArmed = false
			return m, func() tea.Msg {
				return NavigateTo{Screen: NavProfileView, ProfileName: m.profile.Name, EnvName: m.envName}
			}

		// Add environment
		case msg.String() == "n":
			m.screen = reqScreenAddEnv
			m.newEnvInput.SetValue("")
			m.newEnvInput.Focus()
			m.newEnvInput.CursorEnd()
			return m, textinput.Blink

		// Contextual add (a key)
		case key.Matches(msg, opsKeyAdd):
			m.deleteArmed = false
			if m.listCursor < len(m.listItems) {
				item := m.listItems[m.listCursor]
				switch item.kind {
				case reqItemURL:
					return m.startCreateRequestForURL(item.urlIdx)
				case reqItemRequest:
					return m.startCreateRequestForURL(item.parentIdx)
				case reqItemAddURL:
					return m.startCreateURL()
				default:
					return m.startCreateURL()
				}
			}
			return m.startCreateURL()

		// Environment switching
		case msg.String() == "left":
			if len(m.profile.RequestEnvs) > 0 {
				m.activeEnv--
				if m.activeEnv < 0 {
					m.activeEnv = len(m.profile.RequestEnvs) - 1
				}
				m.envName = m.profile.RequestEnvs[m.activeEnv].Name
				m.listItems = m.buildListItems()
				if m.listCursor >= len(m.listItems) {
					m.listCursor = len(m.listItems) - 1
				}
				if m.listCursor < 0 {
					m.listCursor = 0
				}
			}
			return m, nil

		case msg.String() == "right":
			if len(m.profile.RequestEnvs) > 0 {
				m.activeEnv++
				if m.activeEnv >= len(m.profile.RequestEnvs) {
					m.activeEnv = 0
				}
				m.envName = m.profile.RequestEnvs[m.activeEnv].Name
				m.listItems = m.buildListItems()
				if m.listCursor >= len(m.listItems) {
					m.listCursor = len(m.listItems) - 1
				}
				if m.listCursor < 0 {
					m.listCursor = 0
				}
			}
			return m, nil

		// Expand all / Collapse all
		case msg.String() == " ":
			m.deleteArmed = false
			if len(m.profile.RequestFolders) == 0 {
				break
			}
			// If any collapsed → expand all; if all expanded → collapse all
			anyCollapsed := false
			for i := range m.profile.RequestFolders {
				if !m.expandedURLs[i] {
					anyCollapsed = true
					break
				}
			}
			for i := range m.profile.RequestFolders {
				m.expandedURLs[i] = anyCollapsed
			}
			m.listItems = m.buildListItems()
			if m.listCursor >= len(m.listItems) {
				m.listCursor = max(0, len(m.listItems)-1)
			}
			return m, nil

		// Delete environment (D key, double-press → confirm screen)
		case msg.String() == "D":
			env := m.currentEnv()
			if env == nil || env.Name == "local" {
				break
			}
			if !m.deleteArmed {
				m.armGeneration++
				m.deleteArmed = true
				m.deleteArmedIsEnv = true
				return m, armDisarmAfter(m.armGeneration)
			}
			// Second D: show confirmation screen
			m.deleteArmed = false
			m.deleting = env.Name
			m.confirmInput.SetValue("")
			m.confirmInput.Focus()
			m.screen = reqScreenConfirmDelete
			return m, textinput.Blink

		case key.Matches(msg, opsKeyUp):
			m.deleteArmed = false
			if m.listCursor > 0 {
				m.listCursor--
				// Skip disabled items
				for m.listCursor > 0 && !m.listItems[m.listCursor].enabled {
					m.listCursor--
				}
			}

		case key.Matches(msg, opsKeyDown):
			m.deleteArmed = false
			if m.listCursor < len(m.listItems)-1 {
				m.listCursor++
				// Skip disabled items
				for m.listCursor < len(m.listItems)-1 && !m.listItems[m.listCursor].enabled {
					m.listCursor++
				}
				// If we stopped at a disabled item, go back
				if m.listCursor < len(m.listItems) && !m.listItems[m.listCursor].enabled {
					m.listCursor--
				}
			}

		case opsIsJumpTop(msg):
			m.deleteArmed = false
			for i := 0; i < len(m.listItems); i++ {
				if m.listItems[i].enabled {
					m.listCursor = i
					break
				}
			}

		case opsIsJumpBottom(msg):
			m.deleteArmed = false
			for i := len(m.listItems) - 1; i >= 0; i-- {
				if m.listItems[i].enabled {
					m.listCursor = i
					break
				}
			}

		case key.Matches(msg, opsKeyEnter):
			m.deleteArmed = false
			if m.listCursor < len(m.listItems) {
				item := m.listItems[m.listCursor]
				if !item.enabled {
					return m, nil
				}
				switch item.kind {
				case reqItemURL:
					// Toggle expand/collapse (only if URL is set)
					if m.currentEnvURL(item.label) == "" {
						break // can't expand without URL
					}
					m.expandedURLs[item.urlIdx] = !m.expandedURLs[item.urlIdx]
					m.listItems = m.buildListItems()
					if m.listCursor >= len(m.listItems) {
						m.listCursor = max(0, len(m.listItems)-1)
					}
				case reqItemRequest:
					// Execute request
					if item.reqIdx < len(m.profile.RequestDefs) {
						return m.startVarPrompt(m.profile.RequestDefs[item.reqIdx], false)
					}
				case reqItemAddURL:
					return m.startCreateURL()
				}
			}

		case msg.String() == "alt+enter":
			// Force re-prompt all variables
			m.deleteArmed = false
			if m.listCursor < len(m.listItems) {
				item := m.listItems[m.listCursor]
				if item.kind == reqItemRequest && item.enabled {
					if item.reqIdx < len(m.profile.RequestDefs) {
						return m.startVarPrompt(m.profile.RequestDefs[item.reqIdx], true)
					}
				}
			}

		case msg.String() == "c":
			// Copy request as cURL to clipboard
			m.deleteArmed = false
			m.copyFeedback = ""
			if m.listCursor < len(m.listItems) {
				item := m.listItems[m.listCursor]
				if item.kind == reqItemRequest && item.enabled {
					if item.reqIdx < len(m.profile.RequestDefs) {
						curl := m.buildCurl(m.profile.RequestDefs[item.reqIdx])
						_ = clipboard.WriteAll(curl)
						m.copyFeedback = "copied cURL"
					}
				}
			}
			return m, nil

		case key.Matches(msg, opsKeyEdit):
			m.deleteArmed = false
			if m.listCursor < len(m.listItems) {
				item := m.listItems[m.listCursor]
				if item.kind == reqItemRequest && item.enabled {
					if item.reqIdx < len(m.profile.RequestDefs) {
						return m.startEditRequest(item.reqIdx)
					}
				}
				if item.kind == reqItemURL && item.enabled {
					if item.urlIdx < len(m.profile.RequestFolders) {
						return m.startEditURL(item.urlIdx)
					}
				}
			}

		case key.Matches(msg, opsKeyDelete):
			if m.listCursor < len(m.listItems) {
				item := m.listItems[m.listCursor]
				if !item.enabled {
					break
				}
				// Only allow delete on actual URLs and Requests, not add items
				if item.kind != reqItemURL && item.kind != reqItemRequest {
					break
				}
				if !m.deleteArmed {
					m.armGeneration++
					m.deleteArmed = true
					m.deleteArmedIsEnv = false
					return m, armDisarmAfter(m.armGeneration)
				} else {
					m.deleteArmed = false
					switch item.kind {
					case reqItemURL:
						if item.urlIdx < len(m.profile.RequestFolders) {
							// Delete folder + all child requests (global)
							folderName := m.profile.RequestFolders[item.urlIdx].Name
							var newReqs []config.RequestDef
							for _, r := range m.profile.RequestDefs {
								if r.BaseURLName != folderName {
									newReqs = append(newReqs, r)
								}
							}
							m.profile.RequestDefs = newReqs
							m.removeFolderFromAllEnvs(folderName)
							m.profile.RequestFolders = append(m.profile.RequestFolders[:item.urlIdx], m.profile.RequestFolders[item.urlIdx+1:]...)
							// Shift expandedURLs keys for indices after the deleted one
							delete(m.expandedURLs, item.urlIdx)
							newExpanded := make(map[int]bool)
							for k, v := range m.expandedURLs {
								if k > item.urlIdx {
									newExpanded[k-1] = v
								} else {
									newExpanded[k] = v
								}
							}
							m.expandedURLs = newExpanded
							m.profile.UpdatedAt = time.Now()
							if err := m.cfg.UpdateProfile(*m.profile); err != nil {
								m.listItems = m.buildListItems()
								return m, nil
							}
							m.listItems = m.buildListItems()
							if m.listCursor >= len(m.listItems) && m.listCursor > 0 {
								m.listCursor--
							}
						}
					case reqItemRequest:
						if item.reqIdx < len(m.profile.RequestDefs) {
							m.profile.RequestDefs = append(m.profile.RequestDefs[:item.reqIdx], m.profile.RequestDefs[item.reqIdx+1:]...)
							m.profile.UpdatedAt = time.Now()
							if err := m.cfg.UpdateProfile(*m.profile); err != nil {
								m.listItems = m.buildListItems()
								return m, nil
							}
							m.listItems = m.buildListItems()
							if m.listCursor >= len(m.listItems) && m.listCursor > 0 {
								m.listCursor--
							}
						}
					}
				}
			}

		default:
			m.deleteArmed = false
		}
	}
	return m, nil
}

func (m *RequestOpsModel) viewList() string {
	var b strings.Builder
	b.WriteString("\n")

	env := m.currentEnv()
	envName := m.envName
	if env != nil {
		envName = env.Name
	}
	b.WriteString(styles.StyleTitle.Render(
		fmt.Sprintf(" Request Ops — %s / %s ", m.profile.Name, envName)))
	b.WriteString("\n\n")

	// Environment tabs
	if len(m.profile.RequestEnvs) > 1 {
		var tabs []string
		for i, e := range m.profile.RequestEnvs {
			if i == m.activeEnv {
				tabs = append(tabs, styles.StyleBadgeEnv.Render(" "+e.Name+" "))
			} else {
				tabs = append(tabs, " "+e.Name+" ")
			}
		}
		b.WriteString("  " + strings.Join(tabs, "  ") + "\n\n")
	}

	if env == nil {
		b.WriteString(styles.StyleError.Render("  Environment not found."))
		b.WriteString("\n")
		return b.String()
	}

	selStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)
	mutedStyle := styles.StyleSubtitle
	cursorStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)

	// Windowing
	totalItems := len(m.listItems)
	overhead := 13
	if m.deleteArmed {
		overhead++
	}
	win := opsWindow(totalItems, m.listCursor, 1, m.height, overhead)

	if win.hasAbove {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  ↑ %d more", win.start)))
		b.WriteString("\n")
	}

	// Render hierarchical tree
	for i := win.start; i < win.end; i++ {
		item := m.listItems[i]
		isCursor := i == m.listCursor

		if !item.enabled {
			b.WriteString(mutedStyle.Render("      "+item.label) + "\n")
			continue
		}

		switch item.kind {
		case reqItemURL:
			// Folder with ▸ / ▾ icon
			icon := "▸"
			if m.expandedURLs[item.urlIdx] {
				icon = "▾"
			}
			// Show URL (or placeholder if empty)
			urlDisplay := item.sublabel
			if urlDisplay == "" {
				urlDisplay = "(no URL)"
			}
			if isCursor {
				b.WriteString(selStyle.Render(fmt.Sprintf("  > %s %s", icon, item.label)) + "  " + mutedStyle.Render(urlDisplay) + "\n")
			} else {
				b.WriteString(fmt.Sprintf("    %s %s", icon, item.label) + "  " + mutedStyle.Render(urlDisplay) + "\n")
			}

		case reqItemRequest:
			// Child request with └ prefix and method badge
			methodIdx := strings.Index(item.sublabel, " ")
			methodBadge := renderMethodBadge(item.sublabel[:methodIdx])
			path := item.sublabel[methodIdx+1:]
			if isCursor {
				b.WriteString(selStyle.Render("  >  └ "+item.label) + "  " + methodBadge + " " + mutedStyle.Render(path) + "\n")
			} else {
				b.WriteString("      └ " + item.label + "  " + methodBadge + " " + mutedStyle.Render(path) + "\n")
			}

		case reqItemAddURL:
			if isCursor {
				b.WriteString(cursorStyle.Render("  > "+item.label) + "\n")
			} else {
				b.WriteString("    " + mutedStyle.Render(item.label) + "\n")
			}
		}
	}

	if win.hasBelow {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  ↓ %d more", totalItems-win.end)))
		b.WriteString("\n")
	}

	if len(m.listItems) == 0 {
		b.WriteString("\n")
		b.WriteString(mutedStyle.Render("  No folders configured."))
		b.WriteString("\n")
		b.WriteString(mutedStyle.Render("  Press a to add one."))
		b.WriteString("\n")
	}

	if m.deleteArmed {
		if m.deleteArmedIsEnv {
			b.WriteString("\n" + styles.StyleError.Render("  Press D again to confirm removal"))
		} else {
			b.WriteString("\n" + styles.StyleError.Render("  Press d again to confirm removal"))
		}
		b.WriteString("\n")
	}

	if m.copyFeedback != "" {
		b.WriteString("\n  " + styles.StyleSuccess.Render("✓") + " " + m.copyFeedback)
		b.WriteString("\n")
	}

	// Show spinner for in-flight requests
	for label := range m.pendingOps {
		b.WriteString("\n  " + m.renderSpinnerDot(label))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	var contextItems []styles.HelpItemResult
	if m.listCursor < len(m.listItems) {
		item := m.listItems[m.listCursor]
		if item.kind == reqItemURL {
			contextItems = append(contextItems, styles.HelpItem("a", "add"))
			contextItems = append(contextItems, styles.HelpItem("e", "edit"))
			contextItems = append(contextItems, styles.HelpItem("d d", "delete"))
		} else if item.kind == reqItemRequest {
			contextItems = append(contextItems, styles.HelpItem("c", "copy"))
			contextItems = append(contextItems, styles.HelpItem("a", "add"))
			contextItems = append(contextItems, styles.HelpItem("e", "edit"))
			contextItems = append(contextItems, styles.HelpItem("d d", "delete"))
		}
	}
	return b.String()
}

// ── Add Environment screen ────────────────────────────────────────────────────

func (m *RequestOpsModel) updateAddEnv(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)
	if !isKey {
		var cmd tea.Cmd
		m.newEnvInput, cmd = m.newEnvInput.Update(msg)
		return m, cmd
	}

	switch keyMsg.String() {
	case "esc":
		m.screen = reqScreenList
		m.newEnvInput.SetValue("")
		m.newEnvInput.Blur()
		return m, nil

	case "enter":
		name := strings.TrimSpace(m.newEnvInput.Value())
		if name == "" {
			return m, nil
		}
		// Check for duplicate
		for _, env := range m.profile.RequestEnvs {
			if env.Name == name {
				return m, nil
			}
		}
		// Append new environment with empty BaseURL entries for all global folders
		newEnv := config.RequestEnv{Name: name}
		for _, folder := range m.profile.RequestFolders {
			newEnv.BaseURLs = append(newEnv.BaseURLs, config.RequestBaseURL{Name: folder.Name, URL: ""})
		}
		m.profile.RequestEnvs = append(m.profile.RequestEnvs, newEnv)
		m.cfg.UpdateProfile(*m.profile)
		// Switch to the new env
		m.activeEnv = len(m.profile.RequestEnvs) - 1
		m.envName = name
		m.listItems = m.buildListItems()
		m.screen = reqScreenList
		m.newEnvInput.SetValue("")
		m.newEnvInput.Blur()
		return m, nil
	}

	var cmd tea.Cmd
	m.newEnvInput, cmd = m.newEnvInput.Update(keyMsg)
	return m, cmd
}

func (m *RequestOpsModel) viewAddEnv() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.StyleTitle.Render(fmt.Sprintf(" Request Ops — %s / Add Environment ", m.profile.Name)))
	b.WriteString("\n\n")

	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	b.WriteString("  " + focusStyle.Render("Environment name: ") + m.newEnvInput.View() + "\n")
	b.WriteString("\n")
	b.WriteString("  " + styles.InlineHelp(
		styles.HelpItem("esc", "cancel"),
		styles.HelpItem("↵", "create"),
	))
	return b.String()
}

// ── Confirm Delete Environment screen ────────────────────────────────────────

func (m *RequestOpsModel) updateConfirmDelete(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)
	if !isKey {
		var cmd tea.Cmd
		m.confirmInput, cmd = m.confirmInput.Update(msg)
		return m, cmd
	}

	switch keyMsg.String() {
	case "esc":
		m.deleting = ""
		m.confirmInput.SetValue("")
		m.confirmInput.Blur()
		m.screen = reqScreenList
		return m, nil

	case "enter":
		if m.confirmInput.Value() == m.deleting {
			// Confirmed: delete environment
			envIdx := m.activeEnv
			m.profile.RequestEnvs = append(
				m.profile.RequestEnvs[:envIdx],
				m.profile.RequestEnvs[envIdx+1:]...,
			)
			m.profile.UpdatedAt = time.Now()
			_ = m.cfg.UpdateProfile(*m.profile)
			m.deleting = ""
			m.confirmInput.SetValue("")
			m.confirmInput.Blur()
			// Switch to remaining env
			if len(m.profile.RequestEnvs) > 0 {
				if m.activeEnv >= len(m.profile.RequestEnvs) {
					m.activeEnv = len(m.profile.RequestEnvs) - 1
				}
				m.envName = m.profile.RequestEnvs[m.activeEnv].Name
			} else {
				m.activeEnv = 0
				m.envName = ""
			}
			m.screen = reqScreenList
			m.listItems = m.buildListItems()
			if m.listCursor >= len(m.listItems) && m.listCursor > 0 {
				m.listCursor = len(m.listItems) - 1
			}
			return m, nil
		}
		// Wrong input
		m.formError = fmt.Sprintf("name does not match — type %q to confirm", m.deleting)
		m.confirmInput.SetValue("")
		m.confirmInput.CursorEnd()
		return m, nil

	default:
		var cmd tea.Cmd
		m.confirmInput, cmd = m.confirmInput.Update(keyMsg)
		return m, cmd
	}
}

func (m *RequestOpsModel) viewConfirmDelete() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.StyleTitle.Render(fmt.Sprintf(" Request Ops — %s / Delete Environment ", m.profile.Name)))
	b.WriteString("\n\n")

	b.WriteString(styles.StylePanelError.Render(
		styles.StyleError.Render("  ⚠ This will permanently delete the environment and ALL its data"),
	))
	b.WriteString("\n\n")
	nameStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	b.WriteString(styles.StyleSubtitle.Render(
		fmt.Sprintf("  Type %s to confirm deletion:", nameStyle.Render(fmt.Sprintf("%q", m.deleting))),
	))
	b.WriteString("\n\n")
	b.WriteString("  " + m.confirmInput.View())
	if m.formError != "" {
		b.WriteString("\n\n" + formatErrorMsg(m.formError))
	}
	b.WriteString("\n\n")
	b.WriteString(styles.InlineHelp(
		styles.HelpItem("esc", "cancel"),
		styles.HelpItem("↵", "confirm"),
	))
	b.WriteString("\n")
	return b.String()
}

// ── Form (Create / Edit) ─────────────────────────────────────────────────────

func (m *RequestOpsModel) startCreateURL() (tea.Model, tea.Cmd) {
	m.screen = reqScreenCreateForm
	m.formFocus = 0
	m.editingIdx = -1
	m.isURLForm = true
	m.nameInput.SetValue("")
	m.nameInput.Placeholder = "Url"
	m.pathInput.SetValue("")
	m.pathInput.Placeholder = "http://localhost:8080"
	m.formError = ""
	return m, nil
}

func (m *RequestOpsModel) startCreateRequest() (tea.Model, tea.Cmd) {
	m.screen = reqScreenCreateForm
	m.formFocus = 0
	m.editingIdx = -2 // -2 = new request
	m.isURLForm = false
	m.nameInput.SetValue("")
	m.nameInput.Placeholder = "Login"
	m.methodName = "GET"
	m.methodCursor = 0
	if len(m.profile.RequestFolders) > 0 {
		m.baseURLIdx = 0
	} else {
		m.baseURLIdx = -1
	}
	m.isLogin = false
	m.nameInput.SetValue("")
	m.pathInput.SetValue("")
	m.pathInput.Placeholder = "/api/v1/users (appended to base)"
	m.tokenPathInput.SetValue("")
	m.headerKV = []config.RequestKV{{Key: "Content-Type", Value: "application/json"}}
	m.paramKV = nil
	m.bodyInput.SetValue("")
	m.formError = ""
	return m, nil
}

func (m *RequestOpsModel) startCreateRequestForURL(urlIdx int) (tea.Model, tea.Cmd) {
	m.parentURLIdx = urlIdx
	result, cmd := m.startCreateRequest()
	m.baseURLIdx = urlIdx
	return result, cmd
}

func (m *RequestOpsModel) startEditURL(idx int) (tea.Model, tea.Cmd) {
	if idx < 0 || idx >= len(m.profile.RequestFolders) {
		return m, nil
	}
	folder := m.profile.RequestFolders[idx]
	url := m.currentEnvURL(folder.Name)
	m.screen = reqScreenEditForm
	m.formFocus = 0
	m.editingIdx = idx
	m.isURLForm = true
	m.nameInput.SetValue(folder.Name)
	m.nameInput.Placeholder = "Url"
	m.baseURLIdx = -1
	m.pathInput.SetValue(url)
	m.pathInput.Placeholder = "http://localhost:8080"
	m.isLogin = false
	m.tokenPathInput.SetValue("")
	m.headerKV = nil
	m.paramKV = nil
	m.bodyInput.SetValue("")
	m.formError = ""
	return m, nil
}

func (m *RequestOpsModel) startEditRequest(idx int) (tea.Model, tea.Cmd) {
	if idx < 0 || idx >= len(m.profile.RequestDefs) {
		return m, nil
	}
	req := m.profile.RequestDefs[idx]
	m.screen = reqScreenEditForm
	m.formFocus = 0
	m.editingIdx = idx
	m.isURLForm = false
	m.nameInput.SetValue(req.Name)
	m.methodName = req.Method
	m.methodCursor = 0
	for i, method := range httpMethods {
		if method == req.Method {
			m.methodCursor = i
			break
		}
	}
	// Find base URL index in global folders
	m.baseURLIdx = -1
	for i, folder := range m.profile.RequestFolders {
		if folder.Name == req.BaseURLName {
			m.baseURLIdx = i
			break
		}
	}
	m.pathInput.SetValue(req.Path)
	m.isLogin = req.IsLogin
	m.tokenPathInput.SetValue(req.TokenPath)
	m.headerKV = make([]config.RequestKV, len(req.Headers))
	copy(m.headerKV, req.Headers)
	// Auto-add Content-Type if not present
	hasContentType := false
	for _, kv := range m.headerKV {
		if strings.EqualFold(kv.Key, "Content-Type") {
			hasContentType = true
			break
		}
	}
	if !hasContentType {
		m.headerKV = append([]config.RequestKV{{Key: "Content-Type", Value: "application/json"}}, m.headerKV...)
	}
	m.paramKV = make([]config.RequestKV, len(req.Params))
	copy(m.paramKV, req.Params)
	m.bodyInput.SetValue(req.Body)
	m.formError = ""
	return m, nil
}

// formFieldName returns the display name of the current form field.
func (m *RequestOpsModel) formFieldName(idx int) string {
	switch idx {
	case 0:
		return "Name"
	case 1:
		return "Method"
	case 2:
		return "Base URL"
	case 3:
		return "Path"
	case 4:
		return "Is Login"
	case 5:
		return "Token Path"
	case 6:
		return "Headers"
	case 7:
		return "Params"
	case 8:
		return "Body"
	}
	return ""
}

// hasBodyField returns true if the current method supports a request body.
func (m *RequestOpsModel) hasBodyField() bool {
	methodUpper := strings.ToUpper(m.methodName)
	return methodUpper == "POST" || methodUpper == "PUT" || methodUpper == "PATCH"
}

// formFieldCount returns the number of visible form fields.
func (m *RequestOpsModel) formFieldCount() int {
	if m.isURLForm {
		return 2 // Name + URL
	}
	// Indices: 0=Name, 1=Method, 2=BaseURL, 3=Path, 4=IsLogin, 5=TokenPath, 6=Headers, 7=Params, 8=Body
	// TokenPath(5) is a gap when isLogin=false, but max index is always 8 or 7.
	// formFieldCount returns maxVisibleIndex+1 for boundary checks.
	if m.hasBodyField() {
		return 9 // max index = 8 (Body)
	}
	return 8 // max index = 7 (Params)
}

// formVisibleIndex maps a formFocus to the logical field.
func (m *RequestOpsModel) formVisibleIndex(focus int) int {
	return focus
}

// formHasEnterAction returns true if Enter does something useful on the current field.
func (m *RequestOpsModel) formHasEnterAction() bool {
	if m.isURLForm {
		return false // Name and URL are plain text inputs
	}
	switch m.formFocus {
	case 1: // Method cycles
		return true
	case 2: // Base URL cycles
		return true
	case 4: // Is Login toggles
		return true
	case 6, 7: // Headers/Params → KV builder
		return true
	case 8: // Body → edit mode
		return true
	default:
		return false // Name, Path, TokenPath are text inputs
	}
}

func (m *RequestOpsModel) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)
	if !isKey {
		var cmd tea.Cmd
		switch m.formFocus {
		case 0:
			m.nameInput, cmd = m.nameInput.Update(msg)
		case 1:
			if m.isURLForm {
				m.pathInput, cmd = m.pathInput.Update(msg)
			}
		case 3:
			m.pathInput, cmd = m.pathInput.Update(msg)
		case 5:
			m.tokenPathInput, cmd = m.tokenPathInput.Update(msg)
		case 8:
			if m.bodyEditMode {
				m.bodyInput, cmd = m.bodyInput.Update(msg)
			}
		}
		return m, cmd
	}

	// KV builder mode
	if m.kvBuilding {
		return m.updateKVBuilder(keyMsg)
	}

	switch keyMsg.String() {
	case "esc":
		if m.bodyEditMode {
			m.bodyInput.Blur()
			m.bodyEditMode = false
			return m, nil
		}
		m.nameInput.Blur()
		m.pathInput.Blur()
		m.tokenPathInput.Blur()
		m.screen = reqScreenList
		m.listItems = m.buildListItems()
		return m, nil

	case "up":
		if m.formFocus == 8 && m.bodyEditMode {
			if m.bodyInput.Line() > 0 {
				break // textarea handles cursor up within text
			}
		}
		if m.formFocus > 0 {
			m.formFocus--
			// Skip TokenPath (5) when Is Login is off
			if !m.isLogin && !m.isURLForm && m.formFocus == 5 {
				m.formFocus--
			}
			// Skip Body (8) when method doesn't support body
			if !m.isURLForm && m.formFocus == 8 && !m.hasBodyField() {
				m.formFocus--
			}
			m.refreshFormFocus()
		}
		return m, nil

	case "down":
		if m.formFocus == 8 && m.bodyEditMode {
			totalLines := len(strings.Split(m.bodyInput.Value(), "\n"))
			if m.bodyInput.Line() < totalLines-1 {
				break // textarea handles cursor down within text
			}
		}
		if m.formFocus < m.formFieldCount()-1 {
			m.formFocus++
			// Skip TokenPath (5) when Is Login is off
			if !m.isLogin && !m.isURLForm && m.formFocus == 5 {
				m.formFocus++
			}
			// Skip Body (8) when method doesn't support body
			if !m.isURLForm && m.formFocus == 8 && !m.hasBodyField() {
				m.formFocus++
			}
			m.refreshFormFocus()
		}
		return m, nil

	case "enter":
		// Toggle selectors and toggles (only for request form fields)
		if !m.isURLForm {
			switch m.formFocus {
			case 1: // Method selector
				m.methodCursor = (m.methodCursor + 1) % len(httpMethods)
				m.methodName = httpMethods[m.methodCursor]
				// Clamp cursor if Body field just disappeared
				if m.formFocus >= m.formFieldCount() {
					m.formFocus = m.formFieldCount() - 1
				}
				m.refreshFormFocus()
				return m, nil
			case 2: // Base URL selector
				if len(m.profile.RequestFolders) > 0 {
					m.baseURLIdx = (m.baseURLIdx + 1) % len(m.profile.RequestFolders)
				}
				return m, nil
			case 4: // Is Login toggle
				m.isLogin = !m.isLogin
				// If we just turned off and cursor is on TokenPath, move up
				if !m.isLogin && m.formFocus == 5 {
					m.formFocus = 4
				}
				// Clamp if cursor is now out of range
				if m.formFocus >= m.formFieldCount() {
					m.formFocus = m.formFieldCount() - 1
				}
				m.refreshFormFocus()
				return m, nil
			case 6: // Headers — enter KV builder
				m.enterKVBuilder("headers")
				return m, nil
			case 7: // Params — enter KV builder
				m.enterKVBuilder("params")
				return m, nil
			case 8: // Body
				if !m.bodyEditMode {
					m.bodyInput.Focus()
					m.bodyEditMode = true
					return m, nil
				}
				// Enter in edit mode = newline, delegate
				var cmd tea.Cmd
				m.bodyInput, cmd = m.bodyInput.Update(keyMsg)
				m.setBodyTextareaHeight()
				return m, cmd
			}
		}

		// On the last field, save
		if m.formFocus == m.formFieldCount()-1 {
			return m.saveForm()
		}

	case "ctrl+s":
		return m.saveForm()

	case "ctrl+f":
		if m.formFocus == 8 && m.bodyEditMode {
			m.formatBody()
		}
		return m, nil
	}

	// Delegate to focused input
	switch m.formFocus {
	case 0:
		var cmd tea.Cmd
		m.nameInput, cmd = m.nameInput.Update(keyMsg)
		return m, cmd
	case 1:
		if m.isURLForm {
			var cmd tea.Cmd
			m.pathInput, cmd = m.pathInput.Update(keyMsg)
			return m, cmd
		}
	case 3:
		var cmd tea.Cmd
		m.pathInput, cmd = m.pathInput.Update(keyMsg)
		return m, cmd
	case 5:
		if m.isLogin {
			var cmd tea.Cmd
			m.tokenPathInput, cmd = m.tokenPathInput.Update(keyMsg)
			return m, cmd
		}
	case 8:
		if m.bodyEditMode {
			var cmd tea.Cmd
			m.bodyInput, cmd = m.bodyInput.Update(keyMsg)
			m.setBodyTextareaHeight()
			return m, cmd
		}
	}

	return m, nil
}

func (m *RequestOpsModel) refreshFormFocus() {
	// Blur all inputs, focus the current one
	m.nameInput.Blur()
	m.pathInput.Blur()
	m.tokenPathInput.Blur()
	if m.bodyEditMode {
		m.bodyInput.Blur()
		m.bodyEditMode = false
	}
	switch m.formFocus {
	case 0:
		m.nameInput.Focus()
	case 3:
		m.pathInput.Focus()
	case 5:
		if m.isLogin {
			m.tokenPathInput.Focus()
		}
	}
}

func (m *RequestOpsModel) setBodyTextareaHeight() {
	lines := len(strings.Split(m.bodyInput.Value(), "\n"))
	maxH := m.height - 20
	if maxH < 5 {
		maxH = 5
	}
	switch {
	case lines < 6:
		m.bodyInput.SetHeight(min(6, maxH))
	case lines > maxH:
		m.bodyInput.SetHeight(maxH)
	default:
		m.bodyInput.SetHeight(lines)
	}
}

func (m *RequestOpsModel) formatBody() {
	val := m.bodyInput.Value()
	var pretty bytes.Buffer
	if json.Indent(&pretty, []byte(val), "", "  ") == nil {
		m.bodyInput.SetValue(strings.TrimRight(pretty.String(), "\n"))
		m.setBodyTextareaHeight()
	}
}

func (m *RequestOpsModel) saveForm() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(m.nameInput.Value())
	method := m.methodName
	path := strings.TrimSpace(m.pathInput.Value())

	// Validate
	if name == "" {
		m.formError = "Name is required"
		return m, nil
	}

	if m.isURLForm {
		// Save as global Base URL folder
		url := path // pathInput holds the URL value in URL form
		if url == "" {
			m.formError = "URL is required"
			return m, nil
		}
		if m.screen == reqScreenEditForm && m.editingIdx >= 0 && m.editingIdx < len(m.profile.RequestFolders) {
			oldName := m.profile.RequestFolders[m.editingIdx].Name
			if name != oldName {
				m.renameFolderInAllEnvs(oldName, name)
			}
			m.profile.RequestFolders[m.editingIdx].Name = name
			m.setCurrentEnvURL(name, url)
		} else {
			m.profile.RequestFolders = append(m.profile.RequestFolders, config.RequestFolder{Name: name})
			m.syncFolderToAllEnvs(name)
			m.setCurrentEnvURL(name, url)
		}
		m.profile.UpdatedAt = time.Now()
		if err := m.cfg.UpdateProfile(*m.profile); err != nil {
			m.formError = err.Error()
			return m, nil
		}
		m.screen = reqScreenList
		m.listItems = m.buildListItems()
		return m, nil
	}

	// Validate request fields
	if method == "" {
		m.formError = "Method is required"
		return m, nil
	}
	if m.baseURLIdx < 0 || m.baseURLIdx >= len(m.profile.RequestFolders) {
		m.formError = "Base URL is required"
		return m, nil
	}
	if path == "" {
		m.formError = "Path is required"
		return m, nil
	}
	if m.isLogin && strings.TrimSpace(m.tokenPathInput.Value()) == "" {
		m.formError = "Token Path is required when Is Login is enabled"
		return m, nil
	}

	body := ""
	if m.bodyInput.Value() != "" {
		body = m.bodyInput.Value()
	}

	reqDef := config.RequestDef{
		Name:        name,
		Method:      method,
		BaseURLName: m.profile.RequestFolders[m.baseURLIdx].Name,
		Path:        path,
		IsLogin:     m.isLogin,
		TokenPath:   strings.TrimSpace(m.tokenPathInput.Value()),
		Headers:     m.headerKV,
		Params:      m.paramKV,
		Body:        body,
	}

	// Check if editing an existing request
	if m.screen == reqScreenEditForm && m.editingIdx >= 0 && m.editingIdx < len(m.profile.RequestDefs) {
		m.profile.RequestDefs[m.editingIdx] = reqDef
	} else {
		m.profile.RequestDefs = append(m.profile.RequestDefs, reqDef)
	}

	m.profile.UpdatedAt = time.Now()
	if err := m.cfg.UpdateProfile(*m.profile); err != nil {
		m.formError = err.Error()
		return m, nil
	}

	m.screen = reqScreenList
	m.listItems = m.buildListItems()
	return m, nil
}

func (m *RequestOpsModel) viewForm() string {
	var b strings.Builder
	b.WriteString("\n")

	title := "New Base URL"
	if m.screen == reqScreenCreateForm {
		if !m.isURLForm {
			title = "New Request"
		}
	} else if m.screen == reqScreenEditForm {
		if m.isURLForm {
			title = "Edit Base URL"
		} else {
			title = "Edit Request"
		}
	}
	b.WriteString(styles.StyleTitle.Render(fmt.Sprintf(" Request Ops — %s ", title)))
	b.WriteString("\n\n")

	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	normalStyle := lipgloss.NewStyle()
	mutedStyle := styles.StyleSubtitle

	// Field 0: Name
	if m.formFocus == 0 {
		m.nameInput.Focus()
		b.WriteString("  " + focusStyle.Render("Name:     ") + m.nameInput.View() + "\n")
	} else {
		m.nameInput.Blur()
		b.WriteString("  " + normalStyle.Render("Name:     ") + m.nameInput.View() + "\n")
	}

	if !m.isURLForm {
		// Field 1: Method selector
		methodDisplay := m.methodName
		if m.formFocus == 1 {
			b.WriteString("  " + focusStyle.Render("Method:   ") + focusStyle.Render("> "+methodDisplay) + "  " + mutedStyle.Render("(enter to cycle)") + "\n")
		} else {
			b.WriteString("  " + normalStyle.Render("Method:   ") + renderMethodBadge(methodDisplay) + "\n")
		}

		// Field 2: Base URL selector
		if m.formFocus == 2 {
			buLabel := "(none)"
			if m.baseURLIdx >= 0 && m.baseURLIdx < len(m.profile.RequestFolders) {
				buLabel = m.profile.RequestFolders[m.baseURLIdx].Name
			}
			b.WriteString("  " + focusStyle.Render("Base URL: ") + focusStyle.Render("> "+buLabel) + "  " + mutedStyle.Render("(enter to cycle)") + "\n")
		} else {
			buLabel := "(none)"
			if m.baseURLIdx >= 0 && m.baseURLIdx < len(m.profile.RequestFolders) {
				folderName := m.profile.RequestFolders[m.baseURLIdx].Name
				buLabel = folderName + "  " + mutedStyle.Render(m.currentEnvURL(folderName))
			}
			b.WriteString("  " + normalStyle.Render("Base URL: ") + buLabel + "\n")
		}

		// Field 3: Path
		if m.formFocus == 3 {
			m.pathInput.Focus()
			b.WriteString("  " + focusStyle.Render("Path:     ") + m.pathInput.View() + "\n")
		} else {
			m.pathInput.Blur()
			b.WriteString("  " + normalStyle.Render("Path:     ") + m.pathInput.View() + "\n")
		}

		// Field 4: Is Login toggle
		loginVal := "No"
		if m.isLogin {
			loginVal = "Yes"
		}
		if m.formFocus == 4 {
			b.WriteString("  " + focusStyle.Render("Is Login: ") + focusStyle.Render("> "+loginVal) + "  " + mutedStyle.Render("(enter to toggle)") + "\n")
		} else {
			b.WriteString("  " + normalStyle.Render("Is Login: ") + loginVal + "\n")
		}

		// Field 5: Token Path (only when IsLogin)
		if m.isLogin {
			if m.formFocus == 5 {
				m.tokenPathInput.Focus()
				b.WriteString("  " + focusStyle.Render("TokenPath: ") + m.tokenPathInput.View() + "\n")
			} else {
				m.tokenPathInput.Blur()
				b.WriteString("  " + normalStyle.Render("TokenPath: ") + m.tokenPathInput.View() + "\n")
			}
		}

		// Headers
		headerDisplay := "press enter to add"
		if len(m.headerKV) > 0 {
			var parts []string
			for _, kv := range m.headerKV {
				parts = append(parts, kv.Key+"="+kv.Value)
			}
			headerDisplay = strings.Join(parts, ", ")
			if len(headerDisplay) > 50 {
				headerDisplay = headerDisplay[:47] + "..."
			}
		}
		if m.formFocus == 6 {
			b.WriteString("  " + focusStyle.Render("Headers:  ") + focusStyle.Render(headerDisplay) + "\n")
		} else {
			b.WriteString("  " + normalStyle.Render("Headers:  ") + mutedStyle.Render(headerDisplay) + "\n")
		}

		// Params
		paramDisplay := "press enter to add"
		if len(m.paramKV) > 0 {
			var parts []string
			for _, kv := range m.paramKV {
				parts = append(parts, kv.Key+"="+kv.Value)
			}
			paramDisplay = strings.Join(parts, ", ")
			if len(paramDisplay) > 50 {
				paramDisplay = paramDisplay[:47] + "..."
			}
		}
		if m.formFocus == 7 {
			b.WriteString("  " + focusStyle.Render("Params:   ") + focusStyle.Render(paramDisplay) + "\n")
		} else {
			b.WriteString("  " + normalStyle.Render("Params:   ") + mutedStyle.Render(paramDisplay) + "\n")
		}

		// Field 8: Body (only for POST/PUT/PATCH)
		methodUpper := strings.ToUpper(m.methodName)
		if methodUpper == "POST" || methodUpper == "PUT" || methodUpper == "PATCH" {
			if m.formFocus == 8 && m.bodyEditMode {
				b.WriteString("  " + focusStyle.Render("Body:     ") + "\n\n")
				ta := m.bodyInput.View()
				taStyle := lipgloss.NewStyle().
					Border(lipgloss.RoundedBorder()).
					BorderForeground(styles.ColorPrimary).
					Padding(0, 1)
				b.WriteString(taStyle.Render(ta))
				b.WriteString("\n")
			} else if m.formFocus == 8 {
				preview := m.bodyInput.Value()
				if preview == "" {
					preview = "(press enter to edit)"
				} else if len(preview) > 60 {
					preview = preview[:57] + "..."
				}
				b.WriteString("  " + focusStyle.Render("Body:     ") + mutedStyle.Render(preview) + "\n")
			} else {
				preview := m.bodyInput.Value()
				if preview == "" {
					preview = "-"
				} else if len(preview) > 60 {
					preview = preview[:57] + "..."
				}
				b.WriteString("  " + normalStyle.Render("Body:     ") + mutedStyle.Render(preview) + "\n")
			}
		}
	}

	// URL form: show URL field (field 1)
	if m.isURLForm {
		if m.formFocus == 1 {
			m.pathInput.Focus()
			m.pathInput.Placeholder = "http://localhost:8080"
			b.WriteString("  " + focusStyle.Render("URL:      ") + m.pathInput.View() + "\n")
		} else {
			m.pathInput.Blur()
			b.WriteString("  " + normalStyle.Render("URL:      ") + m.pathInput.View() + "\n")
		}
	}

	if m.formError != "" {
		b.WriteString("\n  " + formatErrorMsg(m.formError) + "\n")
	}

	b.WriteString("\n")

	if m.bodyEditMode {
		b.WriteString("  " + styles.InlineHelp(
			styles.HelpItem("esc", "exit edit"),
			styles.HelpItem("ctrl+f", "format"),
			styles.HelpItem("ctrl+s", "save"),
		))
	} else {
		helpItems := []styles.HelpItemResult{
			styles.HelpItem("esc", "cancel"),
		}
		// Only show ↵ hint when Enter actually does something
		if m.formHasEnterAction() {
			if m.formFocus == 6 || m.formFocus == 7 {
				helpItems = append(helpItems, styles.HelpItem("↵", "edit"))
			} else if m.formFocus == 8 {
				helpItems = append(helpItems, styles.HelpItem("↵", "edit"))
			} else {
				helpItems = append(helpItems, styles.HelpItem("↵", "select/toggle"))
			}
		}
		helpItems = append(helpItems, styles.HelpItem("ctrl+s", "save"))
		b.WriteString("  " + styles.InlineHelp(helpItems...))
	}

	return b.String()
}

// ── KV Builder (for Headers and Params) ──────────────────────────────────────

func (m *RequestOpsModel) enterKVBuilder(target string) {
	m.kvBuilding = true
	m.kvTarget = target
	m.kvPhase = 0
	m.kvDeleteArmed = false
	m.kvOverwriteIdx = -1
	m.kvPairs = nil

	if target == "headers" {
		m.kvPairs = make([][2]string, len(m.headerKV))
		for i, kv := range m.headerKV {
			m.kvPairs[i] = [2]string{kv.Key, kv.Value}
		}
		// Auto-add Content-Type if not present
		hasContentType := false
		for _, p := range m.kvPairs {
			if strings.EqualFold(p[0], "Content-Type") {
				hasContentType = true
				break
			}
		}
		if !hasContentType {
			m.kvPairs = append([][2]string{{"Content-Type", "application/json"}}, m.kvPairs...)
		}
	} else {
		m.kvPairs = make([][2]string, len(m.paramKV))
		for i, kv := range m.paramKV {
			m.kvPairs[i] = [2]string{kv.Key, kv.Value}
		}
	}

	m.varPromptInput.SetValue("")
	m.varPromptInput.Focus()
	m.varPromptInput.Placeholder = "key name (leave empty to finish)"
	m.varPromptInput.CursorEnd()
}

func (m *RequestOpsModel) updateKVBuilder(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.kvBuilding = false
		m.kvPairs = nil
		m.kvOverwriteIdx = -1
		m.kvContentTypeKey = false
		m.kvContentTypeIdx = 0
		m.varPromptInput.SetValue("")
		m.varPromptInput.Blur()
		return m, nil

	case "enter":
		val := strings.TrimSpace(m.varPromptInput.Value())
		m.varPromptInput.SetValue("")

		if m.kvPhase == 0 {
			if val == "" {
				// Finish builder
				m.finishKVBuilder()
				return m, nil
			}
			// Check for duplicate key — overwrite instead of discard
			for i, pair := range m.kvPairs {
				if pair[0] == val {
					m.kvOverwriteIdx = i
					m.kvPhase = 1
					m.kvContentTypeKey = strings.EqualFold(val, "Content-Type")
					if m.kvContentTypeKey {
						// Initialize selector at matching option or 0
						m.kvContentTypeIdx = 0
						for j, opt := range contentTypeOptions {
							if opt == m.kvPairs[i][1] {
								m.kvContentTypeIdx = j
								break
							}
						}
						m.varPromptInput.SetValue("")
						m.varPromptInput.Blur()
					} else {
						m.varPromptInput.SetValue("")
						m.varPromptInput.Focus()
						m.varPromptInput.Placeholder = "value for " + val + " (overwrite)"
						m.varPromptInput.CursorEnd()
					}
					return m, nil
				}
			}
			m.kvOverwriteIdx = -1
			m.kvPairs = append(m.kvPairs, [2]string{val, ""})
			m.kvPhase = 1
			m.kvContentTypeKey = strings.EqualFold(val, "Content-Type")
			if m.kvContentTypeKey {
				m.kvContentTypeIdx = 0
				m.varPromptInput.SetValue("")
				m.varPromptInput.Blur()
			} else {
				m.varPromptInput.Focus()
				m.varPromptInput.Placeholder = "value for " + val
				m.varPromptInput.CursorEnd()
			}
		} else {
			// Phase 1 — confirm value
			if m.kvContentTypeKey {
				val = contentTypeOptions[m.kvContentTypeIdx]
			}
			if len(m.kvPairs) > 0 {
				if m.kvOverwriteIdx >= 0 && m.kvOverwriteIdx < len(m.kvPairs) {
					m.kvPairs[m.kvOverwriteIdx][1] = val
				} else {
					m.kvPairs[len(m.kvPairs)-1][1] = val
				}
			}
			m.kvOverwriteIdx = -1
			m.kvPhase = 0
			m.kvContentTypeKey = false
			m.varPromptInput.SetValue("")
			m.varPromptInput.Focus()
			m.varPromptInput.Placeholder = "key name (leave empty to finish)"
			m.varPromptInput.CursorEnd()
		}
		m.kvDeleteArmed = false
		m.kvOverwriteIdx = -1
		return m, nil

	case "left":
		if m.kvPhase == 1 && m.kvContentTypeKey {
			m.kvContentTypeIdx--
			if m.kvContentTypeIdx < 0 {
				m.kvContentTypeIdx = len(contentTypeOptions) - 1
			}
			return m, nil
		}
		// Pass through to input if focused
		if m.varPromptInput.Focused() {
			var cmd tea.Cmd
			m.varPromptInput, cmd = m.varPromptInput.Update(msg)
			return m, cmd
		}

	case "right":
		if m.kvPhase == 1 && m.kvContentTypeKey {
			m.kvContentTypeIdx++
			if m.kvContentTypeIdx >= len(contentTypeOptions) {
				m.kvContentTypeIdx = 0
			}
			return m, nil
		}
		if m.varPromptInput.Focused() {
			var cmd tea.Cmd
			m.varPromptInput, cmd = m.varPromptInput.Update(msg)
			return m, cmd
		}

	case "d":
		// Only arm delete when input is empty, we're in key-entry phase,
		// and last pair is NOT Content-Type (Content-Type cannot be deleted)
		if m.kvPhase != 0 || len(m.kvPairs) == 0 || m.varPromptInput.Value() != "" {
			m.kvDeleteArmed = false
			if m.varPromptInput.Focused() {
				var cmd tea.Cmd
				m.varPromptInput, cmd = m.varPromptInput.Update(msg)
				return m, cmd
			}
			break
		}
		lastPair := m.kvPairs[len(m.kvPairs)-1]
		if strings.EqualFold(lastPair[0], "Content-Type") {
			// Content-Type cannot be deleted
			return m, nil
		}
		if m.kvDeleteArmed {
			m.kvPairs = m.kvPairs[:len(m.kvPairs)-1]
			m.kvDeleteArmed = false
		} else {
			m.kvDeleteArmed = true
		}
		return m, nil

	default:
		m.kvDeleteArmed = false
		if m.varPromptInput.Focused() {
			var cmd tea.Cmd
			m.varPromptInput, cmd = m.varPromptInput.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *RequestOpsModel) finishKVBuilder() {
	kvs := make([]config.RequestKV, len(m.kvPairs))
	for i, p := range m.kvPairs {
		kvs[i] = config.RequestKV{Key: p[0], Value: p[1]}
	}
	if m.kvTarget == "headers" {
		m.headerKV = kvs
	} else {
		m.paramKV = kvs
	}
	m.kvBuilding = false
	m.kvPairs = nil
	m.kvOverwriteIdx = -1
	m.kvContentTypeKey = false
	m.kvContentTypeIdx = 0
	m.varPromptInput.SetValue("")
	m.varPromptInput.Blur()
}

func (m *RequestOpsModel) viewKVBuilder() string {
	var b strings.Builder
	target := "Headers"
	if m.kvTarget == "params" {
		target = "Params"
	}
	b.WriteString(styles.StyleSubtitle.Render("  "+target) + "\n\n")

	pairStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent)
	if len(m.kvPairs) > 0 {
		b.WriteString("  " + styles.StyleHelp.Render("Current pairs:") + "\n")
		for _, p := range m.kvPairs {
			b.WriteString("    " + pairStyle.Render(p[0]) + "  →  " + p[1] + "\n")
		}
		b.WriteString("\n")
	}

	promptStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	if m.kvPhase == 0 {
		b.WriteString("  " + promptStyle.Render("▸ Key") + "  (leave empty to finish)\n\n")
	} else if m.kvContentTypeKey {
		b.WriteString("  " + promptStyle.Render("▸ Value for Content-Type") + "  (← → to cycle)\n\n")
		opt := contentTypeOptions[m.kvContentTypeIdx]
		arrowStyle := lipgloss.NewStyle().Foreground(styles.ColorMuted)
		optStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		b.WriteString("    " + arrowStyle.Render("← ") + optStyle.Render(opt) + arrowStyle.Render(" →") + "\n")
	} else {
		k := m.kvPairs[len(m.kvPairs)-1][0]
		b.WriteString("  " + promptStyle.Render("▸ Value for "+k) + "\n\n")
	}
	if !m.kvContentTypeKey {
		b.WriteString("  " + m.varPromptInput.View() + "\n")
	}

	if m.kvDeleteArmed {
		b.WriteString("\n" + styles.StyleError.Render("  Press d again to remove last pair") + "\n")
	}

	helpItems := []styles.HelpItemResult{
		styles.HelpItem("esc", "cancel"),
		styles.HelpItem("↵", "confirm"),
	}
	// Show d d hint only if there's at least one deletable pair
	if m.kvPhase == 0 && len(m.kvPairs) > 0 {
		hasDeletable := false
		for _, p := range m.kvPairs {
			if m.kvTarget == "headers" && strings.EqualFold(p[0], "Content-Type") {
				continue
			}
			hasDeletable = true
			break
		}
		if hasDeletable {
			helpItems = append(helpItems, styles.HelpItem("d d", "delete last"))
		}
	}
	b.WriteString("\n  " + styles.InlineHelp(helpItems...))
	return b.String()
}

// ── Variable prompt ───────────────────────────────────────────────────────────

// varRe matches {var_name} patterns (but not {token}).
var varRe = regexp.MustCompile(`\{([\w-]+)\}`)

// startVarPrompt checks variables and decides whether to prompt or execute directly.
// forcePrompt=true means always show the prompt (used by Option+Enter).
func (m *RequestOpsModel) startVarPrompt(req config.RequestDef, forcePrompt bool) (tea.Model, tea.Cmd) {
	env := m.currentEnv()
	if env == nil {
		m.screen = reqScreenList
		return m, nil
	}

	// Collect all {var} references from body, headers, params, path
	allRefs := req.VariableRefs
	textsToScan := []string{req.Path, req.Body}
	for _, h := range req.Headers {
		textsToScan = append(textsToScan, h.Key, h.Value)
	}
	for _, p := range req.Params {
		textsToScan = append(textsToScan, p.Key, p.Value)
	}
	for _, text := range textsToScan {
		matches := varRe.FindAllStringSubmatch(text, -1)
		for _, match := range matches {
			name := match[1]
			if name == "token" {
				continue
			}
			found := false
			for _, ref := range allRefs {
				if ref == name {
					found = true
					break
				}
			}
			if !found {
				allRefs = append(allRefs, name)
			}
		}
	}

	if len(allRefs) == 0 {
		// No variables needed, execute directly
		return m.executeRequest(req)
	}

	// Check if all variables already have cached values
	if !forcePrompt {
		allCached := true
		for _, name := range allRefs {
			found := false
			for _, ev := range env.Variables {
				if ev.Name == name && ev.Value != "" {
					found = true
					break
				}
			}
			if !found {
				allCached = false
				break
			}
		}
		if allCached {
			// All variables have values — execute directly
			m.varPromptValues = make(map[string]string)
			for _, name := range allRefs {
				for _, ev := range env.Variables {
					if ev.Name == name {
						m.varPromptValues[name] = ev.Value
						break
					}
				}
			}
			return m.executeRequest(req)
		}
	}

	// Initialize prompt values from env cache (pre-fill what we have)
	m.varPromptValues = make(map[string]string)
	m.varPromptNames = make([]string, len(allRefs))
	copy(m.varPromptNames, allRefs) // stable order from allRefs slice
	for _, name := range allRefs {
		m.varPromptValues[name] = "" // always register, default empty
		for _, ev := range env.Variables {
			if ev.Name == name {
				m.varPromptValues[name] = ev.Value
				break
			}
		}
	}

	m.varPromptIdx = 0
	m.screen = reqScreenVarPrompt
	m.refreshVarPromptInput()

	return m, nil
}

func (m *RequestOpsModel) refreshVarPromptInput() {
	if m.varPromptIdx >= len(m.varPromptNames) {
		return
	}
	name := m.varPromptNames[m.varPromptIdx]
	// Pre-fill with existing value if available
	if existing, ok := m.varPromptValues[name]; ok && existing != "" {
		m.varPromptInput.SetValue(existing)
	} else {
		m.varPromptInput.SetValue("")
	}
	m.varPromptInput.Placeholder = name
	m.varPromptInput.Focus()
	m.varPromptInput.CursorEnd()
}

// varPromptNames returns the ordered list of variable names.
func (m *RequestOpsModel) getVarPromptNames() []string {
	return m.varPromptNames
}

func (m *RequestOpsModel) updateVarPrompt(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)
	if !isKey {
		var cmd tea.Cmd
		m.varPromptInput, cmd = m.varPromptInput.Update(keyMsg)
		return m, cmd
	}

	names := m.getVarPromptNames()
	if m.varPromptIdx >= len(names) {
		m.screen = reqScreenList
		return m, nil
	}

	switch keyMsg.String() {
	case "esc":
		m.varPromptInput.Blur()
		m.screen = reqScreenList
		m.varPromptValues = nil
		m.varPromptNames = nil
		return m, nil

	case "up":
		if m.varPromptIdx > 0 {
			// Save current value
			val := strings.TrimSpace(m.varPromptInput.Value())
			m.varPromptValues[names[m.varPromptIdx]] = val
			m.varPromptIdx--
			m.refreshVarPromptInput()
		}
		return m, nil

	case "down":
		if m.varPromptIdx < len(names)-1 {
			val := strings.TrimSpace(m.varPromptInput.Value())
			m.varPromptValues[names[m.varPromptIdx]] = val
			m.varPromptIdx++
			m.refreshVarPromptInput()
		}
		return m, nil

	case "enter":
		val := strings.TrimSpace(m.varPromptInput.Value())
		// If user typed nothing and there's an existing value, keep existing
		if val == "" {
			if existing, ok := m.varPromptValues[names[m.varPromptIdx]]; ok && existing != "" {
				val = existing
			}
		}
		m.varPromptValues[names[m.varPromptIdx]] = val
		m.varPromptIdx++
		if m.varPromptIdx >= len(names) {
			// All variables collected — execute
			return m.executeRequestFromPrompt()
		}
		m.refreshVarPromptInput()
		return m, nil

	}

	// Delegate to input
	var cmd tea.Cmd
	m.varPromptInput, cmd = m.varPromptInput.Update(keyMsg)
	return m, cmd
}

func (m *RequestOpsModel) viewVarPrompt() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.StyleTitle.Render(fmt.Sprintf(" Request Ops — %s / %s ", m.profile.Name, m.envName)))
	b.WriteString("\n\n")

	b.WriteString(styles.StyleSubtitle.Render("  Enter variable values") + "\n\n")

	names := m.getVarPromptNames()
	purpleStyle := lipgloss.NewStyle().Foreground(styles.ColorPurple).Bold(true)
	valStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)

	for i, name := range names {
		if i < m.varPromptIdx {
			// Already filled
			v := m.varPromptValues[name]
			display := v
			if len(display) > 40 {
				display = display[:37] + "..."
			}
			b.WriteString("  " + purpleStyle.Render("{"+name+"}") + " " + valStyle.Render(display) + "\n")
		} else if i == m.varPromptIdx {
			// Current — input
			b.WriteString("  " + purpleStyle.Render("{"+name+"}") + " " + m.varPromptInput.View() + "\n")
		} else {
			// Upcoming — show cached value if exists, otherwise empty
			if v, ok := m.varPromptValues[name]; ok && v != "" {
				display := v
				if len(display) > 40 {
					display = display[:37] + "..."
				}
				b.WriteString("  " + purpleStyle.Render("{"+name+"}") + " " + valStyle.Render(display) + "\n")
			} else {
				b.WriteString("  " + purpleStyle.Render("{"+name+"}") + " \n")
			}
		}
	}

	b.WriteString("\n")
	helpItems := []styles.HelpItemResult{
		styles.HelpItem("esc", "cancel"),
		styles.HelpItem("↵", "next"),
	}
	b.WriteString("  " + styles.InlineHelp(helpItems...))
	return b.String()
}

// ── HTTP Execution ────────────────────────────────────────────────────────────

func (m *RequestOpsModel) executeRequestFromPrompt() (tea.Model, tea.Cmd) {
	env := m.currentEnv()
	if env == nil {
		m.screen = reqScreenList
		return m, nil
	}

	// Persist prompted variable values to env.Variables
	for name, val := range m.varPromptValues {
		if name == "token" || val == "" {
			continue
		}
		found := false
		for i, ev := range env.Variables {
			if ev.Name == name {
				env.Variables[i].Value = val
				found = true
				break
			}
		}
		if !found {
			env.Variables = append(env.Variables, config.RequestEnvVar{
				Name:  name,
				Value: val,
			})
		}
	}
	_ = m.cfg.UpdateProfile(*m.profile)

	// Find which request we're executing (from the list cursor)
	if m.listCursor >= len(m.listItems) {
		m.screen = reqScreenList
		return m, nil
	}
	item := m.listItems[m.listCursor]
	if item.kind != reqItemRequest || item.reqIdx >= len(m.profile.RequestDefs) {
		m.screen = reqScreenList
		return m, nil
	}

	req := m.profile.RequestDefs[item.reqIdx]
	return m.doExecuteRequest(req)
}

func (m *RequestOpsModel) executeRequest(req config.RequestDef) (tea.Model, tea.Cmd) {
	m.screen = reqScreenList
	return m.doExecuteRequest(req)
}

func (m *RequestOpsModel) doExecuteRequest(req config.RequestDef) (tea.Model, tea.Cmd) {
	env := m.currentEnv()
	if env == nil {
		m.screen = reqScreenList
		return m, nil
	}

	// Find base URL from current env
	baseURL := m.currentEnvURL(req.BaseURLName)
	if baseURL == "" {
		m.screen = reqScreenList
		m.listItems = m.buildListItems()
		return m, nil
	}

	// Get token
	token := ""
	for _, ev := range env.Variables {
		if ev.Name == "_token" {
			token = ev.Value
			break
		}
	}

	// Build request
	path := req.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	fullURL := strings.TrimRight(baseURL, "/") + path

	// Substitute variables
	vars := m.varPromptValues
	if vars == nil {
		vars = make(map[string]string)
	}

	// Substitute variables in URL path
	fullURL = m.substituteVars(fullURL, vars, token)

	// Add query params
	if len(req.Params) > 0 {
		var paramParts []string
		for _, p := range req.Params {
			key := m.substituteVars(p.Key, vars, token)
			val := m.substituteVars(p.Value, vars, token)
			paramParts = append(paramParts, key+"="+val)
		}
		fullURL += "?" + strings.Join(paramParts, "&")
	}

	// Build body
	var bodyReader io.Reader
	if req.Body != "" {
		substitutedBody := m.substituteVars(req.Body, vars, token)
		bodyReader = bytes.NewReader([]byte(substitutedBody))
	}

	httpReq, err := http.NewRequest(req.Method, fullURL, bodyReader)
	if err != nil {
		m.screen = reqScreenList
		m.listItems = m.buildListItems()
		return m, nil
	}

	// Add headers
	for _, h := range req.Headers {
		key := m.substituteVars(h.Key, vars, token)
		val := m.substituteVars(h.Value, vars, token)
		httpReq.Header.Set(key, val)
	}
	if req.Body != "" && httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	label := req.Name
	m.pendingOps[label] = "running"
	m.screen = reqScreenList

	return m, tea.Batch(
		func() tea.Msg {
			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Do(httpReq)
			if err != nil {
				return requestOpsDoneMsg{err: fmt.Errorf("request failed: %w", err)}
			}
			defer resp.Body.Close()

			respData, _ := io.ReadAll(resp.Body)

			// If login request, extract token
			if req.IsLogin && req.TokenPath != "" && len(respData) > 0 {
				extracted := extractTokenFromJSON(respData, req.TokenPath)
				if extracted != "" {
					// Store token in env.Variables
					found := false
					for i, ev := range env.Variables {
						if ev.Name == "_token" {
							env.Variables[i].Value = extracted
							found = true
							break
						}
					}
					if !found {
						env.Variables = append(env.Variables, config.RequestEnvVar{
							Name:  "_token",
							Value: extracted,
						})
					}
					// Persist
					_ = m.cfg.UpdateProfile(*m.profile)
				}
			}

			// Format response
			statusLine := fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
			if len(respData) > 0 {
				var pretty bytes.Buffer
				if json.Indent(&pretty, respData, "", "  ") == nil {
					return requestOpsDoneMsg{result: statusLine + "\n\n" + pretty.String()}
				}
				return requestOpsDoneMsg{result: statusLine + "\n\n" + string(respData)}
			}
			return requestOpsDoneMsg{result: statusLine}
		},
		spinnerCmd(),
	)
}

// ── cURL builder ─────────────────────────────────────────────────────────────

func (m *RequestOpsModel) buildCurl(req config.RequestDef) string {
	// Resolve variables (same as execution)
	env := m.currentEnv()
	token := ""
	vars := make(map[string]string)
	if env != nil {
		for _, ev := range env.Variables {
			if ev.Name == "_token" {
				token = ev.Value
			} else if ev.Value != "" {
				vars[ev.Name] = ev.Value
			}
		}
	}
	// Merge cached prompt values (override env defaults)
	if m.varPromptValues != nil {
		for k, v := range m.varPromptValues {
			vars[k] = v
		}
	}

	var b strings.Builder
	b.WriteString("curl")

	// Method
	if req.Method != "" && req.Method != "GET" {
		b.WriteString(" -X " + req.Method)
	}

	// URL
	baseURL := m.currentEnvURL(req.BaseURLName)
	path := req.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	fullURL := strings.TrimRight(baseURL, "/") + path
	fullURL = m.substituteVars(fullURL, vars, token)

	// Query params
	if len(req.Params) > 0 {
		sep := "?"
		for _, p := range req.Params {
			key := m.substituteVars(p.Key, vars, token)
			val := m.substituteVars(p.Value, vars, token)
			fullURL += sep + key + "=" + val
			sep = "&"
		}
	}

	b.WriteString(" \\\n  '" + fullURL + "'")

	// Headers
	for _, h := range req.Headers {
		key := m.substituteVars(h.Key, vars, token)
		val := m.substituteVars(h.Value, vars, token)
		b.WriteString(" \\\n  -H '" + key + ": " + val + "'")
	}
	// Auto-add Content-Type for body methods if not already set
	if req.Method == "POST" || req.Method == "PUT" || req.Method == "PATCH" {
		hasCT := false
		for _, h := range req.Headers {
			if strings.EqualFold(h.Key, "Content-Type") {
				hasCT = true
				break
			}
		}
		if !hasCT {
			b.WriteString(" \\\n  -H 'Content-Type: application/json'")
		}
	}

	// Body
	if req.Body != "" && (req.Method == "POST" || req.Method == "PUT" || req.Method == "PATCH") {
		body := m.substituteVars(req.Body, vars, token)
		// Minify JSON if possible
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(body)); err == nil {
			body = compact.String()
		}
		// Escape single quotes in body for shell
		escaped := strings.ReplaceAll(body, "'", "'\\''")
		b.WriteString(" \\\n  -d '" + escaped + "'")
	}

	return b.String()
}

// ── Substitution helpers ──────────────────────────────────────────────────────

func (m *RequestOpsModel) substituteVars(s string, vars map[string]string, token string) string {
	result := s
	for k, v := range vars {
		result = strings.ReplaceAll(result, "{"+k+"}", v)
	}
	result = strings.ReplaceAll(result, "{token}", token)
	return result
}

func extractTokenFromJSON(data []byte, dotPath string) string {
	if dotPath == "" {
		return ""
	}
	var raw interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	parts := strings.Split(dotPath, ".")
	current := raw
	for _, part := range parts {
		switch v := current.(type) {
		case map[string]interface{}:
			val, ok := v[part]
			if !ok {
				return ""
			}
			current = val
		default:
			return ""
		}
	}
	switch v := current.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	case bool:
		return fmt.Sprintf("%t", v)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// ── View dispatch ─────────────────────────────────────────────────────────────

func (m *RequestOpsModel) View() string {
	switch m.screen {
	case reqScreenCreateForm, reqScreenEditForm:
		if m.kvBuilding {
			return m.viewKVBuilder()
		}
		return m.viewForm()
	case reqScreenVarPrompt:
		return m.viewVarPrompt()
	case reqScreenResult:
		return m.viewResult()
	case reqScreenAddEnv:
		return m.viewAddEnv()
	case reqScreenConfirmDelete:
		return m.viewConfirmDelete()
	default:
		return m.viewList()
	}
}

func (m *RequestOpsModel) viewResult() string {
	if !m.viewingResult {
		return m.viewList()
	}
	scrollHint := ""
	atTop := m.resultViewport.AtTop()
	atBottom := m.resultViewport.AtBottom()
	switch {
	case !atTop && !atBottom:
		scrollHint = "\n  " + styles.StyleHelp.Render("▲ up  ▼ down") + "\n"
	case !atTop:
		scrollHint = "\n  " + styles.StyleHelp.Render("▲ up") + "\n"
	case !atBottom:
		scrollHint = "\n  " + styles.StyleHelp.Render("▼ down") + "\n"
	}
	titleLine := styles.StyleHeader.Render("  " + m.resultTitle + "  —  result")
	footer := "press any key to close"
	if m.copied {
		footer = "✓ copied to clipboard  —  press any key to close"
	} else if m.resultRaw != "" {
		footer = "c copy  —  press any key to close"
	}
	return titleLine + "\n\n" + m.resultViewport.View() + scrollHint + "\n  " + styles.StyleHelp.Render(footer)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (m *RequestOpsModel) renderSpinnerDot(op string) string {
	return opsRenderSpinnerDot(m.spinnerFrame, op)
}

// renderMethodBadge renders a colored badge for an HTTP method.
func renderMethodBadge(method string) string {
	var color lipgloss.TerminalColor
	switch method {
	case "GET":
		color = styles.ColorPrimary
	case "POST":
		color = styles.ColorAccent
	case "PUT":
		color = styles.ColorCyan
	case "DELETE":
		color = lipgloss.Color("#FF5555") // red
	case "PATCH":
		color = styles.ColorCyan
	default:
		color = styles.ColorMuted
	}
	return lipgloss.NewStyle().
		Foreground(color).
		Bold(true).
		Render(strings.ToUpper(method))
}
