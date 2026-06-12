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
	mockOpsEdit  = key.NewBinding(key.WithKeys("e"))
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
	detailFromList  bool // true when viewingResult came from item list detail
	editingOrigMethod string // set when editing from item list, to clear old expectation
	editingOrigPath   string
	editingOrigQueryParams string // original queryStringParameters when editing
	deleteItemArmed   bool // double-press 'd' from item list to delete

	// Item list (compact view for list endpoints)
	viewingItemList bool
	itemListTitle   string
	itemList        []interface{}
	itemCursor      int

	// Param prompting
	promptingParams bool
	paramsToPrompt  []string
	paramValues     map[string]string
	currentParamIdx int
	paramInput      textinput.Model
	paramTextarea   textarea.Model

	// Query param builder (sub-prompt for key/value pairs)
	qpBuilding    bool       // in key=value builder mode
	qpPairs       [][2]string // accumulated key-value pairs
	qpPhase       int        // 0 = entering key, 1 = entering value
	qpOrigVal     string     // original query_params value to restore on cancel
	qpDeleteArmed bool       // double-press 'd' to delete last pair

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
	m.paramTextarea.SetHeight(6)
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
	if m.viewingResult {
		lineCount := strings.Count(m.viewportContent, "\n") + 1
		// +3 for title + blank lines outside the viewport
		m.resultViewport.Height = min(lineCount, max(8, h-14))
	} else {
		m.resultViewport.Height = min(h-12, 40)
	}
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

// prettyMockResult tries to pretty-print JSON with nested JSON strings
// unescaped, making MockServer API responses readable.
func prettyMockResult(data []byte) string {
	var raw interface{}
	if json.Unmarshal(data, &raw) == nil {
		raw = unescapeNestedJSON(raw)
		if pretty, err := json.MarshalIndent(raw, "", "  "); err == nil {
			return string(pretty)
		}
	}
	// Fallback: simple indent
	var pretty bytes.Buffer
	if json.Indent(&pretty, data, "", "  ") == nil {
		return pretty.String()
	}
	return string(data)
}

// unescapeNestedJSON recursively replaces string values that are themselves
// valid JSON with their parsed representation, so they render as nested
// pretty-printed objects instead of escaped strings.
func unescapeNestedJSON(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, child := range val {
			val[k] = unescapeNestedJSON(child)
		}
	case []interface{}:
		for i, child := range val {
			val[i] = unescapeNestedJSON(child)
		}
	case string:
		// Try to parse this string as JSON
		var nested interface{}
		if json.Unmarshal([]byte(val), &nested) == nil {
			return unescapeNestedJSON(nested)
		}
	}
	return v
}

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
				// Pretty-print with nested JSON unescaping
				return mockOpsDoneMsg{result: prettyMockResult(respData)}
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
		return []string{"method", "path", "query_params", "status_code", "content_type", "body"}
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

		httpRequest := map[string]interface{}{
			"method": m.paramValues["method"],
			"path":   m.paramValues["path"],
		}

		// Parse query_string parameters from key=value&key2=value2 format
		if qp := strings.TrimSpace(m.paramValues["query_params"]); qp != "" {
			if parsed, err := url.ParseQuery(qp); err == nil {
				httpRequest["queryStringParameters"] = map[string][]string(parsed)
			}
		}

		body := map[string]interface{}{
			"httpRequest":  httpRequest,
			"httpResponse": map[string]interface{}{
				"statusCode": statusCode,
				"headers": map[string][]string{
					"Content-Type": {m.paramValues["content_type"]},
				},
				"body": m.paramValues["body"],
			},
		}

		// When editing an existing expectation, clear the old one first
		// (MockServer PUT does NOT replace by matcher — it appends, causing
		// the duplicate listing bug).
		if m.editingOrigMethod != "" {
			origMethod := m.editingOrigMethod
			origPath := m.editingOrigPath
			origQueryParams := m.editingOrigQueryParams
			m.editingOrigMethod = ""
			m.editingOrigPath = ""
			m.editingOrigQueryParams = ""
			return m.editThenCreateExpectation(body, origMethod, origPath, origQueryParams)
		}

		return m.mockAPICall("/mockserver/expectation", "PUT", body, nil)()

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

// editThenCreateExpectation clears the old expectation by method+path (+ optional
// queryStringParameters) then creates the new one. MockServer's PUT does not
// replace by matcher — it appends — so we must clear first to avoid duplicate
// expectations in the listing.
func (m *MockOpsModel) editThenCreateExpectation(body map[string]interface{}, origMethod, origPath, origQueryParams string) tea.Cmd {
	return func() tea.Msg {
		port := m.mockHostPort()
		client := &http.Client{Timeout: 10 * time.Second}

		// Step 1: Clear the old expectation
		clearRequest := map[string]interface{}{
			"method": origMethod,
			"path":   origPath,
		}
		if qp := strings.TrimSpace(origQueryParams); qp != "" {
			if parsed, err := url.ParseQuery(qp); err == nil {
				clearRequest["queryStringParameters"] = map[string][]string(parsed)
			}
		}
		clearBody := map[string]interface{}{
			"httpRequest": clearRequest,
		}
		clearJSON, err := json.Marshal(clearBody)
		if err == nil {
			clearReq, reqErr := http.NewRequest("PUT", fmt.Sprintf("http://localhost:%s/mockserver/clear", port), bytes.NewReader(clearJSON))
			if reqErr == nil {
				clearReq.Header.Set("Content-Type", "application/json")
				clearResp, doErr := client.Do(clearReq)
				if doErr == nil {
					clearResp.Body.Close()
				}
			}
		}

		// Step 2: Create the new expectation
		createJSON, err := json.Marshal(body)
		if err != nil {
			return mockOpsDoneMsg{err: fmt.Errorf("invalid request: %w", err)}
		}
		createReq, err := http.NewRequest("PUT", fmt.Sprintf("http://localhost:%s/mockserver/expectation", port), bytes.NewReader(createJSON))
		if err != nil {
			return mockOpsDoneMsg{err: fmt.Errorf("request failed: %w", err)}
		}
		createReq.Header.Set("Content-Type", "application/json")
		createResp, err := client.Do(createReq)
		if err != nil {
			return mockOpsDoneMsg{err: fmt.Errorf("cannot reach MockServer at localhost:%s — is the service running?\n  %w", port, err)}
		}
		defer createResp.Body.Close()

		respData, _ := io.ReadAll(createResp.Body)
		if createResp.StatusCode >= 400 {
			return mockOpsDoneMsg{err: fmt.Errorf("MockServer error (%d): %s", createResp.StatusCode, strings.TrimSpace(string(respData)))}
		}
		if len(respData) > 0 {
			trimmed := strings.TrimSpace(string(respData))
			if trimmed == "[]" {
				return mockOpsDoneMsg{result: "No active expectations found."}
			}
			if trimmed == "{}" {
				return mockOpsDoneMsg{result: "Empty response from MockServer."}
			}
			return mockOpsDoneMsg{result: prettyMockResult(respData)}
		}
		return mockOpsDoneMsg{result: ""}
	}
}

// showItemDetail marshals the selected item to pretty-printed JSON and shows
// it in the scrollable result viewport.
func (m *MockOpsModel) showItemDetail() (tea.Model, tea.Cmd) {
	if m.itemCursor < 0 || m.itemCursor >= len(m.itemList) {
		return m, nil
	}
	item := m.itemList[m.itemCursor]
	pretty, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return m, nil
	}
	m.resultOutput = string(pretty)
	m.resultTitle = fmt.Sprintf("%s — item %d/%d", m.itemListTitle, m.itemCursor+1, len(m.itemList))
	// Only the code goes inside the viewport — title stays visible above.
	m.viewportContent = styles.StyleCode.Render(m.resultOutput)
	m.resultViewport.SetContent(m.viewportContent)
	m.resultViewport.Width = m.width - 4
	lineCount := strings.Count(m.viewportContent, "\n") + 1
	// +3 for title + blank lines outside the viewport
	m.resultViewport.Height = min(lineCount, max(8, m.height-14))
	m.resultViewport.YPosition = 0
	m.resultViewport.GotoTop()
	m.viewingResult = true
	m.detailFromList = true
	m.viewingItemList = false
	return m, nil
}

// prefillParamsFromItem extracts expectation or request fields into m.paramValues.
// For expectations it extracts both request and response fields.
// For recorded requests it prefills method/path/query with default response values.
// Returns true if the item has extractable request fields.
func (m *MockOpsModel) prefillParamsFromItem(item map[string]interface{}) bool {
	vals := make(map[string]string, 6)
	hadContent := false

	// Extract request fields (from httpRequest for expectations, top-level for requests)
	req, hasHTTPReq := item["httpRequest"].(map[string]interface{})
	if !hasHTTPReq {
		req = item // try top-level fields (recorded requests)
	}

	if v, ok := req["method"].(string); ok && v != "" {
		vals["method"] = v
		hadContent = true
	}
	if v, ok := req["path"].(string); ok && v != "" {
		vals["path"] = v
		hadContent = true
	}
	if qsp, ok := req["queryStringParameters"].(map[string]interface{}); ok {
		qsv := make(url.Values)
		for k, v := range qsp {
			switch val := v.(type) {
			case []interface{}:
				for _, sv := range val {
					qsv.Add(k, fmt.Sprintf("%v", sv))
				}
			case string:
				qsv.Add(k, val)
			}
		}
		if enc := qsv.Encode(); enc != "" {
			vals["query_params"] = enc
		}
	}

	// Extract response fields (only for expectations, not recorded requests)
	if res, ok := item["httpResponse"].(map[string]interface{}); ok {
		if v, ok := res["statusCode"].(float64); ok {
			vals["status_code"] = fmt.Sprintf("%d", int(v))
		}
		if headers, ok := res["headers"].(map[string]interface{}); ok {
			if ctList, ok := headers["Content-Type"].([]interface{}); ok && len(ctList) > 0 {
				vals["content_type"] = fmt.Sprintf("%v", ctList[0])
			}
		}
		if body, ok := res["body"]; ok && body != nil {
			switch b := body.(type) {
			case string:
				vals["body"] = b
			default:
				if bjson, err := json.MarshalIndent(b, "", "  "); err == nil {
					vals["body"] = string(bjson)
				}
			}
		}
	}

	if !hadContent {
		return false
	}
	m.paramValues = vals
	return true
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
			m.viewingItemList = false
			m.resultOutput = ""
			return m, spinnerCmd()
		}

		// List endpoints: parse as array and show compact item list.
		// These are read-only — clear doneOp so no spurious checkmark appears.
		if completedLabel == "List active expectations" || completedLabel == "Retrieve requests" {
			m.doneOp = ""
			var items []interface{}
			if json.Unmarshal([]byte(msg.result), &items) == nil && len(items) > 0 {
				m.itemList = items
				m.itemCursor = 0
				m.itemListTitle = completedLabel
				m.viewingItemList = true
				m.viewingResult = false
				return m, spinnerCmd()
			}
			// Empty or non-array: fall through to viewport
		}

		// Show result in viewport — title stays visible outside
		m.resultOutput = msg.result
		m.resultTitle = completedLabel
		m.viewportContent = styles.StyleCode.Render(m.resultOutput)
		m.resultViewport.SetContent(m.viewportContent)
		m.resultViewport.Width = m.width - 4
		lineCount := strings.Count(m.viewportContent, "\n") + 1
		// +3 for title + blank lines outside the viewport
		m.resultViewport.Height = min(lineCount, max(8, m.height-13))
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
		if m.viewingItemList {
			switch {
			case key.Matches(msg, mockOpsUp):
				m.deleteItemArmed = false
				if m.itemCursor > 0 {
					m.itemCursor--
				}
			case key.Matches(msg, mockOpsDown):
				m.deleteItemArmed = false
				if m.itemCursor < len(m.itemList)-1 {
					m.itemCursor++
				}
			case key.Matches(msg, mockOpsEnter):
				m.deleteItemArmed = false
				// Show selected item detail in viewport
				return m.showItemDetail()
			case key.Matches(msg, mockOpsEdit):
				m.deleteItemArmed = false
				// Edit the selected item — pre-fill param form from its JSON
				if m.itemCursor >= 0 && m.itemCursor < len(m.itemList) {
					if item, ok := m.itemList[m.itemCursor].(map[string]interface{}); ok && m.prefillParamsFromItem(item) {
						m.editingOrigMethod = m.paramValues["method"]
						m.editingOrigPath = m.paramValues["path"]
						m.editingOrigQueryParams = m.paramValues["query_params"]
						// Keep itemList alive and mark detailFromList so the
						// viewport close handler returns to the item list.
						m.viewingItemList = false
						m.detailFromList = true
						for i, act := range m.actions {
							if act.label == "Create expectation" {
								m.actionCursor = i
								break
							}
						}
						m.promptingParams = true
						m.paramsToPrompt = []string{"method", "path", "query_params", "status_code", "content_type", "body"}
						m.currentParamIdx = 0
						m.refreshParamInput()
						return m, nil
					}
					// Silently skip non-expectation items (requests) — help text
					// hides edit/delete for requests via isExpectationItem.
				}
			case msg.String() == "d":
				if m.deleteItemArmed {
					// Second 'd' — confirm delete via /mockserver/clear
					m.deleteItemArmed = false
					if item, ok := m.itemList[m.itemCursor].(map[string]interface{}); ok {
						// Support both expectations (httpRequest) and requests (top-level)
						req, hasHTTPReq := item["httpRequest"].(map[string]interface{})
						if !hasHTTPReq {
							req = item
						}
						method, _ := req["method"].(string)
						path, _ := req["path"].(string)
						if method != "" && path != "" {
							clearBody := map[string]interface{}{
								"httpRequest": map[string]interface{}{
									"method": method,
									"path":   path,
								},
							}
							m.viewingItemList = false
							m.itemList = nil
							m.doneOp = "Delete expectation"
							m.doneOpAt = time.Now()
							m.pendingOps["Delete expectation"] = "running"
							// The doneMsg handler shows the result in the viewport
							// since "Delete expectation" doesn't match list/report labels.
							return m, m.mockAPICall("/mockserver/clear", "PUT", clearBody, nil)()
						}
					}
				} else {
					m.deleteItemArmed = true
					return m, nil
				}
			case key.Matches(msg, mockOpsBack):
				m.deleteItemArmed = false
				m.viewingItemList = false
				m.itemList = nil
				// Return focus to the action that produced this list
				for i, act := range m.actions {
					if act.label == m.itemListTitle {
						m.actionCursor = i
						break
					}
				}
			default:
				m.deleteItemArmed = false
			}
			return m, nil
		}
		if m.viewingResult {
			switch {
			case key.Matches(msg, mockOpsUp):
				m.resultViewport.LineUp(1)
			case key.Matches(msg, mockOpsDown):
				m.resultViewport.LineDown(1)
			default:
				m.viewingResult = false
				// If we came from the item list, go back to it
				if m.detailFromList {
					m.detailFromList = false
					m.viewingItemList = true
				}
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
// setBodyTextareaHeight auto-sizes the body textarea to fit content.
// The cap is dynamic so the total param-prompt view (builder prefix + param labels
// + textarea border + help) stays within m.height. Without this the body overflows
// the terminal and the header (rendered by App.View) scrolls off the top.
// Content beyond the cap is scrollable inside the textarea.
func (m *MockOpsModel) setBodyTextareaHeight() {
	lines := len(strings.Split(m.paramTextarea.Value(), "\n"))
	// Overhead outside the textarea + border when body is focused:
	//   builder prefix (7) + viewParamPrompt non-body (6) + "> body" label (2)
	//   + border (2) + trailing newlines (2) + help (1) = 20 lines
	// Available = m.height − overhead → cap ensures total fits ≤ m.height.
	maxVisible := m.height - 20
	if maxVisible < 5 {
		maxVisible = 5
	}
	switch {
	case lines < 6:
		// Never go below the min, but respect the cap if smaller
		m.paramTextarea.SetHeight(min(6, maxVisible))
	case lines > maxVisible:
		m.paramTextarea.SetHeight(maxVisible)
	default:
		m.paramTextarea.SetHeight(lines)
	}
}

func (m *MockOpsModel) refreshParamInput() {
	param := m.paramsToPrompt[m.currentParamIdx]
	if param == "body" {
		m.paramInput.Blur()
		// Start in preview mode — blurred; Enter to edit, esc to navigate params
		m.paramTextarea.Blur()
		if v, ok := m.paramValues[param]; ok && v != "" {
			m.paramTextarea.SetValue(v)
		} else {
			m.paramTextarea.SetValue("")
		}
		m.setBodyTextareaHeight()
		m.paramTextarea.CursorEnd()
	} else if param == "query_params" {
		// query_params uses the builder — blur inputs but show value in the label
		m.paramInput.Blur()
		m.paramInput.SetValue("")
		m.paramTextarea.Blur()
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
	// query_params is managed by the builder — never overwrite from input
	if param == "query_params" {
		return
	}
	val := m.currentParamValue()
	if param != "body" {
		val = strings.TrimSpace(val)
	}
	m.paramValues[param] = val
}

func (m *MockOpsModel) allParamsFilled() bool {
	for _, p := range m.paramsToPrompt {
		if p == "query_params" {
			continue // optional
		}
		if v, ok := m.paramValues[p]; !ok || v == "" {
			return false
		}
	}
	return true
}

// ── Query param builder ──────────────────────────────────────────────────────

// enterQPBuilder initialises the sub-prompt for adding key=value pairs.
func (m *MockOpsModel) enterQPBuilder() {
	m.qpOrigVal = m.paramValues["query_params"]
	m.qpPairs = nil

	// Parse existing query_params into pairs for editing
	if m.qpOrigVal != "" {
		if parsed, err := url.ParseQuery(m.qpOrigVal); err == nil {
			for k, vs := range parsed {
				for _, v := range vs {
					m.qpPairs = append(m.qpPairs, [2]string{k, v})
				}
			}
		}
	}

	m.qpBuilding = true
	m.qpPhase = 0 // start with key
	m.paramInput.SetValue("")
	m.paramInput.Focus()
	m.paramInput.Placeholder = "key name (leave empty to finish)"
	m.paramInput.CursorEnd()
}

// finishQPBuilder encodes the accumulated pairs into a query string and exits
// builder mode. If pairs is empty, query_params is cleared.
func (m *MockOpsModel) finishQPBuilder() {
	if len(m.qpPairs) == 0 {
		m.paramValues["query_params"] = ""
	} else {
		qsv := make(url.Values)
		for _, p := range m.qpPairs {
			qsv.Add(p[0], p[1])
		}
		m.paramValues["query_params"] = qsv.Encode()
	}
	m.qpBuilding = false
	m.qpPairs = nil
	m.paramInput.SetValue("")
	m.paramInput.Placeholder = "value"
}

// submitForm executes the current action with all param values already saved.
func (m *MockOpsModel) submitForm() (tea.Model, tea.Cmd) {
	m.promptingParams = false
	act := m.actions[m.actionCursor]
	isEdit := m.editingOrigMethod != "" // capture before execParamAction clears it
	execCmd := m.execParamAction(act)
	if execCmd == nil {
		return m, nil
	}
	m.lastResult = ""
	if isEdit {
		m.doneOp = "Edit expectation"
		m.doneOpAt = time.Now()
		m.pendingOps["Edit expectation"] = "running"
	} else {
		m.doneOp = act.label
		m.doneOpAt = time.Now()
		m.pendingOps[act.label] = "running"
	}
	return m, tea.Batch(execCmd, spinnerCmd())
}

// formatBody pretty-prints the body value according to content_type.
func (m *MockOpsModel) formatBody() {
	ct := m.paramValues["content_type"]
	val := m.paramTextarea.Value()

	switch {
	case strings.Contains(ct, "json"):
		var pretty bytes.Buffer
		if json.Indent(&pretty, []byte(val), "", "  ") == nil {
			m.paramTextarea.SetValue(pretty.String())
			m.setBodyTextareaHeight()
		}
	default:
		// For other content types, just trim leading/trailing whitespace
		m.paramTextarea.SetValue(strings.TrimSpace(val))
	}
}

func (m *MockOpsModel) updateParamPrompt(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		isBody := m.paramsToPrompt[m.currentParamIdx] == "body"
		isQP := m.paramsToPrompt[m.currentParamIdx] == "query_params"

		// Query param builder mode — handle key/value entry
		if m.qpBuilding {
			switch msg.String() {
			case "esc":
				// Cancel builder, restore original value
				if m.qpOrigVal != "" {
					m.paramValues["query_params"] = m.qpOrigVal
				} else {
					delete(m.paramValues, "query_params")
				}
				m.qpBuilding = false
				m.qpPairs = nil
				m.paramInput.SetValue("")
				m.paramInput.Placeholder = "value"
				m.refreshParamInput()
				return m, nil

			case "enter":
				val := strings.TrimSpace(m.paramInput.Value())
				m.paramInput.SetValue("")

				if m.qpPhase == 0 {
					// Entering key — empty key means finish
					if val == "" {
						m.finishQPBuilder()
						if m.allParamsFilled() {
							return m.submitForm()
						}
						m.refreshParamInput()
						return m, nil
					}
					// Save key, move to value phase
					m.qpPairs = append(m.qpPairs, [2]string{val, ""})
					m.qpPhase = 1
					m.paramInput.Placeholder = "value for " + val
					m.paramInput.CursorEnd()
				} else {
					// Entering value — save to current pair
					if len(m.qpPairs) > 0 {
						m.qpPairs[len(m.qpPairs)-1][1] = val
					}
					m.qpPhase = 0
					m.paramInput.Placeholder = "key name (leave empty to finish)"
					m.paramInput.CursorEnd()
				}
				m.qpDeleteArmed = false
				return m, nil

			case "ctrl+d":
				if len(m.qpPairs) == 0 || m.qpPhase != 0 {
					m.qpDeleteArmed = false
					break
				}
				if m.qpDeleteArmed {
					// Second Ctrl+D — remove last pair
					m.qpPairs = m.qpPairs[:len(m.qpPairs)-1]
					m.qpDeleteArmed = false
				} else {
					m.qpDeleteArmed = true
				}
				return m, nil

			default:
				m.qpDeleteArmed = false
				// Delegate text input to paramInput
				if m.paramInput.Focused() {
					var cmd tea.Cmd
					m.paramInput, cmd = m.paramInput.Update(msg)
					return m, cmd
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "esc":
			if isBody && m.paramTextarea.Focused() {
				// First esc on body → exit edit mode, back to param navigation
				m.paramTextarea.Blur()
				return m, nil
			}
			m.promptingParams = false
			m.editingOrigMethod = ""
			m.editingOrigPath = ""
			m.editingOrigQueryParams = ""
			// Return to item list when cancelling an edit started from there
			if m.detailFromList {
				m.detailFromList = false
				m.viewingItemList = true
			}
			return m, nil

		case "up":
			if isBody && m.paramTextarea.Focused() {
				if m.paramTextarea.Line() > 0 {
					break // textarea handles cursor up within text
				}
			}
			if m.currentParamIdx > 0 {
				m.setCurrentParamValue()
				m.currentParamIdx--
				m.refreshParamInput()
			}
			return m, nil

		case "down":
			if isBody && m.paramTextarea.Focused() {
				totalLines := len(strings.Split(m.paramTextarea.Value(), "\n"))
				if m.paramTextarea.Line() < totalLines-1 {
					break // textarea handles cursor down within text
				}
			}
			if m.currentParamIdx < len(m.paramsToPrompt)-1 {
				m.setCurrentParamValue()
				m.currentParamIdx++
				m.refreshParamInput()
			}
			return m, nil

		case "enter":
			if isBody {
				if !m.paramTextarea.Focused() {
					// Enter on preview → enter edit mode
					m.paramTextarea.Focus()
					return m, nil
				}
				break // Enter in edit mode → newline
			}
			if isQP {
				// Enter query param builder mode
				m.enterQPBuilder()
				return m, nil
			}
			// Non-body: save current field and try to submit
			m.setCurrentParamValue()
			if m.allParamsFilled() {
				return m.submitForm()
			}
			return m, nil

		case "ctrl+f":
			if isBody {
				if !m.paramTextarea.Focused() {
					break // ignore when not in edit mode
				}
				m.formatBody()
			}
			return m, nil

		case "ctrl+s":
			if isBody {
				if !m.paramTextarea.Focused() {
					break // ignore when not in edit mode
				}
				m.setCurrentParamValue()
				m.formatBody()
				m.paramValues["body"] = m.paramTextarea.Value()
				if m.allParamsFilled() {
					return m.submitForm()
				}
			}
			return m, nil
		}

		// Delegate to focused input
		if isBody {
			if m.paramTextarea.Focused() {
				var cmd tea.Cmd
				m.paramTextarea, cmd = m.paramTextarea.Update(msg)
				m.setBodyTextareaHeight()
				return m, cmd
			}
			// Preview mode: ignore non-navigation keys
			return m, nil
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
	isEdit := m.editingOrigMethod != "" // capture before execParamAction clears it
	execCmd := m.execParamAction(act)
	if execCmd == nil {
		m.editingOrigMethod = ""
		m.editingOrigPath = ""
		return m, nil
	}
	m.lastResult = ""
	if isEdit {
		// Use a distinct label so the result viewport says "Edit expectation"
		// and no spurious checkmark appears on "Create expectation" in the
		// action list.
		m.doneOp = "Edit expectation"
		m.doneOpAt = time.Now()
		m.pendingOps["Edit expectation"] = "running"
	} else {
		m.doneOp = act.label
		m.doneOpAt = time.Now()
		m.pendingOps[act.label] = "running"
	}
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
		titleLine := styles.StyleHeader.Render("  " + m.resultTitle + "  —  result")
		return titleLine + "\n\n" + m.resultViewport.View() + scrollHint + "\n  " + m.viewportHelp()
	}

	if m.viewingItemList {
		return m.viewItemList()
	}

	// ── Builder-based views (action list, param prompt, confirm) ──
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
		return m.padView(b.String())
	}

	// Confirm destructive
	if m.confirmAction {
		promptStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		b.WriteString(styles.StyleError.Render("  ✗ This action clears ALL active expectations.") + "\n")
		b.WriteString("  " + promptStyle.Render("▸ Continue?") + "  " + styles.StyleSubtitle.Render("(y/n/esc)") + "\n\n")
		return m.padView(b.String())
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

	b.WriteString("\n")
	b.WriteString("  " + styles.InlineHelp(
		styles.HelpItem("↑ ↓", "navigate"),
		styles.HelpItem("↵", "execute"),
		styles.HelpItem("esc", "back"),
	))
	return m.padView(b.String())
}

// padView fills to one line less than the available height, leaving
// breathing room so the terminal does not scroll the header off-screen
// when the frame exactly matches the terminal height.
func (m *MockOpsModel) padView(view string) string {
	target := m.height - 1 // leave 1 line of breathing room
	if target > 0 {
		lines := strings.Count(view, "\n") + 1
		if lines < target {
			view += strings.Repeat("\n", target-lines)
		}
	}
	return view
}

// viewItemList renders a compact navigable list of items (expectations or requests).
func (m *MockOpsModel) viewItemList() string {
	var b strings.Builder
	b.WriteString(styles.StyleTitle.Render(fmt.Sprintf(" %s — %d items ", m.itemListTitle, len(m.itemList))))
	b.WriteString("\n\n")

	selStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)
	itemStyle := lipgloss.NewStyle().Foreground(styles.ColorMuted)

	for i, item := range m.itemList {
		label := mockItemLabel(item)
		if i == m.itemCursor {
			b.WriteString(selStyle.Render("    > " + label) + "\n")
		} else {
			b.WriteString(itemStyle.Render("      " + label) + "\n")
		}
	}

	b.WriteString("\n")

	if m.deleteItemArmed {
		b.WriteString("  " + styles.StyleError.Render("Press d again to delete this expectation") + "\n\n")
	}

	// Only show edit/delete actions when the current item is an expectation
	helpItems := []styles.HelpItemResult{
		styles.HelpItem("↑ ↓", "navigate"),
		styles.HelpItem("↵", "view"),
	}
	if cur := m.currentItem(); cur != nil && isExpectationItem(cur) {
		helpItems = append(helpItems,
			styles.HelpItem("e", "edit"),
			styles.HelpItem("d", "delete"),
		)
	}
	helpItems = append(helpItems, styles.HelpItem("esc", "back"))

	b.WriteString("  " + styles.InlineHelp(helpItems...))
	return b.String()
}

// currentItem returns the item at the current cursor position, or nil.
func (m *MockOpsModel) currentItem() interface{} {
	if m.itemCursor >= 0 && m.itemCursor < len(m.itemList) {
		return m.itemList[m.itemCursor]
	}
	return nil
}

// isExpectationItem returns true if the item has an httpRequest field.
func isExpectationItem(item interface{}) bool {
	if obj, ok := item.(map[string]interface{}); ok {
		_, ok = obj["httpRequest"].(map[string]interface{})
		return ok
	}
	return false
}

// mockItemLabel extracts a readable "METHOD /path" label from a parsed
// MockServer expectation or request item.
func mockItemLabel(item interface{}) string {
	obj, ok := item.(map[string]interface{})
	if !ok {
		return fmt.Sprintf("%v", item)
	}

	// Try top-level method/path (requests)
	if method, ok := obj["method"].(string); ok {
		if path, ok := obj["path"].(string); ok {
			return method + " " + path
		}
	}

	// Try httpRequest.method/httpRequest.path (expectations)
	if req, ok := obj["httpRequest"].(map[string]interface{}); ok {
		method, _ := req["method"].(string)
		path, _ := req["path"].(string)
		if method != "" || path != "" {
			return method + " " + path
		}
	}

	return fmt.Sprintf("%v", item)
}

// viewportHelp returns the help bar shown below the result viewport.
func (m *MockOpsModel) viewportHelp() string {
	return styles.StyleHelp.Render("press any key to close")
}

// ── Param prompt view ──────────────────────────────────────────────────────────

func (m *MockOpsModel) viewParamPrompt() string {
	var b strings.Builder

	// Query param builder sub-view
	if m.qpBuilding {
		b.WriteString(styles.StyleSubtitle.Render("  Query parameters") + "\n\n")

		pairStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent)
		if len(m.qpPairs) > 0 {
			b.WriteString("  " + styles.StyleHelp.Render("Current pairs:") + "\n")
			for _, p := range m.qpPairs {
				b.WriteString("    " + pairStyle.Render(p[0]) + "  →  " + p[1] + "\n")
			}
			b.WriteString("\n")
		}

		promptStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		if m.qpPhase == 0 {
			b.WriteString("  " + promptStyle.Render("▸ Key") + "  (leave empty to finish)\n\n")
		} else {
			key := m.qpPairs[len(m.qpPairs)-1][0]
			b.WriteString("  " + promptStyle.Render("▸ Value for "+key) + "\n\n")
		}
		b.WriteString("  " + m.paramInput.View() + "\n")

		if m.qpDeleteArmed {
			b.WriteString("\n  " + styles.StyleError.Render("Press Ctrl+D again to remove last pair") + "\n")
		}

		helpItems := []styles.HelpItemResult{
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("enter", "confirm"),
		}
		if len(m.qpPairs) > 0 && m.qpPhase == 0 {
			helpItems = append(helpItems, styles.HelpItem("^d ^d", "delete last"))
		}
		b.WriteString("\n  " + styles.InlineHelp(helpItems...))
		return b.String()
	}

	b.WriteString(styles.StyleSubtitle.Render("  Enter parameter values") + "\n\n")

	cursorStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	filledStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	emptyStyle := lipgloss.NewStyle().Foreground(styles.ColorMuted)

	// Fixed width for param lines ensures old content is fully overwritten
	// when switching between cursor/non-cursor views of the same line.
	linePad := lipgloss.NewStyle().Width(60)

	onBody := m.paramsToPrompt[m.currentParamIdx] == "body"
	isQP := m.paramsToPrompt[m.currentParamIdx] == "query_params"

	for i, param := range m.paramsToPrompt {
		if i == m.currentParamIdx && onBody && param == "body" {
			continue
		}
		if i == m.currentParamIdx {
			if isQP {
				// Show the encoded value or a prompt to enter builder
				display := m.paramValues["query_params"]
				if display != "" {
					line := "  " + cursorStyle.Render("> "+param+"  ") + display
					b.WriteString(linePad.Render(line) + "\n")
				} else {
					line := "  " + cursorStyle.Render("> "+param+"  ") + emptyStyle.Render("press enter to add key=value pairs")
					b.WriteString(linePad.Render(line) + "\n")
				}
			} else {
				line := "  " + cursorStyle.Render("> "+param+"  ") + m.paramInput.View()
				b.WriteString(linePad.Render(line) + "\n")
			}
		} else if v, ok := m.paramValues[param]; ok && v != "" {
			display := v
			// Truncate body inline so a large JSON doesn't wrap and break layout
			if param == "body" && len(display) > 60 {
				display = display[:57] + "..."
			}
			line := "    " + param + "  " + filledStyle.Render(display)
			b.WriteString(linePad.Render(line) + "\n")
		} else {
			line := "    " + param + "  " + emptyStyle.Render("-")
			b.WriteString(linePad.Render(line) + "\n")
		}
	}

	if onBody {
		b.WriteString("  " + cursorStyle.Render("> body  ") + "\n\n")
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
			styles.HelpItem("↑ ↓", "navigate"),
			styles.HelpItem("enter", "newline"),
			styles.HelpItem("ctrl+f", "format"),
			styles.HelpItem("ctrl+s", "save"),
		))
	} else if isQP {
		b.WriteString("  " + styles.InlineHelp(
			styles.HelpItem("esc", "cancel"),
			styles.HelpItem("↑ ↓", "navigate"),
			styles.HelpItem("enter", "add pairs"),
		))
	} else {
		// Dynamic label: "save" if just storing the field, "submit" if it triggers the action
		willSubmit := false
			if m.currentParamIdx == len(m.paramsToPrompt)-1 {
				othersFilled := true
				for i, p := range m.paramsToPrompt {
					if i == m.currentParamIdx || p == "query_params" {
						continue
					}
					if v, ok := m.paramValues[p]; !ok || v == "" {
						othersFilled = false
						break
					}
				}
				willSubmit = othersFilled
			}
		enterLabel := "save"
		if willSubmit {
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
