package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/hub"
	"github.com/stackr/stackr/internal/tui/styles"
)

type addSvcStep int

const (
	svcStepSource addSvcStep = iota // catalog / search / manual
	svcStepCatalog
	svcStepSearch
	svcStepSelectTag
	svcStepName
	svcStepPorts
	svcStepEnvVars
	svcStepVolumes
	svcStepReview
	svcStepRunCommand
)

type AddServiceModel struct {
	cfg     *config.Config
	profile *config.Profile
	envName string
	step    addSvcStep

	// source selection
	sourceCursor int
	sourcePath   string // "catalog" | "search" | "manual"

	// catalog
	catalogCursor int

	// hub search
	searchInput     textinput.Model
	searchResults   []hub.SearchResult
	searchCursor    int
	searching       bool
	searchErr       string
	lastSearchQuery string // track last executed query; Enter re-searches if query changed

	// tag selection
	tags        []hub.Tag
	tagCursor   int
	loadingTags bool

	// service being built
	svc        config.Service
	imageInput textinput.Model // for manual entry — enter image name first
	nameInput  textinput.Model
	nameFocus  int // 0=image input, 1=service name input (manual mode only)

	// ports editor
	portHost         textinput.Model
	portContainer    textinput.Model
	ports            []config.PortMapping
	portsDeleteArmed bool

	// env vars editor
	envKey             textinput.Model
	envVal             textinput.Model
	envFocus           int // 0=key, 1=val
	envVars            []config.EnvVar
	envVarsDeleteArmed bool
	resetArmed         bool
	protectedEnvKeys   map[string]bool      // env var keys from catalog defaults
	defaultPorts       []config.PortMapping // snapshot for reset
	defaultEnvVars     []config.EnvVar      // snapshot for reset

	// volumes editor
	volSource          textinput.Model
	volNamed           bool
	volumes            []config.VolumeMapping
	volumesDeleteArmed bool
	defaultVolumes     []config.VolumeMapping // snapshot for reset on re-entry

	// run command
	runCmdName    textinput.Model
	runCmdCmd     textinput.Model
	runCmdWorkDir textinput.Model
	runCmdFocus   int // 0=name, 1=command, 2=workdir

	err    string
	height int // terminal height, set via WindowSizeMsg
}

// addSvcItemWindow calculates the visible range for single-line item lists
// (catalog, search results, tag list) given the terminal height and overhead.
func addSvcItemWindow(total, cursor, height, overhead int) itemWindow {
	w := itemWindow{start: 0, end: total}
	if total == 0 || height <= 0 {
		return w
	}
	avail := height - overhead
	if avail <= 0 {
		return itemWindow{
			start:    cursor,
			end:      min(cursor+1, total),
			hasAbove: cursor > 0,
			hasBelow: cursor+1 < total,
		}
	}
	if total <= avail {
		return w
	}
	half := avail / 2
	start := cursor - half
	if start < 0 {
		start = 0
	}
	end := start + avail
	if end > total {
		end = total
		start = total - avail
	}
	return itemWindow{
		start: start, end: end,
		hasAbove: start > 0,
		hasBelow: end < total,
	}
}

func addSvcTailWindow(total, height, overhead int) itemWindow {
	w := itemWindow{start: 0, end: total}
	if total == 0 || height <= 0 {
		return w
	}
	avail := height - overhead
	if avail <= 0 {
		return itemWindow{start: total - 1, end: total, hasAbove: total > 1}
	}
	if total <= avail {
		return w
	}
	start := total - avail
	if start < 0 {
		start = 0
	}
	return itemWindow{start: start, end: total, hasAbove: start > 0}
}

func NewAddService(cfg *config.Config, p *config.Profile, envName string) *AddServiceModel {
	si := textinput.New()
	si.Placeholder = "postgres, redis, my-image..."
	si.CharLimit = 64

	ni := textinput.New()
	ni.Placeholder = "postgres"
	ni.CharLimit = 64

	ii := textinput.New()
	ii.Placeholder = "mongo:4.2, postgres:16, my-registry/image..."
	ii.CharLimit = 128

	ph := textinput.New()
	ph.Placeholder = "5432"
	ph.CharLimit = 10
	ph.Width = 8
	pc := textinput.New()
	pc.Placeholder = "5432"
	pc.CharLimit = 10
	pc.Width = 8

	ek := textinput.New()
	ek.Placeholder = "POSTGRES_USER"
	ek.CharLimit = 64
	ek.Width = 24
	ev := textinput.New()
	ev.Placeholder = "postgres"
	ev.CharLimit = 512
	ev.Width = 28

	vs := textinput.New()
	vs.Placeholder = "./data or volume name"
	vs.CharLimit = 256
	vs.Width = 30

	rn := textinput.New()
	rn.Placeholder = "my-dev-server"
	rn.CharLimit = 64
	rn.Width = 30

	rc := textinput.New()
	rc.Placeholder = "pnpm dev"
	rc.CharLimit = 256
	rc.Width = 40

	rw := textinput.New()
	rw.Placeholder = "~/my-project"
	rw.CharLimit = 256
	rw.Width = 40

	return &AddServiceModel{
		cfg:              cfg,
		profile:          p,
		envName:          envName,
		step:             svcStepSource,
		searchInput:      si,
		imageInput:       ii,
		nameInput:        ni,
		portHost:         ph,
		portContainer:    pc,
		envKey:           ek,
		envVal:           ev,
		volSource:        vs,
		runCmdName:       rn,
		runCmdCmd:        rc,
		runCmdWorkDir:    rw,
		protectedEnvKeys: make(map[string]bool),
	}
}

func (m *AddServiceModel) ProfileName() string { return m.profile.Name }

// refocusInputs blurs all text inputs and focuses the ones relevant
// to the given step. Call this when navigating BACK to a step so
// keyboard input goes to the right field.
func (m *AddServiceModel) resetServiceData() {
	m.err = ""
	m.ports = nil
	m.envVars = nil
	m.volumes = nil
	m.defaultPorts = nil
	m.defaultEnvVars = nil
	m.defaultVolumes = nil
	m.protectedEnvKeys = make(map[string]bool)
	m.svc = config.Service{}
	m.searchInput.SetValue("")
	m.searchResults = nil
	m.searchCursor = 0
	m.searching = false
	m.searchErr = ""
	m.lastSearchQuery = ""
	m.imageInput.SetValue("")
	m.nameInput.SetValue("")
	m.nameFocus = 0
	m.volNamed = false
	m.tags = nil
	m.tagCursor = 0
	m.loadingTags = false
	m.catalogCursor = 0
	m.runCmdName.SetValue("")
	m.runCmdCmd.SetValue("")
	m.runCmdWorkDir.SetValue("")
	m.runCmdFocus = 0
}

func (m *AddServiceModel) refocusInputs(step addSvcStep) tea.Cmd {
	m.searchInput.Blur()
	m.imageInput.Blur()
	m.nameInput.Blur()
	m.portHost.Blur()
	m.portContainer.Blur()
	m.envKey.Blur()
	m.envVal.Blur()
	m.volSource.Blur()
	m.runCmdName.Blur()
	m.runCmdCmd.Blur()
	m.runCmdWorkDir.Blur()

	switch step {
	case svcStepSearch:
		m.searchInput.Focus()
		return textinput.Blink
	case svcStepName:
		if m.sourcePath == "manual" {
			m.nameFocus = 0
			m.imageInput.Focus()
		} else {
			m.nameInput.Focus()
		}
		return textinput.Blink
	case svcStepPorts:
		m.portHost.Focus()
		return textinput.Blink
	case svcStepEnvVars:
		m.envKey.Focus()
		m.envFocus = 0
		return textinput.Blink
	case svcStepVolumes:
		m.volSource.Focus()
		return textinput.Blink
	case svcStepRunCommand:
		m.runCmdName.Blur()
		m.runCmdCmd.Blur()
		m.runCmdWorkDir.Blur()
		switch m.runCmdFocus {
		case 0:
			m.runCmdName.Focus()
		case 1:
			m.runCmdCmd.Focus()
		case 2:
			m.runCmdWorkDir.Focus()
		}
		return textinput.Blink
	}
	return nil
}

// ── Messages ─────────────────────────────────────────────────────────────────

type hubSearchResultMsg struct {
	results []hub.SearchResult
	err     string
}

type hubTagsResultMsg struct {
	tags []hub.Tag
	err  string
}

type svcSavedMsg struct{}

// ── Init ─────────────────────────────────────────────────────────────────────

func (m *AddServiceModel) Init() tea.Cmd { return textinput.Blink }

// ── Update ───────────────────────────────────────────────────────────────────

var addSvcBack = key.NewBinding(key.WithKeys("esc"))
var addSvcNext = key.NewBinding(key.WithKeys("enter"))
var addSvcUp = key.NewBinding(key.WithKeys("up"))
var addSvcDown = key.NewBinding(key.WithKeys("down"))
var addSvcUpField = key.NewBinding(key.WithKeys("up"))
var addSvcDownField = key.NewBinding(key.WithKeys("down"))
var addSvcToggle = key.NewBinding(key.WithKeys("ctrl+t"))
var addSvcDelete = key.NewBinding(key.WithKeys("ctrl+d"))
var addSvcReset = key.NewBinding(key.WithKeys("ctrl+r"))
var addSvcTab = key.NewBinding(key.WithKeys("tab"))

// SetSize propagates terminal dimensions before the first WindowSizeMsg.
// h is the full terminal height; we subtract 5 for App chrome.
func (m *AddServiceModel) SetSize(w, h int) {
	m.height = max(0, h-5)
}

func (m *AddServiceModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = max(0, msg.Height-5)
		return m, nil

	case ClearActionMsg:
		m.err = ""
		return m, nil

	case hubSearchResultMsg:
		m.searching = false
		m.searchErr = msg.err
		m.searchResults = msg.results
		return m, nil

	case hubTagsResultMsg:
		m.loadingTags = false
		m.tags = msg.tags
		if msg.err != "" {
			m.searchErr = msg.err
		}
		return m, nil

	case svcSavedMsg:
		return m, func() tea.Msg {
			return NavigateTo{Screen: NavProfileView, ProfileName: m.profile.Name, EnvName: m.envName, ServiceName: m.svc.Name}
		}

	case tea.KeyMsg:
	if key.Matches(msg, addSvcBack) {
		m.portsDeleteArmed = false
		m.envVarsDeleteArmed = false
		m.volumesDeleteArmed = false
		m.resetArmed = false
		if m.step == svcStepSource {
				return m, func() tea.Msg {
					return NavigateTo{Screen: NavProfileView, ProfileName: m.profile.Name, EnvName: m.envName}
				}
			}

			// Branch steps always go back to source
			switch m.step {
			case svcStepSearch, svcStepCatalog:
				m.resetServiceData()
				m.step = svcStepSource
				m.sourcePath = ""
				return m, nil
		case svcStepSelectTag:
			m.resetServiceData()
			m.step = svcStepSource
			m.sourcePath = ""
			return m, nil
		case svcStepRunCommand:
			m.resetServiceData()
			m.step = svcStepSource
			m.sourcePath = ""
			return m, nil
		case svcStepName:
				if m.sourcePath == "manual" {
					m.resetServiceData()
					m.step = svcStepSource
					m.sourcePath = ""
					return m, nil
				}
				m.err = ""
				m.step--
				return m, m.refocusInputs(m.step)
			default:
				m.err = ""
				m.step--
				return m, m.refocusInputs(m.step)
			}
		}
		return m.handleStep(msg)
	}
	return m.updateActiveInput(msg)
}

func (m *AddServiceModel) handleStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.step {
	case svcStepSource:
		return m.handleSource(msg)
	case svcStepCatalog:
		return m.handleCatalog(msg)
	case svcStepSearch:
		return m.handleSearch(msg)
	case svcStepSelectTag:
		return m.handleTagSelect(msg)
	case svcStepName:
		if m.sourcePath == "manual" {
			switch {
			case key.Matches(msg, addSvcUpField), key.Matches(msg, addSvcDownField), key.Matches(msg, addSvcTab):
				if m.nameFocus == 0 {
					m.nameFocus = 1
					m.imageInput.Blur()
					m.nameInput.Focus()
					m.err = ""
				} else {
					m.nameFocus = 0
					m.nameInput.Blur()
					m.imageInput.Focus()
					m.err = ""
				}
				return m, textinput.Blink
			case key.Matches(msg, addSvcNext):
				if m.nameFocus == 0 {
					// Enter on image input: set image, prefill, move to name
					img := strings.TrimSpace(m.imageInput.Value())
					if img == "" {
						return m, nil
					}
					m.svc.Image = img
					m.prefillFromCatalog(img)
					// Auto-fill service name from image (strip tag, sanitize for compose)
					name := img
					if idx := strings.IndexByte(name, ':'); idx >= 0 {
						name = name[:idx]
					}
					name = config.SanitizeComposeName(name)
					m.nameInput.SetValue(name)
					m.nameFocus = 1
					m.imageInput.Blur()
					m.nameInput.Focus()
					m.err = ""
					return m, textinput.Blink
				}
				// Enter on service name: advance to ports
				m.svc.Name = config.SanitizeComposeName(strings.TrimSpace(m.nameInput.Value()))
				if m.svc.Name == "" {
					return m, nil
				}
				for _, env := range m.profile.Environments {
					if env.Name == m.envName {
						for _, svc := range env.Services {
							if svc.Name == m.svc.Name {
								m.err = "service name already exists"
								return m, nil
							}
						}
						break
					}
				}
				m.err = ""
				m.step = svcStepPorts
				m.nameInput.Blur()
				m.portHost.Focus()
				return m, textinput.Blink
			}
		} else {
			// Catalog / Search flow: image is read-only, just service name input
			if key.Matches(msg, addSvcNext) {
				m.svc.Name = config.SanitizeComposeName(strings.TrimSpace(m.nameInput.Value()))
				if m.svc.Name == "" {
					return m, nil
				}
				for _, env := range m.profile.Environments {
					if env.Name == m.envName {
						for _, svc := range env.Services {
							if svc.Name == m.svc.Name {
								m.err = "service name already exists"
								return m, nil
							}
						}
						break
					}
				}
				m.err = ""
				m.step = svcStepPorts
				m.nameInput.Blur()
				m.portHost.Focus()
				return m, textinput.Blink
			}
		}
	case svcStepPorts:
		return m.handlePorts(msg)
	case svcStepEnvVars:
		return m.handleEnvVars(msg)
	case svcStepVolumes:
		return m.handleVolumes(msg)
	case svcStepRunCommand:
		return m.handleRunCommand(msg)
	case svcStepReview:
		if key.Matches(msg, addSvcNext) {
			return m, m.saveService()
		}
	}
	return m.updateActiveInput(msg)
}

func (m *AddServiceModel) handleSource(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	dockerCount := 0
	hostOffset := 0
	if len(hub.Catalog) > 0 {
		dockerCount = 3 // Catalog, Search, Manual
	} else {
		dockerCount = 1 // Manual
	}
	hostOffset = dockerCount
	maxIdx := dockerCount // last item is "Host: Run command" at position dockerCount

	switch {
	case key.Matches(msg, addSvcUp) && m.sourceCursor > 0:
		m.sourceCursor--
	case key.Matches(msg, addSvcDown) && m.sourceCursor < maxIdx:
		m.sourceCursor++
	case key.Matches(msg, addSvcNext):
		if m.sourceCursor < hostOffset {
			// Docker options
			if len(hub.Catalog) > 0 {
				switch m.sourceCursor {
				case 0:
					m.sourcePath = "catalog"
					m.step = svcStepCatalog
				case 1:
					m.sourcePath = "search"
					m.step = svcStepSearch
					m.searchInput.Focus()
					return m, textinput.Blink
				case 2:
					m.sourcePath = "manual"
					m.step = svcStepName
					m.nameFocus = 0
					m.imageInput.Focus()
					return m, textinput.Blink
				}
			} else {
				m.sourcePath = "manual"
				m.step = svcStepName
				m.nameFocus = 0
				m.imageInput.Focus()
				return m, textinput.Blink
			}
		} else {
			// Host: Run command
			m.sourcePath = "run_command"
			m.step = svcStepRunCommand
			m.runCmdFocus = 0
			m.runCmdName.Focus()
			return m, textinput.Blink
		}
	}
	return m.updateActiveInput(msg)
}

// prefillFromCatalog looks up known defaults for the given image and pre-populates
// ports, env vars, and volumes. Safe to call from any source (catalog, search, manual).
func (m *AddServiceModel) prefillFromCatalog(image string) {
	base := image
	if idx := strings.IndexByte(base, ':'); idx >= 0 {
		base = base[:idx] // strip tag
	}
	// If it looks like "library/xxx", strip the library/ prefix
	base = strings.TrimPrefix(base, "library/")

	// Reset before populating — prevents duplicates if called multiple times
	m.ports = nil
	m.envVars = nil
	m.volumes = nil
	m.defaultPorts = nil
	m.defaultEnvVars = nil
	m.defaultVolumes = nil
	m.protectedEnvKeys = make(map[string]bool)

	for _, entry := range hub.Catalog {
		if entry.Image != base {
			continue
		}
		// prefill ports
		for _, p := range entry.DefaultPorts {
			parts := strings.SplitN(p, ":", 2)
			if len(parts) == 2 {
				m.ports = append(m.ports, config.PortMapping{Host: parts[0], Container: parts[1]})
			}
		}
		// prefill env vars
		for k, v := range entry.DefaultEnv {
			m.envVars = append(m.envVars, config.EnvVar{Key: k, Value: v})
			m.protectedEnvKeys[k] = true
		}
		// prefill volume — use named volume so Docker manages permissions
		// and each service has isolated data (no shared ./data collisions)
		if entry.DefaultVolumeTarget != "" {
			m.volumes = append(m.volumes, config.VolumeMapping{
				Source: "", // compose generator will create profile_env_service_data
				Target: entry.DefaultVolumeTarget,
				Named:  true,
			})
			m.volNamed = true // sync toggle to match the prefilled volume
		}
		// snapshot defaults for reset on re-entry
		m.defaultPorts = append([]config.PortMapping{}, m.ports...)
		m.defaultEnvVars = append([]config.EnvVar{}, m.envVars...)
		m.defaultVolumes = append([]config.VolumeMapping{}, m.volumes...)
		return
	}
}

func (m *AddServiceModel) handleCatalog(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cat := hub.Catalog
	switch {
	case key.Matches(msg, addSvcUp) && m.catalogCursor > 0:
		m.catalogCursor--
	case key.Matches(msg, addSvcDown) && m.catalogCursor < len(cat)-1:
		m.catalogCursor++
	case key.Matches(msg, addSvcNext):
		entry := cat[m.catalogCursor]
		m.svc.Image = entry.Image
		m.svc.Name = config.SanitizeComposeName(entry.Image)
		m.svc.Tag = entry.DefaultTag
		m.prefillFromCatalog(entry.Image)
		// go to tag selection
		m.step = svcStepSelectTag
		m.loadingTags = true
		image := entry.Image
		return m, func() tea.Msg {
			tags, err := hub.FetchTags(image, 20)
			errStr := ""
			if err != nil {
				errStr = err.Error()
			}
			return hubTagsResultMsg{tags: tags, err: errStr}
		}
	}
	return m.updateActiveInput(msg)
}

func (m *AddServiceModel) handleSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, addSvcUp) && len(m.searchResults) > 0:
		if m.searchCursor > 0 {
			m.searchCursor--
		}
	case key.Matches(msg, addSvcDown) && len(m.searchResults) > 0:
		if m.searchCursor < len(m.searchResults)-1 {
			m.searchCursor++
		}
	case key.Matches(msg, addSvcNext):
		query := strings.TrimSpace(m.searchInput.Value())
		if query == "" {
			break
		}
		if query != m.lastSearchQuery {
			// query changed — search (or re-search)
			m.lastSearchQuery = query
			m.searching = true
			return m, func() tea.Msg {
				results, err := hub.Search(query, 15)
				errStr := ""
				if err != nil {
					errStr = err.Error()
				}
				return hubSearchResultMsg{results: results, err: errStr}
			}
		}
		// same query + results exist → select result at cursor
		if len(m.searchResults) > 0 {
			result := m.searchResults[m.searchCursor]
			m.svc.Image = result.Name
			m.svc.Name = strings.ReplaceAll(result.Name, "/", "-")
			m.prefillFromCatalog(result.Name)
			m.step = svcStepSelectTag
			m.loadingTags = true
			image := result.Name
			return m, func() tea.Msg {
				tags, err := hub.FetchTags(image, 20)
				errStr := ""
				if err != nil {
					errStr = err.Error()
				}
				return hubTagsResultMsg{tags: tags, err: errStr}
			}
		}
	}
	return m.updateActiveInput(msg)
}

func (m *AddServiceModel) handleTagSelect(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, addSvcUp) && m.tagCursor > 0:
		m.tagCursor--
	case key.Matches(msg, addSvcDown) && m.tagCursor < len(m.tags)-1:
		m.tagCursor++
	case key.Matches(msg, addSvcNext):
		if len(m.tags) > 0 {
			m.svc.Tag = m.tags[m.tagCursor].Name
		}
		m.nameInput.SetValue(m.svc.Name)
		m.step = svcStepName
		m.nameInput.Focus()
		return m, textinput.Blink
	}
	return m.updateActiveInput(msg)
}

func (m *AddServiceModel) handlePorts(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, addSvcUpField), key.Matches(msg, addSvcDownField):
		if m.portHost.Focused() {
			m.portHost.Blur()
			m.portContainer.Focus()
		} else {
			m.portContainer.Blur()
			m.portHost.Focus()
		}
		return m, textinput.Blink
	case key.Matches(msg, addSvcNext):
		h := strings.TrimSpace(m.portHost.Value())
		c := strings.TrimSpace(m.portContainer.Value())
		if h != "" && c != "" {
			// Only add if not a duplicate
			isDup := false
			for _, p := range m.ports {
				if p.Host == h && p.Container == c {
					isDup = true
					break
				}
			}
			if !isDup {
				m.ports = append(m.ports, config.PortMapping{Host: h, Container: c})
			}
			m.portsDeleteArmed = false
			m.resetArmed = false
			m.portHost.SetValue("")
			m.portContainer.SetValue("")
			m.portHost.Focus()
			return m, nil
		}
		// Empty inputs → advance to next step
		m.portsDeleteArmed = false
		m.resetArmed = false
		m.svc.Ports = m.ports
		m.err = ""
		m.step = svcStepEnvVars
		m.envKey.Focus()
		return m, textinput.Blink
	case key.Matches(msg, addSvcDelete):
		h := strings.TrimSpace(m.portHost.Value())
		c := strings.TrimSpace(m.portContainer.Value())
		if h != "" || c != "" {
			// Typing d in a field with content → let it through
			m.portsDeleteArmed = false
			return m.updateActiveInput(msg)
		}
		// Don't arm if last port is protected or only one left
		if len(m.ports) <= 1 {
			return m, nil
		}
		last := m.ports[len(m.ports)-1]
		if last.Host == "5432" && last.Container == "5432" {
			return m, nil
		}
		if !m.portsDeleteArmed {
			// First d: arm deletion
			m.portsDeleteArmed = true
			return m, nil
		}
		// Second d: delete last port
		m.portsDeleteArmed = false
		m.resetArmed = false
		if len(m.ports) > 0 {
			m.ports = m.ports[:len(m.ports)-1]
		}
		return m, nil
	case key.Matches(msg, addSvcReset):
		h := strings.TrimSpace(m.portHost.Value())
		c := strings.TrimSpace(m.portContainer.Value())
		if h != "" || c != "" {
			m.resetArmed = false
			return m.updateActiveInput(msg)
		}
		if len(m.defaultPorts) == 0 {
			return m, nil
		}
		if !m.resetArmed {
			m.resetArmed = true
			return m, nil
		}
		// Second r: reset to defaults
		m.resetArmed = false
		m.ports = append([]config.PortMapping{}, m.defaultPorts...)
		return m, nil
	}
	m.portsDeleteArmed = false
	m.resetArmed = false
	return m.updateActiveInput(msg)
}

func (m *AddServiceModel) handleEnvVars(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, addSvcUpField), key.Matches(msg, addSvcDownField):
		m.envVarsDeleteArmed = false
		if m.envFocus == 0 {
			m.envFocus = 1
			m.envKey.Blur()
			m.envVal.Focus()
		} else {
			m.envFocus = 0
			m.envVal.Blur()
			m.envKey.Focus()
		}
		return m, textinput.Blink
	case key.Matches(msg, addSvcNext):
		k := strings.TrimSpace(m.envKey.Value())
		v := m.envVal.Value()
		if k != "" {
			// If key exists → overwrite, otherwise append
			found := false
			for i, e := range m.envVars {
				if e.Key == k {
					m.envVars[i].Value = v
					found = true
					break
				}
			}
			if !found {
				m.envVars = append(m.envVars, config.EnvVar{Key: k, Value: v})
			}
			m.envVarsDeleteArmed = false
			m.resetArmed = false
			m.envKey.SetValue("")
			m.envVal.SetValue("")
			m.envKey.Focus()
			m.envFocus = 0
			return m, nil
		}
		// Empty key → advance to next step
		m.envVarsDeleteArmed = false
		m.resetArmed = false
		m.svc.EnvVars = m.envVars
		m.err = ""
		m.step = svcStepVolumes
		m.volSource.Focus()
		return m, textinput.Blink
	case key.Matches(msg, addSvcDelete):
		k := strings.TrimSpace(m.envKey.Value())
		if k != "" {
			// Typing in the key field → let d through
			m.envVarsDeleteArmed = false
			return m.updateActiveInput(msg)
		}
		// Don't arm if last item is protected
		if len(m.envVars) > 0 && m.protectedEnvKeys[m.envVars[len(m.envVars)-1].Key] {
			return m, nil
		}
		if !m.envVarsDeleteArmed {
			m.envVarsDeleteArmed = true
			return m, nil
		}
		// Second d: remove last env var
		m.envVarsDeleteArmed = false
		m.resetArmed = false
		if len(m.envVars) > 0 {
			last := m.envVars[len(m.envVars)-1]
			if m.protectedEnvKeys[last.Key] {
				// protected — can't delete
				return m, nil
			}
			m.envVars = m.envVars[:len(m.envVars)-1]
		}
		return m, nil
	case key.Matches(msg, addSvcReset):
		k := strings.TrimSpace(m.envKey.Value())
		if k != "" {
			m.resetArmed = false
			return m.updateActiveInput(msg)
		}
		if len(m.defaultEnvVars) == 0 {
			return m, nil
		}
		if !m.resetArmed {
			m.resetArmed = true
			return m, nil
		}
		// Second r: reset to defaults
		m.resetArmed = false
		m.envVars = append([]config.EnvVar{}, m.defaultEnvVars...)
		return m, nil
	}
	m.envVarsDeleteArmed = false
	m.resetArmed = false
	return m.updateActiveInput(msg)
}

// resolveVolumeTarget returns the default volume target for the selected image,
// or empty string if unknown.
func (m *AddServiceModel) resolveVolumeTarget() string {
	base := m.svc.Image
	if idx := strings.IndexByte(base, ':'); idx >= 0 {
		base = base[:idx]
	}
	base = strings.TrimPrefix(base, "library/")
	for _, entry := range hub.Catalog {
		if entry.Image == base && entry.DefaultVolumeTarget != "" {
			return entry.DefaultVolumeTarget
		}
	}
	return ""
}

func (m *AddServiceModel) handleVolumes(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, addSvcUpField), key.Matches(msg, addSvcDownField):
		// Only one input now — tab just focuses source
		return m, nil
	case key.Matches(msg, addSvcToggle):
		src := strings.TrimSpace(m.volSource.Value())
		if src != "" {
			// Typing in Source → let t through
			return m.updateActiveInput(msg)
		}
		m.volumesDeleteArmed = false
		m.volNamed = !m.volNamed
		if m.volNamed && m.volSource.Value() == "" {
			// Auto-generate a name when toggling to named volume
			gen := m.profile.Name + "_" + strings.ReplaceAll(m.svc.Name, "-", "_") + "_data"
			m.volSource.SetValue(gen)
		} else if !m.volNamed && m.volSource.Value() != "" {
			// Toggle back to bind mount — clear auto-generated name
			src := strings.TrimSpace(m.volSource.Value())
			if !strings.HasPrefix(src, "./") && !strings.HasPrefix(src, "/") {
				m.volSource.SetValue("")
			}
		}
		return m, nil
	case key.Matches(msg, addSvcNext):
		src := strings.TrimSpace(m.volSource.Value())
		if src != "" {
			// Validate: bind mount needs a path-like source
			if !m.volNamed && !strings.HasPrefix(src, "./") && !strings.HasPrefix(src, "/") && !strings.HasPrefix(src, "~") {
				m.err = "bind mount source should start with ./ or / — use t to switch to named volume"
				return m, nil
			}
			// Resolve target automatically from image
			tgt := m.resolveVolumeTarget()
			if tgt == "" {
				m.err = "unknown volume target for this image — remove the volume or switch to a known image"
				return m, nil
			}
			// If target already exists, UPDATE the source instead of adding
			updated := false
			for i, v := range m.volumes {
				if v.Target == tgt {
					m.volumes[i].Source = src
					m.volumes[i].Named = m.volNamed
					updated = true
					break
				}
			}
			if !updated {
				m.volumes = append(m.volumes, config.VolumeMapping{
					Source: src,
					Target: tgt,
					Named:  m.volNamed,
				})
			}
			m.err = ""
			m.volumesDeleteArmed = false
			m.resetArmed = false
			m.volSource.SetValue("")
			m.volSource.Focus()
			return m, nil
		}
		// Empty source → advance to next step
		m.volumesDeleteArmed = false
		m.resetArmed = false
		m.svc.Volumes = m.volumes
		m.err = ""
		m.step = svcStepReview
	case key.Matches(msg, addSvcDelete):
		src := strings.TrimSpace(m.volSource.Value())
		if src != "" {
			m.volumesDeleteArmed = false
			return m.updateActiveInput(msg)
		}
		if len(m.volumes) <= 1 {
			return m, nil
		}
		if !m.volumesDeleteArmed {
			m.volumesDeleteArmed = true
			return m, nil
		}
		// Second d: remove last volume
		m.volumesDeleteArmed = false
		m.resetArmed = false
		m.volumes = m.volumes[:len(m.volumes)-1]
		return m, nil
	case key.Matches(msg, addSvcReset):
		src := strings.TrimSpace(m.volSource.Value())
		if src != "" {
			m.resetArmed = false
			return m.updateActiveInput(msg)
		}
		if len(m.defaultVolumes) == 0 {
			return m, nil
		}
		if !m.resetArmed {
			m.resetArmed = true
			return m, nil
		}
		// Second r: reset to defaults
		m.resetArmed = false
		m.volumes = append([]config.VolumeMapping{}, m.defaultVolumes...)
		return m, nil
	}
	m.volumesDeleteArmed = false
	m.resetArmed = false
	return m.updateActiveInput(msg)
}

func (m *AddServiceModel) handleRunCommand(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, addSvcUpField):
		if m.runCmdFocus > 0 {
			m.runCmdFocus--
		} else {
			m.runCmdFocus = 2
		}
		m.runCmdName.Blur()
		m.runCmdCmd.Blur()
		m.runCmdWorkDir.Blur()
		switch m.runCmdFocus {
		case 0:
			m.runCmdName.Focus()
		case 1:
			m.runCmdCmd.Focus()
		case 2:
			m.runCmdWorkDir.Focus()
		}
		return m, textinput.Blink
	case key.Matches(msg, addSvcDownField):
		if m.runCmdFocus < 2 {
			m.runCmdFocus++
		} else {
			m.runCmdFocus = 0
		}
		m.runCmdName.Blur()
		m.runCmdCmd.Blur()
		m.runCmdWorkDir.Blur()
		switch m.runCmdFocus {
		case 0:
			m.runCmdName.Focus()
		case 1:
			m.runCmdCmd.Focus()
		case 2:
			m.runCmdWorkDir.Focus()
		}
		return m, textinput.Blink

	case key.Matches(msg, addSvcNext):
		name := config.SanitizeComposeName(strings.TrimSpace(m.runCmdName.Value()))
		if name == "" || strings.TrimSpace(m.runCmdCmd.Value()) == "" {
			return m, nil
		}
		cmdStr := strings.TrimSpace(m.runCmdCmd.Value())
		for _, env := range m.profile.Environments {
			if env.Name == m.envName {
				for _, svc := range env.Services {
					if svc.Name == name || svc.ComposeName() == name {
						m.err = "name already exists"
						return m, nil
					}
				}
				for _, lp := range env.LocalProcesses {
					if lp.Name == name {
						m.err = "name already exists"
						return m, nil
					}
				}
				break
			}
		}
		m.err = ""
		return m, m.saveLocalProcess(name, cmdStr, strings.TrimSpace(m.runCmdWorkDir.Value()))

	case key.Matches(msg, addSvcTab):
		if m.runCmdFocus < 2 {
			m.runCmdFocus++
		} else {
			m.runCmdFocus = 0
		}
		m.runCmdName.Blur()
		m.runCmdCmd.Blur()
		m.runCmdWorkDir.Blur()
		switch m.runCmdFocus {
		case 0:
			m.runCmdName.Focus()
		case 1:
			m.runCmdCmd.Focus()
		case 2:
			m.runCmdWorkDir.Focus()
		}
		return m, textinput.Blink
	}
	return m.updateActiveInput(msg)
}

func (m *AddServiceModel) saveLocalProcess(name, command, workDir string) tea.Cmd {
	return func() tea.Msg {
		for i, env := range m.profile.Environments {
			if env.Name == m.envName {
				m.profile.Environments[i].LocalProcesses = append(
					m.profile.Environments[i].LocalProcesses,
					config.LocalProcess{Name: name, Command: command, WorkDir: workDir},
				)
				break
			}
		}
		m.profile.UpdatedAt = time.Now()
		if err := m.cfg.UpdateProfile(*m.profile); err != nil {
			return ErrorMsg(err.Error())
		}
		return svcSavedMsg{}
	}
}

func (m *AddServiceModel) viewRunCommand() string {
	var b strings.Builder
	b.WriteString("  Run command  (local process)\n\n")

	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	normalStyle := lipgloss.NewStyle()

	nameLabel, cmdLabel, dirLabel := normalStyle, normalStyle, normalStyle
	switch m.runCmdFocus {
	case 0:
		nameLabel = focusStyle
	case 1:
		cmdLabel = focusStyle
	case 2:
		dirLabel = focusStyle
	}

	b.WriteString("  " + nameLabel.Render("Name:    ") + m.runCmdName.View() + "\n")
	b.WriteString("  " + cmdLabel.Render("Command: ") + m.runCmdCmd.View() + "\n")
	b.WriteString("  " + dirLabel.Render("Workdir: ") + styles.StyleSubtitle.Render("(optional) ") + m.runCmdWorkDir.View() + "\n")

	b.WriteString("\n")
	btnW := 60
	button := styles.StyleButton.Render(" Save service ")
	b.WriteString(lipgloss.PlaceHorizontal(btnW, lipgloss.Center, button) + "\n")

	b.WriteString("\n")
	b.WriteString(styles.BackHelp(
		styles.HelpItem("↑ ↓", "field"),
	) + "\n")
	return b.String()
}

func (m *AddServiceModel) saveService() tea.Cmd {
	return func() tea.Msg {
		for _, env := range m.profile.Environments {
			if env.Name == m.envName {
				if err := checkPortConflicts(m.svc, env.Services); err != nil {
					return ErrorMsg(err.Error())
				}
				break
			}
		}
		// Find environment and append service
		for i, env := range m.profile.Environments {
			if env.Name == m.envName {
				m.profile.Environments[i].Services = append(
					m.profile.Environments[i].Services, m.svc,
				)
				break
			}
		}
		m.profile.UpdatedAt = time.Now()
		if err := m.cfg.UpdateProfile(*m.profile); err != nil {
			return ErrorMsg(err.Error())
		}
		// Regenerate compose
		if _, err := config.GenerateCompose(*m.profile, m.envName); err != nil {
			return ErrorMsg("compose gen: " + err.Error())
		}
		return svcSavedMsg{}
	}
}

func checkPortConflicts(newService config.Service, existingServices []config.Service) error {
	hostPorts := make(map[string]string) // hostPort -> owning service name

	for _, svc := range existingServices {
		if svc.Name == newService.Name {
			continue
		}
		for _, p := range svc.Ports {
			if p.Host != "" {
				hostPorts[p.Host] = svc.Name
			}
		}
	}

	seen := make(map[string]bool)
	for _, p := range newService.Ports {
		if p.Host == "" {
			continue
		}
		if owner, exists := hostPorts[p.Host]; exists {
			return fmt.Errorf("Port %s is already used by '%s'. Choose a different host port.", p.Host, owner)
		}
		if seen[p.Host] {
			return fmt.Errorf("Port %s is mapped multiple times in the new service.", p.Host)
		}
		seen[p.Host] = true
	}

	return nil
}

func (m *AddServiceModel) updateActiveInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.step {
	case svcStepSearch:
		m.searchInput, cmd = m.searchInput.Update(msg)
	case svcStepName:
		if m.sourcePath == "manual" && m.nameFocus == 0 {
			m.imageInput, cmd = m.imageInput.Update(msg)
		} else {
			m.nameInput, cmd = m.nameInput.Update(msg)
		}
	case svcStepPorts:
		if m.portHost.Focused() {
			m.portHost, cmd = m.portHost.Update(msg)
		} else {
			m.portContainer, cmd = m.portContainer.Update(msg)
		}
	case svcStepEnvVars:
		if m.envFocus == 0 {
			m.envKey, cmd = m.envKey.Update(msg)
		} else {
			m.envVal, cmd = m.envVal.Update(msg)
		}
	case svcStepVolumes:
		m.volSource, cmd = m.volSource.Update(msg)
	case svcStepRunCommand:
		switch m.runCmdFocus {
		case 0:
			m.runCmdName, cmd = m.runCmdName.Update(msg)
		case 1:
			m.runCmdCmd, cmd = m.runCmdCmd.Update(msg)
		case 2:
			m.runCmdWorkDir, cmd = m.runCmdWorkDir.Update(msg)
		}
	}
	return m, cmd
}

// ── View ─────────────────────────────────────────────────────────────────────

func (m *AddServiceModel) View() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styles.StyleTitle.Render(fmt.Sprintf(" Add Service → %s / %s ", m.profile.Name, m.envName)))
	b.WriteString("\n\n")

	switch m.step {
	case svcStepSource:
		b.WriteString(m.viewSource())
	case svcStepCatalog:
		b.WriteString(m.viewCatalog())
	case svcStepSearch:
		b.WriteString(m.viewSearch())
	case svcStepSelectTag:
		b.WriteString(m.viewTags())
	case svcStepName:
		b.WriteString(m.viewName())
	case svcStepPorts:
		b.WriteString(m.viewPorts())
	case svcStepEnvVars:
		b.WriteString(m.viewEnvVars())
	case svcStepVolumes:
		b.WriteString(m.viewVolumes())
	case svcStepRunCommand:
		b.WriteString(m.viewRunCommand())
	case svcStepReview:
		b.WriteString(m.viewReview())
	}

	if m.err != "" {
		b.WriteString("\n" + styles.StyleError.Render("  ✗ "+m.err) + "\n")
	}
	return b.String()
}

func (m *AddServiceModel) viewSource() string {
	selStyle := lipgloss.NewStyle().
		Foreground(styles.ColorPrimary).
		Bold(true)

	groupHeaderStyle := lipgloss.NewStyle().
		Foreground(styles.ColorSecondary).
		Bold(true)

	var b strings.Builder
	b.WriteString("  Select source:\n\n")

	// Docker group
	var dockerItems []string
	if len(hub.Catalog) > 0 {
		dockerItems = []string{
			"Curated catalog        (postgres, mongo, redis, kafka…)",
			"Search Docker Hub      (any public image)",
			"Manual entry           (type image name directly)",
		}
	} else {
		dockerItems = []string{
			"Manual entry           (type image name directly)",
		}
	}

	b.WriteString("  " + groupHeaderStyle.Render("Docker") + "\n")
	for i, o := range dockerItems {
		if i == m.sourceCursor {
			b.WriteString(selStyle.Render("  > " + o))
		} else {
			b.WriteString("    " + o)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")

	// Host group
	hostStartIdx := len(dockerItems)
	b.WriteString("  " + groupHeaderStyle.Render("Host") + "\n")
	hostLabel := "Run command            (manage local processes)"
	if m.sourceCursor == hostStartIdx {
		b.WriteString(selStyle.Render("  > " + hostLabel))
	} else {
		b.WriteString("    " + hostLabel)
	}
	b.WriteString("\n")

	b.WriteString("\n\n")
	b.WriteString(styles.BackHelp(styles.HelpItem("↑ ↓", "switch field"), styles.HelpItem("↵", "add/continue")))
	return b.String()
}

func (m *AddServiceModel) viewCatalog() string {
	var b strings.Builder
	b.WriteString("  Select a service from the catalog:\n\n")
	selStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)

	// overhead: View() = 4, catalog = 4 (header 2 + after 2) = 8
	win := addSvcItemWindow(len(hub.Catalog), m.catalogCursor, m.height, 8)

	if win.hasAbove {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", win.start)))
		b.WriteString("\n")
	}
	for i := win.start; i < win.end; i++ {
		entry := hub.Catalog[i]
		prefix := "    "
		if i == m.catalogCursor {
			prefix = "  > "
		}
		label := fmt.Sprintf("%s%-16s  %s:%s  — %s",
			prefix, entry.Name, entry.Image, entry.DefaultTag, truncate(entry.Description, 44))
		if i == m.catalogCursor {
			b.WriteString(selStyle.Render(label))
		} else {
			b.WriteString(label)
		}
		b.WriteString("\n")
	}
	if win.hasBelow {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↓ %d more", len(hub.Catalog)-win.end)))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styles.BackHelp(styles.HelpItem("↑ ↓", "navigate"), styles.HelpItem("↵", "choose image")))
	return b.String()
}

func (m *AddServiceModel) viewSearch() string {
	var b strings.Builder
	b.WriteString("  Search Docker Hub:\n\n")
	b.WriteString("  " + m.searchInput.View() + "  ")
	if m.searching {
		b.WriteString(styles.StyleSubtitle.Render("searching…"))
	}
	b.WriteString("\n\n")
	if m.searchErr != "" {
		b.WriteString(styles.StyleError.Render("  ✗ "+m.searchErr) + "\n")
	}
	selStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)

	// overhead: View() = 4, search = ~7 (header 2 + input 2 + err 1 + after 2)
	win := addSvcItemWindow(len(m.searchResults), m.searchCursor, m.height, 11)

	if win.hasAbove {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", win.start)))
		b.WriteString("\n")
	}
	for i := win.start; i < win.end; i++ {
		r := m.searchResults[i]
		official := ""
		if r.IsOfficial {
			official = styles.StyleSuccess.Render(" [official]")
		}
		label := fmt.Sprintf("%-32s  stars: %d%s  — %s",
			r.Name, r.StarCount, official, truncate(r.Description, 38))
		if i == m.searchCursor {
			b.WriteString(selStyle.Render("  > " + label))
		} else {
			b.WriteString("    " + label)
		}
		b.WriteString("\n")
	}
	if win.hasBelow {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↓ %d more", len(m.searchResults)-win.end)))
		b.WriteString("\n")
	}
	if len(m.searchResults) == 0 && !m.searching {
		b.WriteString("  Type a name and press ↵ to search\n")
	}
	b.WriteString("\n")
	b.WriteString(styles.BackHelp(styles.HelpItem("↑ ↓", "navigate"), styles.HelpItem("↵", "search/select")))
	return b.String()
}

func (m *AddServiceModel) viewTags() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  Select tag for %s:\n\n", m.svc.Image))
	if m.loadingTags {
		b.WriteString(styles.StyleSubtitle.Render("  Loading tags from Docker Hub…\n"))
		return b.String()
	}
	selStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)

	// overhead: View() = 4, tags = 4 (header 2 + after 2) = 8
	win := addSvcItemWindow(len(m.tags), m.tagCursor, m.height, 8)

	if win.hasAbove {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", win.start)))
		b.WriteString("\n")
	}
	for i := win.start; i < win.end; i++ {
		tag := m.tags[i]
		size := ""
		if tag.FullSize > 0 {
			size = fmt.Sprintf("  %.0f MB", float64(tag.FullSize)/1_000_000)
		}
		label := fmt.Sprintf("%-24s%s", tag.Name, size)
		if i == m.tagCursor {
			b.WriteString(selStyle.Render("  > " + label))
		} else {
			b.WriteString("    " + label)
		}
		b.WriteString("\n")
	}
	if win.hasBelow {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↓ %d more", len(m.tags)-win.end)))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styles.BackHelp(styles.HelpItem("↑ ↓", "navigate"), styles.HelpItem("↵", "choose tag")))
	return b.String()
}

func (m *AddServiceModel) viewName() string {
	var b strings.Builder
	if m.sourcePath == "manual" {
		if m.nameFocus == 0 {
			// Step 1: enter image name
			b.WriteString("  Image\n\n")
			b.WriteString("  " + m.imageInput.View() + "\n")
			b.WriteString("  Docker image to pull (e.g. postgres:16, mongo:4.2)\n")
			b.WriteString("\n  " + styles.BackHelp(styles.HelpItem("↑ ↓", "next field"), styles.HelpItem("↵", "use image")))
		} else {
			// Step 2: confirm/enter service name
			b.WriteString(fmt.Sprintf("  Image: %s\n\n", styles.StyleCode.Render(m.svc.FullImage())))
			focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
			b.WriteString("  " + focusStyle.Render("Service name: ") + m.nameInput.View() + "\n")
			b.WriteString("  Used as the compose service key and container name suffix\n")
			b.WriteString("\n  " + styles.BackHelp(styles.HelpItem("↑ ↓", "switch field"), styles.HelpItem("↵", "continue")))
		}
	} else {
		b.WriteString(fmt.Sprintf("  Image: %s\n\n", styles.StyleCode.Render(m.svc.FullImage())))
		focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
		b.WriteString("  " + focusStyle.Render("Service name: ") + m.nameInput.View() + "\n")
		b.WriteString("  Used as the compose service key and container name suffix\n")
		b.WriteString("\n" + styles.BackHelp(styles.HelpItem("↵", "continue")))
	}
	return b.String()
}

func (m *AddServiceModel) viewPorts() string {
	var b strings.Builder
	b.WriteString("  Port mappings  (host:container)\n\n")

	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	normalStyle := lipgloss.NewStyle()

	hostLabel, contLabel := normalStyle, normalStyle
	if m.portHost.Focused() {
		hostLabel = focusStyle
	} else if m.portContainer.Focused() {
		contLabel = focusStyle
	}

	b.WriteString("  " + hostLabel.Render("Host port:      ") + m.portHost.View() + "\n")
	b.WriteString("  " + contLabel.Render("Container port: ") + m.portContainer.View() + "\n\n")
	win := addSvcTailWindow(len(m.ports), m.height, 13)
	if win.hasAbove {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", win.start)))
		b.WriteString("\n")
	}
	for _, p := range m.ports[win.start:win.end] {
		b.WriteString("    " + styles.StyleCode.Render(p.String()) + "\n")
	}
	if len(m.ports) == 0 {
		b.WriteString(styles.StyleSubtitle.Render("  Press ↵ on empty inputs to continue."))
		b.WriteString("\n")
	}
	if m.portsDeleteArmed {
		b.WriteString(styles.StyleError.Render("  Press ctrl+d again to confirm removal"))
		b.WriteString("\n")
	}
	if m.resetArmed {
		b.WriteString(styles.StyleError.Render("  Press ctrl+r again to reset to defaults"))
		b.WriteString("\n")
	}
	var actItems []styles.HelpItemResult
	if len(m.ports) > 1 {
		actItems = append(actItems, styles.HelpItem("ctrl+d ctrl+d", "remove last"))
	}
	if m.portsCanReset() {
		actItems = append(actItems, styles.HelpItem("ctrl+r ctrl+r", "reset"))
	}
	b.WriteString("\n")
	b.WriteString(styles.HelpBar(
		styles.HelpGroup{Label: "Manage", Items: actItems},
	))
	if len(actItems) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(styles.BackHelp(styles.HelpItem("↑ ↓", "switch field"), styles.HelpItem("↵", "add/continue")))
	return b.String()
}

func (m *AddServiceModel) portsCanReset() bool {
	if len(m.defaultPorts) == 0 {
		return false
	}
	if len(m.ports) != len(m.defaultPorts) {
		return true
	}
	for i, p := range m.ports {
		if p != m.defaultPorts[i] {
			return true
		}
	}
	return false
}

func (m *AddServiceModel) envVarsCanReset() bool {
	if len(m.defaultEnvVars) == 0 {
		return false
	}
	if len(m.envVars) != len(m.defaultEnvVars) {
		return true
	}
	for i, e := range m.envVars {
		if e != m.defaultEnvVars[i] {
			return true
		}
	}
	return false
}

func (m *AddServiceModel) volumesCanReset() bool {
	if len(m.defaultVolumes) == 0 {
		return false
	}
	if len(m.volumes) != len(m.defaultVolumes) {
		return true
	}
	for i, v := range m.volumes {
		if v != m.defaultVolumes[i] {
			return true
		}
	}
	return false
}

func (m *AddServiceModel) envVarsRemovable() bool {
	if len(m.envVars) == 0 {
		return false
	}
	return !m.protectedEnvKeys[m.envVars[len(m.envVars)-1].Key]
}

func (m *AddServiceModel) viewEnvVars() string {
	var b strings.Builder
	b.WriteString("  Environment variables\n\n")

	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	normalStyle := lipgloss.NewStyle()

	keyLabel, valLabel := normalStyle, normalStyle
	switch m.envFocus {
	case 0:
		keyLabel = focusStyle
	case 1:
		valLabel = focusStyle
	}

	b.WriteString("  " + keyLabel.Render("Key:  ") + m.envKey.View() + "\n")
	b.WriteString("  " + valLabel.Render("Value:") + m.envVal.View() + "\n\n")
	win := addSvcTailWindow(len(m.envVars), m.height, 13)
	if win.hasAbove {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", win.start)))
		b.WriteString("\n")
	}
	for _, e := range m.envVars[win.start:win.end] {
		b.WriteString("    " + styles.StyleCode.Render(e.Key) + " = " + e.Value + "\n")
	}
	if len(m.envVars) == 0 {
		b.WriteString(styles.StyleSubtitle.Render("  Press ↵ on an empty key to continue."))
		b.WriteString("\n")
	}
	if m.envVarsDeleteArmed {
		b.WriteString(styles.StyleError.Render("  Press ctrl+d again to confirm removal"))
		b.WriteString("\n")
	}
	if m.resetArmed {
		b.WriteString(styles.StyleError.Render("  Press ctrl+r again to reset to defaults"))
		b.WriteString("\n")
	}
	var actItems []styles.HelpItemResult
	if m.envVarsRemovable() {
		actItems = append(actItems, styles.HelpItem("ctrl+d ctrl+d", "remove last"))
	}
	if m.envVarsCanReset() {
		actItems = append(actItems, styles.HelpItem("ctrl+r ctrl+r", "reset"))
	}
	b.WriteString("\n")
	b.WriteString(styles.HelpBar(
		styles.HelpGroup{Label: "Manage", Items: actItems},
	))
	if len(actItems) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(styles.BackHelp(styles.HelpItem("↑ ↓", "switch field"), styles.HelpItem("↵", "add/continue")))
	return b.String()
}

func (m *AddServiceModel) viewVolumes() string {
	namedLabel := "bind mount"
	if m.volNamed {
		namedLabel = "named volume"
	}
	var b strings.Builder
	b.WriteString("  Volumes\n\n")

	focusStyle := lipgloss.NewStyle().Foreground(styles.ColorAccent).Bold(true)
	normalStyle := lipgloss.NewStyle()

	label := focusStyle
	if !m.volSource.Focused() {
		label = normalStyle
	}

	b.WriteString("  " + label.Render("Source: ") + m.volSource.View() + "  " + styles.StyleCode.Render(namedLabel) + "\n\n")
	win := addSvcTailWindow(len(m.volumes), m.height, 13)
	if win.hasAbove {
		b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  ↑ %d more", win.start)))
		b.WriteString("\n")
	}
	for _, v := range m.volumes[win.start:win.end] {
		source := v.Source
		if v.Named && source == "" {
			source = fmt.Sprintf("%s_%s_%s_data", m.profile.Name, m.envName, m.svc.Name)
		}
		b.WriteString(fmt.Sprintf("    %s → %s\n", source, v.Target))
	}
	if len(m.volumes) == 0 {
		b.WriteString(styles.StyleSubtitle.Render("  Press ↵ on an empty source to continue."))
		b.WriteString("\n")
	}
	if m.volumesDeleteArmed {
		b.WriteString(styles.StyleError.Render("  Press ctrl+d again to confirm removal"))
		b.WriteString("\n")
	}
	if m.resetArmed {
		b.WriteString(styles.StyleError.Render("  Press ctrl+r again to reset to defaults"))
		b.WriteString("\n")
	}
	var actItems []styles.HelpItemResult
	if len(m.volumes) > 1 {
		actItems = append(actItems, styles.HelpItem("ctrl+d ctrl+d", "remove last"))
	}
	if m.volumesCanReset() {
		actItems = append(actItems, styles.HelpItem("ctrl+r ctrl+r", "reset"))
	}
	b.WriteString("\n")
	b.WriteString(styles.HelpBar(
		styles.HelpGroup{Label: "Manage", Items: actItems},
	))
	if len(actItems) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(styles.BackHelp(styles.HelpItem("↵", "select")))
	return b.String()
}

func (m *AddServiceModel) viewReview() string {
	svc := m.svc
	svc.Ports = m.ports
	svc.EnvVars = m.envVars
	svc.Volumes = m.volumes

	var b strings.Builder
	b.WriteString("  Review service configuration\n\n")

	panelContent := lipgloss.JoinVertical(lipgloss.Left,
		styles.StyleHeader.Render(svc.Name)+"  "+styles.StyleCode.Render(svc.FullImage()),
		"",
		m.reviewSection("Ports", func() string {
			if len(svc.Ports) == 0 {
				return "(none)"
			}
			ps := make([]string, len(svc.Ports))
			for i, p := range svc.Ports {
				ps[i] = p.String()
			}
			return strings.Join(ps, "  ")
		}()),
		m.reviewSection("Env vars", func() string {
			if len(svc.EnvVars) == 0 {
				return "(none)"
			}
			evs := make([]string, len(svc.EnvVars))
			for i, ev := range svc.EnvVars {
				evs[i] = ev.Key + "=" + ev.Value
			}
			return strings.Join(evs, "  ")
		}()),
		m.reviewSection("Volumes", func() string {
			if len(svc.Volumes) == 0 {
				return "(none)"
			}
			vs := make([]string, len(svc.Volumes))
			for i, v := range svc.Volumes {
				vs[i] = v.Source + "→" + v.Target
			}
			return strings.Join(vs, "  ")
		}()),
		m.reviewSection("Environment", m.envName),
	)
	panelW := lipgloss.Width(panelContent) + 4
	if panelW < 60 {
		panelW = 60
	}
	b.WriteString(styles.StylePanel.Width(panelW).Render(panelContent))

	b.WriteString("\n\n")
	button := styles.StyleButton.Render(" Save service ")
	b.WriteString(lipgloss.PlaceHorizontal(panelW, lipgloss.Center, button) + "\n")

	b.WriteString("\n")
	b.WriteString(styles.BackHelp(styles.HelpItem("↵", "save")))
	return b.String()
}

func (m *AddServiceModel) reviewSection(label, value string) string {
	return styles.StyleInputLabel.Render(label) + "  " + styles.StyleSubtitle.Render(value)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
