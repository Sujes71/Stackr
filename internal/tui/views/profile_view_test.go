package views

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stackr/stackr/internal/config"
)

func TestRenderServices_LongWorkDirDoesNotWrap(t *testing.T) {
	longDir := "/Users/zedven/IdeaProjects/cc.globick.oneticket.backend.oneticket-web"
	env := &config.Environment{
		Name: "local",
		LocalProcesses: []config.LocalProcess{{
			Name:    "portal",
			Command: "pnpm dev",
			WorkDir: longDir,
		}},
	}
	m := &ProfileViewModel{
		width:   80,
		height:  40,
		profile: &config.Profile{Name: "test", Environments: []config.Environment{*env}},
	}

	out := m.renderServices(env)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines (2 borders + 3 content), got %d:\n%s", len(lines), out)
	}

	for i, line := range lines {
		if w := lipgloss.Width(line); w > m.cardWidth()+2 {
			t.Errorf("line %d width %d exceeds card width %d: %q", i, w, m.cardWidth()+2, line)
		}
	}

	if !strings.Contains(out, "oneticket-web") {
		t.Errorf("expected workdir tail to remain visible:\n%s", out)
	}
}

func TestRenderServices_LongServiceFieldsDoNotWrap(t *testing.T) {
	env := &config.Environment{
		Name: "local",
		Services: []config.Service{{
			Name:  "a-very-long-service-name-that-would-overflow-the-card",
			Image: strings.Repeat("registry.example.com/team/", 4) + "postgres",
			Tag:   "16",
			Ports: []config.PortMapping{{Host: "5432", Container: "5432"}},
		}},
	}
	m := &ProfileViewModel{
		width:   80,
		height:  40,
		profile: &config.Profile{Name: "test", Environments: []config.Environment{*env}},
	}

	out := m.renderServices(env)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines (2 borders + name + image + port), got %d:\n%s", len(lines), out)
	}

	for i, line := range lines {
		if w := lipgloss.Width(line); w > m.cardWidth()+2 {
			t.Errorf("line %d width %d exceeds card width %d: %q", i, w, m.cardWidth()+2, line)
		}
	}
}
