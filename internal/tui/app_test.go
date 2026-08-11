package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/pershin-daniil/goworktree/internal/config"
)

func TestDiagnosticReportIsStableAndContextual(t *testing.T) {
	m := appModel{
		actions:     AppActions{Version: "0.1.0"},
		pendingArgs: []string{"sync", "ticket-42"},
		selected:    "ticket-42",
		err:         errors.New("git fetch origin: offline"),
		log:         []string{"syncing ticket-42", "failed api"},
	}
	report := m.diagnostic()
	for _, want := range []string{
		"goworktree diagnostic report",
		"report_version: 1",
		"app_version: 0.1.0",
		"action: sync ticket-42",
		"project: ticket-42",
		"error: git fetch origin: offline",
		"operation_log:\nsyncing ticket-42\nfailed api",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestProjectActionsHideDisabledPrograms(t *testing.T) {
	m := appModel{cfg: &config.Config{OpenWith: map[string]config.Program{
		"cursor": {Name: "Cursor", Path: "cursor", Enabled: false},
		"goland": {Name: "GoLand", Path: "goland", Enabled: true},
	}}}
	m.setProjectActions()
	ids := make(map[string]bool)
	for _, item := range m.list.Items() {
		ids[item.(appItem).id] = true
	}
	if ids["open:cursor"] {
		t.Fatal("Cursor action is shown while disabled")
	}
	if !ids["open:goland"] {
		t.Fatal("GoLand action is not shown while enabled")
	}
}

func TestSelectedIDsAreSorted(t *testing.T) {
	got := selectedIDs(map[string]bool{"z": true, "a": true, "off": false})
	if strings.Join(got, ",") != "a,z" {
		t.Fatalf("selectedIDs = %v", got)
	}
}

func TestRepositoryPickerSpaceThenEnterRunsSelectedRepository(t *testing.T) {
	m := appModel{
		actions:  AppActions{Execute: func([]string) (string, error) { return "", nil }},
		cfg:      &config.Config{Repos: map[string]config.Repo{"api": {Path: "/tmp/api"}}},
		selected: "ticket-42",
	}
	m.prepareRepoPicker("add")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(appModel)
	if !m.selectedRepos["api"] {
		t.Fatal("space did not select the focused repository")
	}
	if !strings.Contains(m.View(), "✓ api") {
		t.Fatal("repository picker does not show selected state")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(appModel)
	if m.screen != appRunning {
		t.Fatalf("screen = %v, want appRunning", m.screen)
	}
	if got, want := strings.Join(m.pendingArgs, " "), "add ticket-42 --repos api"; got != want {
		t.Fatalf("pendingArgs = %q, want %q", got, want)
	}
}

func TestProgramEditSubmitIncludesArguments(t *testing.T) {
	m := appModel{
		screen:        appProgramForm,
		programID:     "cursor",
		pendingAction: "program-edit",
		setupInputs:   []textinput.Model{textinput.New(), textinput.New(), textinput.New()},
		setupFocus:    2,
	}
	m.setupInputs[0].SetValue("Cursor")
	m.setupInputs[1].SetValue("cursor")
	m.setupInputs[2].SetValue("--reuse-window")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(appModel)
	if got.screen != appRunning {
		t.Fatalf("screen = %v, want appRunning", got.screen)
	}
	if want := []string{"programs", "update", "cursor", "--name", "Cursor", "--path", "cursor", "--args", "--reuse-window"}; strings.Join(got.pendingArgs, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("pendingArgs = %v, want %v", got.pendingArgs, want)
	}
}
