package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	gitops "github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectworks"
)

func TestWorkAppNavigatesHomeWorkAndRepository(t *testing.T) {
	t.Parallel()

	snapshot := testWorksSnapshot("ticket-42")
	model := newWorkAppModel(WorkAppActions{Load: func(context.Context) (inspectworks.Snapshot, error) {
		return snapshot, nil
	}}, context.Background(), nil)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(workAppModel)
	updated, _ = model.Update(worksLoadedMsg{generation: 1, snapshot: snapshot})
	model = updated.(workAppModel)
	if model.screen != workHome || !strings.Contains(model.View(), "1 Work") || !strings.Contains(model.View(), "ticket-42") {
		t.Fatalf("home view:\n%s", model.View())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	model = updated.(workAppModel)
	if model.screen != workOverview || !strings.Contains(model.View(), "Work · ticket-42") || !strings.Contains(model.View(), "api") {
		t.Fatalf("Work view:\n%s", model.View())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	if model.screen != workRepository {
		t.Fatalf("screen = %v, want repository", model.screen)
	}
	view := model.View()
	for _, wanted := range []string{"Repository · api", "Expected branch:", "ticket-42", "Working tree:", "staged 1"} {
		if !strings.Contains(view, wanted) {
			t.Fatalf("repository view missing %q:\n%s", wanted, view)
		}
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	model = updated.(workAppModel)
	if model.screen != workOverview {
		t.Fatalf("h returned to screen %v", model.screen)
	}
}

func TestWorkAppRefreshPreservesRepositoryContext(t *testing.T) {
	t.Parallel()

	snapshot := testWorksSnapshot("ticket-42")
	model := newWorkAppModel(WorkAppActions{Load: func(context.Context) (inspectworks.Snapshot, error) {
		return snapshot, nil
	}}, context.Background(), nil)
	model.snapshot = snapshot
	model.setHome("ticket-42")
	model.activateListItem()
	model.activateListItem()
	if model.screen != workRepository {
		t.Fatalf("setup screen = %v", model.screen)
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	model = updated.(workAppModel)
	if model.screen != workLoading || cmd == nil {
		t.Fatalf("refresh did not enter loading: screen=%v cmd=%v", model.screen, cmd)
	}
	loaded := cmd().(worksLoadedMsg)
	updated, _ = model.Update(loaded)
	model = updated.(workAppModel)
	if model.screen != workRepository || model.selectedWorkName != "ticket-42" || model.selectedRepoID != "api" {
		t.Fatalf("refresh context = screen:%v Work:%q repo:%q", model.screen, model.selectedWorkName, model.selectedRepoID)
	}
}

func TestWorkAppEnforcesMinimumTerminalSize(t *testing.T) {
	t.Parallel()

	model := newWorkAppModel(WorkAppActions{Load: func(context.Context) (inspectworks.Snapshot, error) {
		return inspectworks.Snapshot{}, nil
	}}, context.Background(), nil)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 79, Height: 23})
	view := updated.(workAppModel).View()
	if !strings.Contains(view, "requires at least 80×24") || !strings.Contains(view, "79×23") {
		t.Fatalf("small terminal view:\n%s", view)
	}
}

func TestWorkAppShowsUnknownAsUnknown(t *testing.T) {
	t.Parallel()

	repository := inspectwork.RepositorySnapshot{
		ID: "api", Intent: work.RepositoryIntent{Destination: "/works/ticket/api", BranchRef: "refs/heads/ticket"},
	}
	formatted := formatRepository(repository)
	if count := strings.Count(formatted, "unknown"); count < 5 {
		t.Fatalf("unknown facts were collapsed:\n%s", formatted)
	}
}

func TestWorkAppAllowsQAndRInSearchInput(t *testing.T) {
	t.Parallel()

	snapshot := testWorksSnapshot("ticket-42")
	model := newWorkAppModel(WorkAppActions{Load: func(context.Context) (inspectworks.Snapshot, error) {
		return snapshot, nil
	}}, context.Background(), nil)
	model.snapshot = snapshot
	model.setHome("")

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	model = updated.(workAppModel)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	model = updated.(workAppModel)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	model = updated.(workAppModel)
	if model.quitting || model.screen != workHome || model.list.FilterState() != list.Filtering {
		t.Fatalf("search input changed navigation: quitting=%v screen=%v filter=%v", model.quitting, model.screen, model.list.FilterState())
	}
}

func TestWorkAppSanitizesTerminalControlSequences(t *testing.T) {
	t.Parallel()

	got := terminalSafe("bad\x1b[31m\rpath\nnext", true)
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\r') || !strings.Contains(got, "\nnext") {
		t.Fatalf("terminalSafe = %q", got)
	}
}

func testWorksSnapshot(name string) inspectworks.Snapshot {
	workName, _ := work.ParseName(name)
	repository := inspectwork.RepositorySnapshot{
		ID:            "api",
		Intent:        work.RepositoryIntent{ID: "api", SourcePath: "/repos/api", Destination: "/works/" + name + "/api", BranchRef: "refs/heads/" + name},
		SourceKnown:   true,
		Source:        inspectwork.RepositoryIdentity{SourcePath: "/repos/api"},
		CheckoutKnown: true,
		Checkout: inspectwork.CheckoutSnapshot{
			FullRef: "refs/heads/" + name, HeadOID: strings.Repeat("a", 40),
		},
		WorkingTreeKnown:   true,
		WorkingTree:        gitops.WorkingTreeStatus{Staged: 1},
		RegistrationsKnown: true,
		GitOperationsKnown: true,
	}
	observed := inspectwork.Snapshot{
		InspectedAt: time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC),
		WorkName:    workName, WorkRoot: "/works/" + name,
		Manifest:     inspectwork.ManifestSnapshot{State: inspectwork.MetadataValid},
		Operation:    inspectwork.OperationSnapshot{State: inspectwork.MetadataValid, Phase: "created"},
		IntentSource: inspectwork.IntentManifest,
		Repositories: []inspectwork.RepositorySnapshot{repository},
	}
	return inspectworks.Snapshot{
		InspectedAt: observed.InspectedAt,
		WorksRoot:   "/works",
		Works:       []inspectworks.Work{{Name: name, RootPath: observed.WorkRoot, FromDirectory: true, Snapshot: &observed}},
	}
}
