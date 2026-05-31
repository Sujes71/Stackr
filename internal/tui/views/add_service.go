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
	portHost      textinput.Model
	portContainer textinput.Model
	ports         []config.PortMapping

	// env vars editor
	envKey   textinput.Model
	envVal   textinput.Model
	envFocus int // 0=key, 1=val
	envVars  []config.EnvVar

	// volumes editor
	volSource textinput.Model
	volTarget textinput.Model
	volNamed  bool
	volumes   []config.VolumeMapping

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
	vt := textinput.New(); vt.Placeholder = "/var/lib/postgresql/data"; vt.CharLimit = 80; vt.Width = 34

	return &AddServiceModel{
		cfg:           cfg,
		profile:       p,
		envName:       envName,
		step:          svcStepSource,
		searchInput:   si,
		nameInput:     ni,
		portHost:      ph,
		portContainer: pc,
		envKey:        ek,
		envVal:        ev,
		volSource:     vs,
		volTarget:     vt,
	}
}

func (m *AddServiceModel) ProfileName() string { return m.profile.Name }

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
				m.step = svcStepSource
				m.sourcePath = ""
				return m, nil
			case svcStepSelectTag:
				m.step = svcStepSource
				m.sourcePath = ""
				return m, nil
			case svcStepName:
				if m.sourcePath == "manual" {
					// Manual: Source → Name, go back to Source
					m.step = svcStepSource
					m.sourcePath = ""
					return m, nil
				}
				// Catalog/Search → Tag Select → Name, go back one
				m.step--
				return m, nil
			default:
				m.step--
				return m, nil
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
		}
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
		// commit pending port if any
		h := strings.TrimSpace(m.portHost.Value())
		c := strings.TrimSpace(m.portContainer.Value())
		if h != "" && c != "" {
			m.ports = append(m.ports, config.PortMapping{Host: h, Container: c})
		}
		m.portHost.Blur()
		m.portContainer.Blur()
		m.svc.Ports = m.ports
		m.step = svcStepEnvVars
		m.envKey.Focus()
		return m, textinput.Blink
	}
	return m.updateActiveInput(msg)
}

func (m *AddServiceModel) handleEnvVars(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, addSvcTab):
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
			m.envVars = append(m.envVars, config.EnvVar{Key: k, Value: v})
		}
		m.envKey.Blur()
		m.envVal.Blur()
		m.svc.EnvVars = m.envVars
		m.step = svcStepVolumes
		m.volSource.Focus()
		return m, textinput.Blink
	}
	return m.updateActiveInput(msg)
}

func (m *AddServiceModel) handleVolumes(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, addSvcTab):
		if m.volSource.Focused() {
			m.volSource.Blur()
			m.volTarget.Focus()
		} else {
			m.volTarget.Blur()
			m.volSource.Focus()
		}
		return m, textinput.Blink
	case key.Matches(msg, addSvcToggle):
		m.volNamed = !m.volNamed
	case key.Matches(msg, addSvcNext):
		src := strings.TrimSpace(m.volSource.Value())
		tgt := strings.TrimSpace(m.volTarget.Value())
		if tgt != "" {
			m.volumes = append(m.volumes, config.VolumeMapping{
				Source: src,
				Target: tgt,
				Named:  m.volNamed,
			})
		}
		m.svc.Volumes = m.volumes
		m.step = svcStepReview
	}
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
		if m.volSource.Focused() {
			m.volSource, cmd = m.volSource.Update(msg)
		} else {
			m.volTarget, cmd = m.volTarget.Update(msg)
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
		// Use a simple 4-char prefix: "  > " for selected, "    " for others
		// This keeps ALL lines at the same indentation without relying on
		// lipgloss padding or alignment.
		prefix := "    "
		if i == m.sourceCursor {
			prefix = "  > "
		}
		line := prefix + o.emoji + "  " + o.label
		for len(line)-len(prefix) < maxW+4+2 { // align descriptions
			line += " "
		}
		line += " (" + o.desc + ")"
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
	b.WriteString(styles.StyleSubtitle.Render("  Search Docker Hub:\n\n"))
	b.WriteString("  " + m.searchInput.View() + "  ")
	if m.searching {
		b.WriteString(styles.StyleSubtitle.Render("searching…"))
	}
	b.WriteString("\n\n")
	if m.searchErr != "" {
		b.WriteString(styles.StyleError.Render("  " + m.searchErr) + "\n")
	}
	for i, r := range m.searchResults {
		official := ""
		if r.IsOfficial {
			official = " ✓"
		}
		label := fmt.Sprintf("%-32s  ★ %d%s  — %s",
			r.Name, r.StarCount, official, truncate(r.Description, 38))
		if i == m.searchCursor {
			b.WriteString(styles.StyleListItemSelected.Render(label))
		} else {
			b.WriteString(styles.StyleListItem.Render(label))
		}
		b.WriteString("\n")
	}
	if len(m.searchResults) == 0 && !m.searching {
		b.WriteString(styles.StyleSubtitle.Render("  Type a name and press ↵ to search\n"))
	}
	b.WriteString("\n" + styles.StyleHelp.Render(
		styles.HelpItem("↵", "search / select")+"   "+styles.HelpItem("↑↓", "navigate results")))
	return b.String()
}

func (m *AddServiceModel) viewTags() string {
	var b strings.Builder
	b.WriteString(styles.StyleSubtitle.Render(fmt.Sprintf("  Select tag for %s:\n\n", m.svc.Image)))
	if m.loadingTags {
		b.WriteString(styles.StyleSubtitle.Render("  Loading tags from Docker Hub…\n"))
		return b.String()
	}
	for i, tag := range m.tags {
		size := ""
		if tag.FullSize > 0 {
			size = fmt.Sprintf("  %.0f MB", float64(tag.FullSize)/1_000_000)
		}
		label := fmt.Sprintf("%-24s%s", tag.Name, size)
		if i == m.tagCursor {
			b.WriteString(styles.StyleListItemSelected.Render(label))
		} else {
			b.WriteString(styles.StyleListItem.Render(label))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n" + styles.StyleHelp.Render(styles.HelpItem("↑↓", "select")+"   "+styles.HelpItem("↵", "confirm")))
	return b.String()
}

func (m *AddServiceModel) viewName() string {
	var b strings.Builder
	b.WriteString(styles.StyleSubtitle.Render(
		fmt.Sprintf("  Image: %s\n\n", styles.StyleCode.Render(m.svc.FullImage()))))
	b.WriteString(styles.StyleInputLabel.Render("Service name") + "\n")
	b.WriteString("  " + m.nameInput.View() + "\n")
	b.WriteString(styles.StyleSubtitle.Render("  Used as the compose service key and container name suffix\n"))
	b.WriteString("\n" + styles.StyleHelp.Render(styles.HelpItem("↵", "next")))
	return b.String()
}

func (m *AddServiceModel) viewPorts() string {
	var b strings.Builder
	b.WriteString(styles.StyleSubtitle.Render("  Port mappings  (host:container)\n\n"))
	b.WriteString("  " + styles.StyleInputLabel.Render("Host port") + "  " + m.portHost.View() + "\n")
	b.WriteString("  " + styles.StyleInputLabel.Render("Container port") + "  " + m.portContainer.View() + "\n\n")
	for _, p := range m.ports {
		b.WriteString(styles.StyleListItem.Render("  " + styles.StyleCode.Render(p.String()) + "\n"))
	}
	b.WriteString("\n" + styles.StyleHelp.Render(
		styles.HelpItem("tab", "switch field")+"   "+
			styles.HelpItem("↵", "next step")))
	return b.String()
}

func (m *AddServiceModel) viewEnvVars() string {
	var b strings.Builder
	b.WriteString(styles.StyleSubtitle.Render("  Environment variables\n\n"))
	b.WriteString("  " + styles.StyleInputLabel.Render("Key") + "  " + m.envKey.View() + "\n")
	b.WriteString("  " + styles.StyleInputLabel.Render("Value") + "  " + m.envVal.View() + "\n\n")
	for _, e := range m.envVars {
		b.WriteString(styles.StyleListItem.Render(
			"  " + styles.StyleCode.Render(e.Key) + " = " + e.Value) + "\n")
	}
	b.WriteString("\n" + styles.StyleHelp.Render(
		styles.HelpItem("tab", "switch field")+"   "+
			styles.HelpItem("↵", "next step")))
	return b.String()
}

func (m *AddServiceModel) viewVolumes() string {
	namedLabel := "bind mount"
	if m.volNamed {
		namedLabel = "named volume"
	}
	var b strings.Builder
	b.WriteString(styles.StyleSubtitle.Render("  Volumes\n\n"))
	b.WriteString("  " + styles.StyleInputLabel.Render("Source") + "  " + m.volSource.View() + "\n")
	b.WriteString("  " + styles.StyleInputLabel.Render("Target") + "  " + m.volTarget.View() + "\n")
	b.WriteString("  Type: " + styles.StyleCode.Render(namedLabel) + "\n\n")
	for _, v := range m.volumes {
		b.WriteString(styles.StyleListItem.Render(
			fmt.Sprintf("  %s → %s", v.Source, v.Target)) + "\n")
	}
	b.WriteString("\n" + styles.StyleHelp.Render(
		styles.HelpItem("tab", "switch field")+"   "+
			styles.HelpItem("t", "toggle type")+"   "+
			styles.HelpItem("↵", "next step")))
	return b.String()
}

func (m *AddServiceModel) viewReview() string {
	svc := m.svc
	svc.Ports = m.ports
	svc.EnvVars = m.envVars
	svc.Volumes = m.volumes

	var b strings.Builder
	b.WriteString(styles.StyleSubtitle.Render("  Review service configuration\n\n"))
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
