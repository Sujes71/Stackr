package views

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/docker"
	"github.com/stackr/stackr/internal/tui/styles"
)

// ── Internal screen ────────────────────────────────────────────────────────────

type mockOpsScreen int

const (
	mockScreenActions mockOpsScreen = iota
)

// ── Key bindings ───────────────────────────────────────────────────────────────

var (
	mockOpsUp    = key.NewBinding(key.WithKeys("up"))
	mockOpsDown  = key.NewBinding(key.WithKeys("down"))
	mockOpsEnter = key.NewBinding(key.WithKeys("enter"))
	mockOpsBack  = key.NewBinding(key.WithKeys("esc"))
)

// ── Model ──────────────────────────────────────────────────────────────────────

// MockOpsModel implements the MockServer operations view.
// Unlike DB Ops and Queue Ops, MockServer is purely local — there is no
// sync-env concept. The model goes directly to the actions screen.
type MockOpsModel struct {
	cfg         *config.Config
	profile     *config.Profile
	envName     string
	serviceName string
	serviceIdx  int

	// Actions state
	actions      []actionItem
	actionCursor int

	// Running state — per-action inline spinners
	pendingOps map[string]string
	lastResult string
	doneOp     string
	doneOpAt   time.Time

	// Spinner
	spinnerFrame int

	// Viewport
	height int
	width  int

	// Result panel overlay
	viewingResult   bool
	resultOutput    string
	resultTitle     string
	resultViewport  viewport.Model
	viewportContent string

	// Param prompting
	promptingParams bool
	paramsToPrompt  []string
	paramValues     map[string]string
	currentParamIdx int
	paramInput      textinput.Model
	paramTextarea   textarea.Model

	// Confirm before execute (double confirmation)
	confirmAction bool
	confirmLabel  string

	// Prompt state (start service before action)
	promptStartService bool
	promptStartDaemon  bool
	daemonStarting     bool
	pendingStartLabel  string
}

// ── Constructor ────────────────────────────────────────────────────────────────

func NewMockOps(cfg *config.Config, p *config.Profile, envName, serviceName string) *MockOpsModel {
	m := &MockOpsModel{
		cfg:         cfg,
		profile:     p,
		envName:     envName,
		serviceName: serviceName,
		pendingOps:  make(map[string]string),
		paramInput:  textinput.New(),
		paramTextarea: textarea.New(),
		resultViewport: viewport.New(0, 0),
	}
	vp := &m.resultViewport
	vp.KeyMap.Up.SetKeys("up")
	vp.KeyMap.Down.SetKeys("down")
	vp.KeyMap.PageUp.SetKeys()
	vp.KeyMap.PageDown.SetKeys()
	vp.KeyMap.HalfPageUp.SetKeys()
	vp.KeyMap.HalfPageDown.SetKeys()
	m.paramInput.Placeholder = "value"
	m.paramTextarea.SetHeight(8)
	m.paramTextarea.ShowLineNumbers = false
	m.paramTextarea.Placeholder = "{\n  \"key\": \"value\"\n}"
	m.paramTextarea.CharLimit = 0
	// Resolve service index for mutations
	for i, svc := range m.currentEnv().Services {
		if svc.Name == serviceName {
			m.serviceIdx = i
			break
		}
	}
	return m
}

// ── Getters ────────────────────────────────────────────────────────────────────

func (m *MockOpsModel) ProfileName() string { return m.profile.Name }
func (m *MockOpsModel) EnvName() string     { return m.envName }
func (m *MockOpsModel) ServiceName() string { return m.serviceName }

func (m *MockOpsModel) ServiceKey() string {
	return m.profile.Name + "/" + m.envName + "/" + m.serviceName
}

func (m *MockOpsModel) ReShow(cfg *config.Config) {
	if p, ok := cfg.GetProfile(m.profile.Name); ok {
		m.profile = p
	}
}

func (m *MockOpsModel) IsBusy() bool {
	return len(m.pendingOps) > 0
}

func (m *MockOpsModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	m.resultViewport.Width = w - 10
	m.resultViewport.Height = min(h-12, 40)
}

// ── Environment / Service helpers ──────────────────────────────────────────────

func (m *MockOpsModel) currentEnv() *config.Environment {
	for i := range m.profile.Environments {
		if m.profile.Environments[i].Name == m.envName {
			return &m.profile.Environments[i]
		}
	}
	return nil
}

func (m *MockOpsModel) currentService() *config.Service {
	env := m.currentEnv()
	if env == nil {
		return nil
	}
	if m.serviceIdx >= 0 && m.serviceIdx < len(env.Services) {
		svc := &env.Services[m.serviceIdx]
		if svc.Name == m.serviceName || svc.ComposeName() == m.serviceName {
			return svc
		}
	}
	for i := range env.Services {
		svc := &env.Services[i]
		if svc.Name == m.serviceName || svc.ComposeName() == m.serviceName {
			m.serviceIdx = i
			return svc
		}
	}
	return nil
}

// mockHostPort returns the host port mapped to the MockServer container port 1080.
func (m *MockOpsModel) mockHostPort() string {
	svc := m.currentService()
	if svc == nil {
		return "1080"
	}
	for _, p := range svc.Ports {
		if p.Container == "1080" {
			return p.Host
		}
	}
	return "1080"
}

// ── HTTP helper ────────────────────────────────────────────────────────────────

// mockAPICall runs a MockServer REST API call from the host to localhost:<port>.
// body is sent as JSON when non-nil; queryParams are added to the URL when non-nil.
// Returns a tea.Cmd that sends mockOpsDoneMsg with the result.
func (m *MockOpsModel) mockAPICall(endpoint, httpMethod string, body interface{}, queryParams map[string]string) func() tea.Cmd {
	return func() tea.Cmd {
		return func() tea.Msg {
			port := m.mockHostPort()

			// Build URL with query params
			apiURL := fmt.Sprintf("http://localhost:%s%s", port, endpoint)
			if len(queryParams) > 0 {
				var params []string
				for k, v := range queryParams {
					params = append(params, url.QueryEscape(k)+"="+url.QueryEscape(v))
				}
				apiURL += "?" + strings.Join(params, "&")
			}

			var reqBody io.Reader
			if body != nil {
				jsonBody, err := json.Marshal(body)
				if err != nil {
					return mockOpsDoneMsg{err: fmt.Errorf("invalid request: %w", err)}
				}
				reqBody = bytes.NewReader(jsonBody)
			}

			req, err := http.NewRequest(httpMethod, apiURL, reqBody)
			if err != nil {
				return mockOpsDoneMsg{err: fmt.Errorf("request failed: %w", err)}
			}
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}

			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				return mockOpsDoneMsg{err: fmt.Errorf("cannot reach MockServer at localhost:%s — is the service running?\n  %w", port, err)}
			}
			defer resp.Body.Close()

			respData, _ := io.ReadAll(resp.Body)

			if resp.StatusCode >= 400 {
				return mockOpsDoneMsg{err: fmt.Errorf("MockServer error (%d): %s", resp.StatusCode, strings.TrimSpace(string(respData)))}
			}

			if len(respData) > 0 {
				// Empty array/object → friendly message instead of "[]"
				trimmed := strings.TrimSpace(string(respData))
				if trimmed == "[]" {
					if retrieveType := queryParams["type"]; retrieveType == "REQUESTS" {
						return mockOpsDoneMsg{result: "No requests recorded yet.\nRequests appear when a client sends traffic to MockServer."}
					}
					return mockOpsDoneMsg{result: "No active expectations found.\nCreate one with 'Create expectation' above."}
				}
				if trimmed == "{}" {
					return mockOpsDoneMsg{result: "Empty response from MockServer."}
				}
				var pretty bytes.Buffer
				if json.Indent(&pretty, respData, "", "  ") == nil {
					return mockOpsDoneMsg{result: pretty.String()}
				}
				return mockOpsDoneMsg{result: string(respData)}
			}

			// No body — success with no meaningful output;
			// auto-dismiss and just show the checkmark.
			return mockOpsDoneMsg{result: ""}
		}
	}
}

// ── Init ───────────────────────────────────────────────────────────────────────

func (m *MockOpsModel) Init() tea.Cmd {
	m.actions = m.buildActionsList()
	m.actionCursor = m.firstEnabledAction()
	return nil
}

// ── Actions builder ────────────────────────────────────────────────────────────

func (m *MockOpsModel) buildActionsList() []actionItem {
	return []actionItem{
		// ── Expectations group ──
		{label: "Create expectation", group: "Expectations", enabled: true,
			execute: nil}, // params: method, path, status_code, content_type, body
		{label: "List active expectations", group: "Expectations", enabled: true,
			execute: m.mockAPICall("/mockserver/retrieve", "PUT", nil, map[string]string{"type": "ACTIVE_EXPECTATIONS"})},
		{label: "Clear by path", group: "Expectations", enabled: true,
			execute: nil}, // params: method, path
		{label: "Reset all", group: "Expectations", enabled: true,
			execute: nil}, // destructive, needs confirm

		// ── Verification group ──
		{label: "Verify request", group: "Verification", enabled: true,
			execute: nil}, // params: method, path, at_least
		{label: "Retrieve requests", group: "Verification", enabled: true,
			execute: m.mockAPICall("/mockserver/retrieve", "PUT", nil, map[string]string{"type": "REQUESTS"})},
	}
}

// ── Param definitions ──────────────────────────────────────────────────────────

func (m *MockOpsModel) actionParams(act actionItem) []string {
	switch act.label {
	case "Create expectation":
		return []string{"method", "path", "status_code", "content_type", "body"}
	case "Clear by path":
		return []string{"method", "path"}
	case "Verify request":
		return []string{"method", "path", "at_least"}
	}
	return nil
}

// paramDefault returns a pre-fill value for a known param key.
func (m *MockOpsModel) paramDefault(param string) string {
	switch param {
	case "method":
		return "GET"
	case "status_code":
		return "200"
	case "content_type":
		return "application/json"
	case "at_least":
		return "1"
	}
	return ""
}

// ── Param prompt execution (builds the HTTP request from param values) ─────────

func (m *MockOpsModel) execParamAction(act actionItem) tea.Cmd {
	switch act.label {
	case "Create expectation":
		statusCode := 200
		fmt.Sscanf(m.paramValues["status_code"], "%d", &statusCode)
		body := map[string]interface{}{
			"httpRequest": map[string]interface{}{
				"method": m.paramValues["method"],
				"path":   m.paramValues["path"],
			},
			"httpResponse": map[string]interface{}{
				"statusCode": statusCode,
				"headers": map[string][]string{
					"Content-Type": {m.paramValues["content_type"]},
				},
				"body": m.paramValues["body"],
			},
		}
		return m.mockAPICall("/mockserver/expectation", "PUT", body, nil)()

	case "Clear by path":
		body := map[string]interface{}{
			"httpRequest": map[string]interface{}{
				"method": m.paramValues["method"],
				"path":   m.paramValues["path"],
			},
		}
		return m.mockAPICall("/mockserver/clear", "PUT", body, nil)()

	case "Verify request":
		atLeast := 1
		fmt.Sscanf(m.paramValues["at_least"], "%d", &atLeast)
		body := map[string]interface{}{
			"httpRequest": map[string]interface{}{
				"method": m.paramValues["method"],
				"path":   m.paramValues["path"],
			},
			"times": map[string]int{
				"atLeast": atLeast,
				"atMost":  atLeast,
			},
		}
		return m.mockAPICall("/mockserver/verify", "PUT", body, nil)()
	}
	return nil
}

// ── Update ─────────────────────────────────────────────────────────────────────

func (m *MockOpsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle messages from completed actions
	switch msg := msg.(type) {
	case mockOpsDoneMsg:
		// Capture the completed label before clearing pendingOps
		var completedLabel string
		for label := range m.pendingOps {
			completedLabel = label
		}
		m.pendingOps = make(map[string]string)

		if msg.err != nil {
			m.lastResult = styles.StyleError.Render("✗ " + msg.err.Error())
			return m, nil
		}

		m.doneOp = completedLabel
		m.doneOpAt = time.Now()

		if msg.result == "" {
			// No output — auto-generated success, just show the checkmark.
			m.viewingResult = false
			m.resultOutput = ""
			return m, spinnerCmd()
		}
		// Show result in viewport
		m.resultOutput = msg.result
		m.resultTitle = completedLabel
		m.viewportContent = styles.StyleHeader.Render("  "+m.resultTitle+"  —  result") + "\n\n" + styles.StyleCode.Render(m.resultOutput)
		m.resultViewport.SetContent(m.viewportContent)
		m.resultViewport.Width = m.width - 4
		lineCount := strings.Count(m.viewportContent, "\n") + 1
		m.resultViewport.Height = min(lineCount, max(8, m.height-10))
		m.resultViewport.YPosition = 0
		m.resultViewport.GotoTop()
		m.viewingResult = true
		return m, spinnerCmd()
	}

	// Delegate to the actions screen
	return m.updateActions(msg)
}

func (m *MockOpsModel) updateActions(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle Docker daemon prompt
	if m.promptStartDaemon {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "y", "Y":
				m.promptStartDaemon = false
				m.daemonStarting = true
				m.lastResult = ""
				if m.pendingStartLabel != "" {
					m.pendingOps[m.pendingStartLabel] = "starting"
				}
				return m, tea.Batch(
					m.launchDockerCmd(),
					spinnerCmd(),
				)
			case "n", "N", "esc":
				m.promptStartDaemon = false
				m.pendingOps = make(map[string]string)
				m.pendingStartLabel = ""
				return m, nil
			}
		}
		return m, nil
	}

	// Block keys while Docker daemon is starting
	if m.daemonStarting {
		if _, ok := msg.(tea.KeyMsg); ok {
			return m, nil
		}
	}

	// Handle start-service prompt inline
	if m.promptStartService {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "y", "Y":
				m.promptStartService = false
				svc := m.currentService()
				if svc == nil {
					m.pendingOps = make(map[string]string)
					m.lastResult = styles.StyleError.Render("Service not found.")
					return m, nil
				}
				if m.pendingStartLabel != "" {
					m.pendingOps[m.pendingStartLabel] = "starting"
				}
				return m, tea.Batch(
					func() tea.Msg {
						err := docker.UpService(m.profile.Name, m.envName, svc.ComposeName())
						return serviceStartedMsg{err: err}
					},
					spinnerCmd(),
				)
			case "n", "N", "esc":
				m.promptStartService = false
				m.pendingOps = make(map[string]string)
				m.pendingStartLabel = ""
				return m, nil
			}
		}
		return m, nil
	}

	// Param prompting
	if m.promptingParams {
		return m.updateParamPrompt(msg)
	}

	// Confirm before destructive action
	if m.confirmAction {
		return m.updateConfirm(msg)
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.viewingResult {
			switch {
			case key.Matches(msg, mockOpsUp):
				m.resultViewport.LineUp(1)
			case key.Matches(msg, mockOpsDown):
				m.resultViewport.LineDown(1)
			default:
				m.viewingResult = false
				// Return spinner so the checkmark timer stays alive
				return m, spinnerCmd()
			}
			return m, nil
		}
		switch {
		case key.Matches(msg, mockOpsBack):
			if m.promptStartDaemon || len(m.pendingOps) > 0 {
				break
			}
			return m, func() tea.Msg {
				return NavigateTo{
					Screen:      NavProfileView,
					ProfileName: m.profile.Name,
					EnvName:     m.envName,
					ServiceName: m.serviceName,
				}
			}

		case key.Matches(msg, mockOpsUp) && len(m.pendingOps) == 0 && !m.promptStartService:
			for i := m.actionCursor - 1; i >= 0; i-- {
				if m.actions[i].enabled {
					m.actionCursor = i
					break
				}
			}

		case key.Matches(msg, mockOpsDown) && len(m.pendingOps) == 0 && !m.promptStartService:
			for i := m.actionCursor + 1; i < len(m.actions); i++ {
				if m.actions[i].enabled {
					m.actionCursor = i
					break
				}
			}

		case key.Matches(msg, mockOpsEnter) && len(m.pendingOps) == 0 && !m.promptStartService:
			if m.actionCursor < len(m.actions) {
				act := m.actions[m.actionCursor]
				if act.enabled {
					// Check for pre-defined actions that need param prompting
					if params := m.actionParams(act); len(params) > 0 {
						m.promptingParams = true
						m.paramsToPrompt = params
						m.paramValues = make(map[string]string)
						m.currentParamIdx = 0
						m.refreshParamInput()
						return m, nil
					}
					// Destructive actions need confirmation
					if act.label == "Reset all" {
						m.confirmAction = true
						m.confirmLabel = act.label
						return m, nil
					}
					// Immediate action
					if act.execute != nil {
						m.lastResult = ""
						m.doneOp = act.label
						m.doneOpAt = time.Now()
						m.pendingOps[act.label] = "running"
						return m, tea.Batch(act.execute(), spinnerCmd())
					}
				}
			}
		}
	}

	// Spinner tick — advance frame
	if _, ok := msg.(SpinnerTickMsg); ok {
		if len(m.pendingOps) > 0 {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			return m, spinnerCmd()
		}
		// Keep spinning for doneOp timer
		if m.doneOp != "" && time.Since(m.doneOpAt) < 5*time.Second {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			return m, spinnerCmd()
		}
		m.doneOp = ""
		m.doneOpAt = time.Time{}
	}

	return m, nil
}

// ── Confirm handler ────────────────────────────────────────────────────────────

func (m *MockOpsModel) updateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "y", "Y":
			m.confirmAction = false
			label := m.confirmLabel
			switch label {
			case "Reset all":
				m.lastResult = ""
				m.doneOp = label
				m.doneOpAt = time.Now()
				m.pendingOps[label] = "running"
				return m, tea.Batch(
					m.mockAPICall("/mockserver/reset", "PUT", nil, nil)(),
					spinnerCmd(),
				)
			}
		case "n", "N", "esc":
			m.confirmAction = false
		}
	}
	return m, nil
}

// ── Param prompt handler ───────────────────────────────────────────────────────

// refreshParamInput syncs the input's value and placeholder to the current param.
func (m *MockOpsModel) refreshParamInput() {
	param := m.paramsToPrompt[m.currentParamIdx]
	if param == "body" {
		m.paramInput.Blur()
		m.paramTextarea.Focus()
		if v, ok := m.paramValues[param]; ok && v != "" {
			m.paramTextarea.SetValue(v)
		} else {
			m.paramTextarea.SetValue("")
		}
		m.paramTextarea.CursorEnd()
	} else {
		m.paramTextarea.Blur()
		m.paramInput.Focus()
		if v, ok := m.paramValues[param]; ok && v != "" {
			m.paramInput.SetValue(v)
		} else {
			m.paramInput.SetValue("")
		}
		m.paramInput.Placeholder = m.paramDefault(param)
		m.paramInput.CursorEnd()
	}
}

func (m *MockOpsModel) currentParamValue() string {
	param := m.paramsToPrompt[m.currentParamIdx]
	if param == "body" {
		return m.paramTextarea.Value()
	}
	return m.paramInput.Value()
}

func (m *MockOpsModel) setCurrentParamValue() {
	param := m.paramsToPrompt[m.currentParamIdx]
	val := m.currentParamValue()
	if param != "body" {
		val = strings.TrimSpace(val)
	}
	m.paramValues[param] = val
}

func (m *MockOpsModel) updateParamPrompt(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		isBody := m.paramsToPrompt[m.currentParamIdx] == "body"

		switch msg.String() {
		case "esc":
			m.promptingParams = false
			return m, nil

		case "up":
			if isBody && m.paramTextarea.Line() > 0 {
				break // let textarea handle cursor up
			}
			if m.currentParamIdx > 0 {
				m.setCurrentParamValue()
				m.currentParamIdx--
				m.refreshParamInput()
			}
			return m, nil

		case "down":
			if isBody && m.paramTextarea.Line() < len(strings.Split(m.paramTextarea.Value(), "\n"))-1 {
				break // let textarea handle cursor down
			}
			if m.currentParamIdx < len(m.paramsToPrompt)-1 {
				m.setCurrentParamValue()
				m.currentParamIdx++
				m.refreshParamInput()
			}
			return m, nil

		case "enter":
			if isBody {
				break // let textarea add newline
			}
			return m.submitCurrentParam()

		case "ctrl+enter":
			if isBody {
				return m.submitCurrentParam()
			}
			return m, nil
		}

		// Delegate to active input
		if isBody {
			var cmd tea.Cmd
			m.paramTextarea, cmd = m.paramTextarea.Update(msg)
			return m, cmd
		}
		if m.paramInput.Focused() {
			var cmd tea.Cmd
			m.paramInput, cmd = m.paramInput.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// submitCurrentParam saves the current param and either advances or executes.
func (m *MockOpsModel) submitCurrentParam() (tea.Model, tea.Cmd) {
	m.setCurrentParamValue()

	if m.currentParamIdx < len(m.paramsToPrompt)-1 {
		m.currentParamIdx++
		m.refreshParamInput()
		return m, nil
	}

	// All params filled — execute
	m.promptingParams = false
	act := m.actions[m.actionCursor]
	execCmd := m.execParamAction(act)
	if execCmd == nil {
		return m, nil
	}
	m.lastResult = ""
	m.doneOp = act.label
	m.doneOpAt = time.Now()
	m.pendingOps[act.label] = "running"
	return m, tea.Batch(execCmd, spinnerCmd())
}

// ── View ───────────────────────────────────────────────────────────────────────

func (m *MockOpsModel) View() string {
	return m.viewActions()
}

func (m *MockOpsModel) viewActions() string {
	if m.viewingResult {
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
		return m.resultViewport.View() + scrollHint + "\n  " + styles.StyleHelp.Render("press any key to close")
	}

	var b strings.Builder
	b.WriteString("\n")

	b.WriteString(styles.StyleTitle.Render(
		fmt.Sprintf(" MockServer Ops — %s ", m.serviceName)))
	b.WriteString("\n\n")
	b.WriteString(styles.StyleSubtitle.Render(
		"  Target: local (HTTP API)  |  Type: mockserver"))
	b.WriteString("\n\n")

	// Param prompting
	if m.promptingParams {
		b.WriteString(m.viewParamPrompt())
		return b.String()
	}

	// Confirm destructive
	if m.confirmAction {
		promptStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		b.WriteString(styles.StyleError.Render("  ✗ This action clears ALL active expectations.") + "\n")
		b.WriteString("  " + promptStyle.Render("▸ Continue?") + "  " + styles.StyleSubtitle.Render("(y/n/esc)") + "\n\n")
		return b.String()
	}

	// Action group sections
	groupHeaderStyle := lipgloss.NewStyle().
		Foreground(styles.ColorPrimary).
		Bold(true)
	selStyle := lipgloss.NewStyle().
		Foreground(styles.ColorPrimary).
		Bold(true)

	currentGroup := ""
	for i, act := range m.actions {
		if act.group != currentGroup {
			currentGroup = act.group
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString("  " + groupHeaderStyle.Render(currentGroup))
			b.WriteString("\n")
		}

		isCursor := i == m.actionCursor

		if !act.enabled {
			b.WriteString(styles.StyleSubtitle.Render("      " + act.label) + "\n")
			if act.disabledReason != "" {
				b.WriteString(styles.StyleSubtitle.Render("      unavailable: " + act.disabledReason) + "\n")
			}
			continue
		}

		if state, pending := m.pendingOps[act.label]; pending {
			b.WriteString("    " + m.renderSpinnerDot(state) + "\n")
			continue
		}
		if m.doneOp == act.label && time.Since(m.doneOpAt) < 5*time.Second {
			b.WriteString("    " + styles.StyleSuccess.Render("✓") + " " + act.label + "\n")
			continue
		}
		if len(m.pendingOps) > 0 {
			b.WriteString("      " + act.label + "\n")
			continue
		}

		if isCursor {
			b.WriteString(selStyle.Render("    > " + act.label) + "\n")
		} else {
			b.WriteString("      " + act.label + "\n")
		}
	}

	b.WriteString("\n")

	if m.lastResult != "" {
		b.WriteString("  " + m.lastResult + "\n\n")
	}

	if m.promptStartDaemon {
		promptStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		b.WriteString(styles.StyleError.Render("  Docker daemon is not running.") + "\n")
		b.WriteString("  " + promptStyle.Render("▸ Start it?") + "  " + styles.StyleSubtitle.Render("(y/n/esc)") + "\n\n")
	} else if m.promptStartService {
		promptStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		b.WriteString(styles.StyleError.Render("  Service '" + m.serviceName + "' is not running.") + "\n")
		b.WriteString("  " + promptStyle.Render("▸ Start it?") + "  " + styles.StyleSubtitle.Render("(y/n/esc)") + "\n\n")
	}

	groups := map[string]bool{}
	for _, act := range m.actions {
		groups[act.group] = true
	}
	b.WriteString("\n")
	b.WriteString("  " + styles.InlineHelp(
		styles.HelpItem("↑ ↓", "navigate"),
		styles.HelpItem("↵", "execute"),
		styles.HelpItem("esc", "back"),
	))
	return b.String()
}

// ── Param prompt view ──────────────────────────────────────────────────────────

func (m *MockOpsModel) viewParamPrompt() string {
	var b strings.Builder
	b.WriteString(styles.StyleSubtitle.Render("  Enter parameter values") + "\n\n")

	cursorStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	filledStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	emptyStyle := lipgloss.NewStyle().Foreground(styles.ColorMuted)

	onBody := m.paramsToPrompt[m.currentParamIdx] == "body"

	for i, param := range m.paramsToPrompt {
		if i == m.currentParamIdx && onBody && param == "body" {
			// Show the textarea when body is active — render above the param list
			continue
		}
		if i == m.currentParamIdx {
			b.WriteString("  " + cursorStyle.Render("> "+param+"  ") + m.paramInput.View() + "\n")
		} else if v, ok := m.paramValues[param]; ok && v != "" {
			b.WriteString("    " + param + "  " + filledStyle.Render(v) + "\n")
		} else {
			b.WriteString("    " + param + "  " + emptyStyle.Render("-") + "\n")
		}
	}

	if onBody {
		// Show the body label with cursor
		b.WriteString("  " + cursorStyle.Render("> body  ") + "\n\n")
		// Render textarea inside a subtle box
		ta := m.paramTextarea.View()
		taStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(styles.ColorPrimary).
			Padding(0, 1)
		b.WriteString(taStyle.Render(ta))
		b.WriteString("\n")
	}

	b.WriteString("\n")

	if onBody {
		b.WriteString("  " + styles.InlineHelp(
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("ctrl+enter", "submit"),
		))
	} else {
		enterLabel := "next"
		if m.currentParamIdx == len(m.paramsToPrompt)-1 {
			enterLabel = "submit"
		}
		b.WriteString("  " + styles.InlineHelp(
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("↑ ↓", "navigate"),
			styles.HelpItem("enter", enterLabel),
		))
	}
	return b.String()
}

// ── Helpers ────────────────────────────────────────────────────────────────────

func (m *MockOpsModel) renderSpinnerDot(op string) string {
	frame := spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
	return lipgloss.NewStyle().Foreground(styles.ColorAccent).Render(fmt.Sprintf("%s %s", frame, op))
}

func (m *MockOpsModel) firstEnabledAction() int {
	for i, act := range m.actions {
		if act.enabled {
			return i
		}
	}
	return 0
}

func (m *MockOpsModel) isContainerRunning(container string) bool {
	statuses, err := docker.Status(m.profile.Name, m.envName)
	if err != nil {
		return false
	}
	for _, s := range statuses {
		if s.Name == container && s.State == "running" {
			return true
		}
	}
	return false
}

func (m *MockOpsModel) launchDockerCmd() tea.Cmd {
	return func() tea.Msg {
		if _, err := exec.LookPath("colima"); err == nil {
			_ = exec.Command("colima", "start").Run()
		} else {
			_ = exec.Command("open", "-a", "Docker").Run()
		}
		time.Sleep(2 * time.Second)
		return daemonStartedMsg{available: docker.DaemonAvailable()}
	}
}
