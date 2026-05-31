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
)

type AddServiceModel struct {
	cfg         *config.Config
	profile     *config.Profile
	envName     string
	step        addSvcStep

	// source selection
	sourceCursor int
	sourcePath    string // "catalog" | "search" | "manual"

	// catalog
	catalogCursor int

	// hub search
	searchInput   textinput.Model
	searchResults []hub.SearchResult
	searchCursor  int
	searching     bool
	searchErr     string

	// tag selection
	tags       []hub.Tag
	tagCursor  int
	loadingTags bool

	// service being built
	svc        config.Service
	nameInput  textinput.Model

	// ports editor
	portHost          textinput.Model
	portContainer     textinput.Model
	ports             []config.PortMapping
	portsDeleteArmed  bool

	// env vars editor
	envKey             textinput.Model
	envVal             textinput.Model
	envFocus           int // 0=key, 1=val
	envVars            []config.EnvVar
	envVarsDeleteArmed bool
	resetArmed         bool
	protectedEnvKeys   map[string]bool // env var keys from catalog defaults
	defaultPorts      []config.PortMapping   // snapshot for reset
	defaultEnvVars    []config.EnvVar        // snapshot for reset

	// volumes editor
	volSource          textinput.Model
	volNamed           bool
	volumes            []config.VolumeMapping
	volumesDeleteArmed bool
	defaultVolumes     []config.VolumeMapping // snapshot for reset on re-entry

	err string
}

func NewAddService(cfg *config.Config, p *config.Profile, envName string) *AddServiceModel {
	si := textinput.New()
	si.Placeholder = "postgres, redis, my-image..."
	si.CharLimit = 60

	ni := textinput.New()
	ni.Placeholder = "postgres"
	ni.CharLimit = 40

	ph := textinput.New(); ph.Placeholder = "5432"; ph.CharLimit = 10; ph.Width = 8
	pc := textinput.New(); pc.Placeholder = "5432"; pc.CharLimit = 10; pc.Width = 8

	ek := textinput.New(); ek.Placeholder = "POSTGRES_USER"; ek.CharLimit = 60; ek.Width = 24
	ev := textinput.New(); ev.Placeholder = "postgres";     ev.CharLimit = 100; ev.Width = 28

	vs := textinput.New(); vs.Placeholder = "./data or volume name"; vs.CharLimit = 80; vs.Width = 30
	return &AddServiceModel{
		cfg:              cfg,
		profile:          p,
		envName:          envName,
		step:             svcStepSource,
		searchInput:      si,
		nameInput:        ni,
		portHost:         ph,
		portContainer:    pc,
		envKey:           ek,
		envVal:           ev,
		volSource:        vs,
		protectedEnvKeys: make(map[string]bool),
	}
}

func (m *AddServiceModel) ProfileName() string { return m.profile.Name }

// refocusInputs blurs all text inputs and focuses the ones relevant
// to the given step. Call this when navigating BACK to a step so
// keyboard input goes to the right field.
func (m *AddServiceModel) resetServiceData() {
	m.ports = nil
	m.envVars = nil
	m.volumes = nil
	m.defaultPorts = nil
	m.defaultEnvVars = nil
	m.defaultVolumes = nil
	m.protectedEnvKeys = make(map[string]bool)
	m.svc = config.Service{}
}

func (m *AddServiceModel) refocusInputs(step addSvcStep) tea.Cmd {
	m.searchInput.Blur()
	m.nameInput.Blur()
	m.portHost.Blur()
	m.portContainer.Blur()
	m.envKey.Blur()
	m.envVal.Blur()
	m.volSource.Blur()

	switch step {
	case svcStepSearch:
		m.searchInput.Focus()
		return textinput.Blink
	case svcStepName:
		m.nameInput.Focus()
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
var addSvcUp = key.NewBinding(key.WithKeys("up", "k"))
var addSvcDown = key.NewBinding(key.WithKeys("down", "j"))
var addSvcTab = key.NewBinding(key.WithKeys("tab"))
var addSvcToggle = key.NewBinding(key.WithKeys("t"))
var addSvcDelete = key.NewBinding(key.WithKeys("d"))
var addSvcReset = key.NewBinding(key.WithKeys("r"))

func (m *AddServiceModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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
			return NavigateTo{Screen: NavProfileView, ProfileName: m.profile.Name}
		}

	case tea.KeyMsg:
		if key.Matches(msg, addSvcBack) {
			if m.step == svcStepSource {
				return m, func() tea.Msg {
					return NavigateTo{Screen: NavProfileView, ProfileName: m.profile.Name}
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
			case svcStepName:
				if m.sourcePath == "manual" {
					m.resetServiceData()
					m.step = svcStepSource
					m.sourcePath = ""
					return m, nil
				}
				m.step--
				return m, m.refocusInputs(m.step)
			default:
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
		if key.Matches(msg, addSvcNext) {
			m.svc.Name = strings.TrimSpace(m.nameInput.Value())
			if m.svc.Name == "" {
				m.err = "service name required"
				return m, nil
			}
			m.step = svcStepPorts
			m.nameInput.Blur()
			m.portHost.Focus()
			return m, textinput.Blink
		}
	case svcStepPorts:
		return m.handlePorts(msg)
	case svcStepEnvVars:
		return m.handleEnvVars(msg)
	case svcStepVolumes:
		return m.handleVolumes(msg)
	case svcStepReview:
		if key.Matches(msg, addSvcNext) {
			return m, m.saveService()
		}
	}
	return m.updateActiveInput(msg)
}

func (m *AddServiceModel) handleSource(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	maxIdx := 0 // only Manual
	if len(hub.Catalog) > 0 {
		maxIdx = 2 // Catalog, Search, Manual
	}
	switch {
	case key.Matches(msg, addSvcUp) && m.sourceCursor > 0:
		m.sourceCursor--
	case key.Matches(msg, addSvcDown) && m.sourceCursor < maxIdx:
		m.sourceCursor++
	case key.Matches(msg, addSvcNext):
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
				m.nameInput.Focus()
				return m, textinput.Blink
			}
		} else {
			m.sourcePath = "manual"
			m.step = svcStepName
			m.nameInput.Focus()
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
		// prefill volume
		if entry.DefaultVolumeTarget != "" {
			m.volumes = append(m.volumes, config.VolumeMapping{
				Source: "./data",
				Target: entry.DefaultVolumeTarget,
				Named:  false,
			})
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
		m.svc.Name = entry.Image
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
		if len(m.searchResults) == 0 && query != "" {
			// first enter = search
			m.searching = true
			return m, func() tea.Msg {
				results, err := hub.Search(query, 15)
				errStr := ""
				if err != nil {
					errStr = err.Error()
				}
				return hubSearchResultMsg{results: results, err: errStr}
			}
		} else if len(m.searchResults) > 0 {
			// second enter on a result = select it
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
	case key.Matches(msg, addSvcTab):
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
			m.portHost.SetValue("")
			m.portContainer.SetValue("")
			m.portHost.Focus()
			return m, nil
		}
		// Empty inputs → advance to next step
		m.portsDeleteArmed = false
		m.svc.Ports = m.ports
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
	case key.Matches(msg, addSvcTab):
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
			m.envKey.SetValue("")
			m.envVal.SetValue("")
			m.envKey.Focus()
			m.envFocus = 0
			return m, nil
		}
		// Empty key → advance to next step
		m.envVarsDeleteArmed = false
		m.svc.EnvVars = m.envVars
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
	case key.Matches(msg, addSvcTab):
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
			m.volSource.SetValue("")
			m.volSource.Focus()
			return m, nil
		}
		// Empty source → advance to next step
		m.volumesDeleteArmed = false
		m.svc.Volumes = m.volumes
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

func (m *AddServiceModel) saveService() tea.Cmd {
	return func() tea.Msg {
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

func (m *AddServiceModel) updateActiveInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.step {
	case svcStepSearch:
		m.searchInput, cmd = m.searchInput.Update(msg)
	case svcStepName:
		m.nameInput, cmd = m.nameInput.Update(msg)
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
	case svcStepReview:
		b.WriteString(m.viewReview())
	}

	if m.err != "" {
		b.WriteString("\n" + styles.StyleError.Render("  ✗ "+m.err))
	}
	b.WriteString("\n\n")
	b.WriteString(styles.StyleHelp.Render(styles.HelpItem("esc", "back")))
	return b.String()
}

func (m *AddServiceModel) viewSource() string {
	type srcOpt struct {
		emoji string
		label string
		desc  string
	}

	var opts []srcOpt
	if len(hub.Catalog) > 0 {
		opts = []srcOpt{
			{emoji: "📦", label: "Curated catalog", desc: "postgres, mongo, redis, kafka…"},
			{emoji: "🔍", label: "Search Docker Hub", desc: "any public image"},
			{emoji: "✏️", label: "Manual entry", desc: "type image name directly"},
		}
	} else {
		opts = []srcOpt{
			{emoji: "✏️", label: "Manual entry", desc: "type image name directly"},
		}
	}

	// Find the widest label for description alignment
	maxW := 0
	for _, o := range opts {
		if len(o.label) > maxW {
			maxW = len(o.label)
		}
	}

	// Selected style: just color+bold, NO padding/width/margin
	selStyle := lipgloss.NewStyle().
		Foreground(styles.ColorPrimary).
		Bold(true)

	var b strings.Builder
	b.WriteString("  How do you want to find the image?\n\n")
	for i, o := range opts {
		prefix := "    "
		if i == m.sourceCursor {
			prefix = "  > "
		}
		// Build emoji + separator + label, measure display width with lipgloss.
		// Use lipgloss.Width() because len() counts bytes, not terminal cells —
		// emojis like ✏️ have 6 bytes but display as 2 cells.
		content := o.emoji + "  " + o.label
		padding := (maxW + 4) - lipgloss.Width(content) // 4 = emoji(2) + separator(2)
		if padding < 0 {
			padding = 0
		}
		line := prefix + content + strings.Repeat(" ", padding) + "  (" + o.desc + ")"
		if i == m.sourceCursor {
			b.WriteString(selStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n" + styles.StyleHelp.Render(styles.HelpItem("↑↓", "select")+"   "+styles.HelpItem("↵", "confirm")))
	return b.String()
}

func (m *AddServiceModel) viewCatalog() string {
	var b strings.Builder
	b.WriteString("  Select a service from the catalog:\n\n")
	selStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)
	for i, entry := range hub.Catalog {
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
	b.WriteString("\n" + styles.StyleHelp.Render(styles.HelpItem("↑↓", "select")+"   "+styles.HelpItem("↵", "confirm")))
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
		b.WriteString(styles.StyleError.Render("  " + m.searchErr) + "\n")
	}
	selStyle := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)
	for i, r := range m.searchResults {
		official := ""
		if r.IsOfficial {
			official = " ✓"
		}
		label := fmt.Sprintf("%-32s  ★ %d%s  — %s",
			r.Name, r.StarCount, official, truncate(r.Description, 38))
		if i == m.searchCursor {
			b.WriteString(selStyle.Render("  > " + label))
		} else {
			b.WriteString("    " + label)
		}
		b.WriteString("\n")
	}
	if len(m.searchResults) == 0 && !m.searching {
		b.WriteString("  Type a name and press ↵ to search\n")
	}
	b.WriteString("\n" + styles.StyleHelp.Render(
		styles.HelpItem("↵", "search / select")+"   "+styles.HelpItem("↑↓", "navigate results")))
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
	for i, tag := range m.tags {
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
	b.WriteString("\n" + styles.StyleHelp.Render(styles.HelpItem("↑↓", "select")+"   "+styles.HelpItem("↵", "confirm")))
	return b.String()
}

func (m *AddServiceModel) viewName() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  Image: %s\n\n", styles.StyleCode.Render(m.svc.FullImage())))
	b.WriteString(styles.StyleInputLabel.Render("Service name") + "\n")
	b.WriteString("  " + m.nameInput.View() + "\n")
	b.WriteString("  Used as the compose service key and container name suffix\n")
	b.WriteString("\n" + styles.StyleHelp.Render(styles.HelpItem("↵", "next")))
	return b.String()
}

func (m *AddServiceModel) viewPorts() string {
	var b strings.Builder
	b.WriteString("  Port mappings  (host:container)\n\n")
	b.WriteString("  " + styles.StyleInputLabel.Render("Host port") + "  " + m.portHost.View() + "\n")
	b.WriteString("  " + styles.StyleInputLabel.Render("Container port") + "  " + m.portContainer.View() + "\n\n")
	for _, p := range m.ports {
		b.WriteString("    " + styles.StyleCode.Render(p.String()) + "\n")
	}
	if m.portsDeleteArmed {
		b.WriteString(styles.StyleError.Render("  Press d again to confirm removal"))
		b.WriteString("\n")
	}
	if m.resetArmed {
		b.WriteString(styles.StyleError.Render("  Press r again to reset to defaults"))
		b.WriteString("\n")
	}
	help := styles.HelpItem("tab", "switch field") + "   " +
		styles.HelpItem("↵", "next step")
	if len(m.ports) > 1 {
		help += "   " + styles.HelpItem("d d", "remove last")
	}
	if m.portsCanReset() {
		help += "   " + styles.HelpItem("r r", "reset")
	}
	b.WriteString("\n" + styles.StyleHelp.Render(help))
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
	b.WriteString("  " + styles.StyleInputLabel.Render("Key") + "  " + m.envKey.View() + "\n")
	b.WriteString("  " + styles.StyleInputLabel.Render("Value") + "  " + m.envVal.View() + "\n\n")
	for _, e := range m.envVars {
		b.WriteString("    " + styles.StyleCode.Render(e.Key) + " = " + e.Value + "\n")
	}
	if m.envVarsDeleteArmed {
		b.WriteString(styles.StyleError.Render("  Press d again to confirm removal"))
		b.WriteString("\n")
	}
	if m.resetArmed {
		b.WriteString(styles.StyleError.Render("  Press r again to reset to defaults"))
		b.WriteString("\n")
	}
	help := styles.HelpItem("tab", "switch field") + "   " +
		styles.HelpItem("↵", "next step")
	if m.envVarsRemovable() {
		help += "   " + styles.HelpItem("d d", "remove last")
	}
	if m.envVarsCanReset() {
		help += "   " + styles.HelpItem("r r", "reset")
	}
	b.WriteString("\n" + styles.StyleHelp.Render(help))
	return b.String()
}

func (m *AddServiceModel) viewVolumes() string {
	namedLabel := "bind mount"
	if m.volNamed {
		namedLabel = "named volume"
	}
	var b strings.Builder
	b.WriteString("  Volumes\n\n")
	b.WriteString("  " + styles.StyleInputLabel.Render("Source") + "  " + m.volSource.View() + "  ")
	b.WriteString(styles.StyleCode.Render(namedLabel) + "\n\n")
	for _, v := range m.volumes {
		b.WriteString(fmt.Sprintf("    %s → %s\n", v.Source, v.Target))
	}
	if m.volumesDeleteArmed {
		b.WriteString(styles.StyleError.Render("  Press d again to confirm removal"))
		b.WriteString("\n")
	}
	if m.resetArmed {
		b.WriteString(styles.StyleError.Render("  Press r again to reset to defaults"))
		b.WriteString("\n")
	}
	help := styles.HelpItem("t", "toggle type") + "   " +
		styles.HelpItem("↵", "next step")
	if len(m.volumes) > 1 {
		help += "   " + styles.HelpItem("d d", "remove last")
	}
	if m.volumesCanReset() {
		help += "   " + styles.HelpItem("r r", "reset")
	}
	b.WriteString("\n" + styles.StyleHelp.Render(help))
	return b.String()
}

func (m *AddServiceModel) viewReview() string {
	svc := m.svc
	svc.Ports = m.ports
	svc.EnvVars = m.envVars
	svc.Volumes = m.volumes

	var b strings.Builder
	b.WriteString("  Review service configuration\n\n")
	b.WriteString(styles.StylePanel.Render(
		lipgloss.JoinVertical(lipgloss.Left,
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
			m.reviewSection("Env vars", fmt.Sprintf("%d configured", len(svc.EnvVars))),
			m.reviewSection("Volumes", fmt.Sprintf("%d configured", len(svc.Volumes))),
			m.reviewSection("Environment", m.envName),
		),
	))
	b.WriteString("\n\n")
	b.WriteString(styles.StyleButton.Render(" Save service "))
	b.WriteString("   " + styles.StyleHelp.Render(styles.HelpItem("↵", "confirm")))
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
