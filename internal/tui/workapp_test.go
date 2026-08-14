package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	gitops "github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectworks"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/syncwork"
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

func TestWorkAppProblemViewFitsTerminalAndReturnsToWork(t *testing.T) {
	t.Parallel()

	snapshot := testWorksSnapshot("ticket-42")
	snapshot.Works[0].Snapshot.Problems = []inspectwork.Problem{{
		Code:    inspectwork.ProblemOperationMissing,
		Message: "New Work operation record is missing",
		Path:    "/config/operations/new-work/ticket-42.json",
		Next:    inspectwork.ActionRepairWork,
	}}
	model := newWorkAppModel(WorkAppActions{Load: func(context.Context) (inspectworks.Snapshot, error) {
		return snapshot, nil
	}}, context.Background(), nil)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: minimumWorkAppWidth, Height: minimumWorkAppHeight})
	model = updated.(workAppModel)
	updated, _ = model.Update(worksLoadedMsg{generation: 1, snapshot: snapshot})
	model = updated.(workAppModel)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	model = updated.(workAppModel)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	if model.screen != workProblem {
		t.Fatalf("screen = %v, want problem", model.screen)
	}
	view := model.View()
	if got := lipgloss.Height(view); got >= model.height {
		t.Fatalf("problem view height = %d leaves no terminal row for stable rendering at height %d:\n%s", got, model.height, view)
	}
	if !strings.Contains(view, "Read-only problem details") || !strings.Contains(view, "h/esc back") || !strings.Contains(view, "q quit") {
		t.Fatalf("problem view does not expose navigation:\n%s", view)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(workAppModel)
	if model.screen != workOverview {
		t.Fatalf("esc returned to screen %v", model.screen)
	}
}

func TestWorkAppActionPaletteOpensWorkInConfiguredProgram(t *testing.T) {
	t.Parallel()

	snapshot := testWorksSnapshot("ticket-42")
	var request WorkOpenRequest
	model := newWorkAppModel(WorkAppActions{
		Load:     func(context.Context) (inspectworks.Snapshot, error) { return snapshot, nil },
		Programs: []WorkProgramOption{{ID: "cursor", Name: "Cursor", Default: true}},
		OpenWork: func(_ context.Context, value WorkOpenRequest) error {
			request = value
			return nil
		},
	}, context.Background(), nil)
	model.snapshot = snapshot
	model.setHome("")
	model.activateListItem()

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(":")})
	model = updated.(workAppModel)
	if model.screen != workActions {
		t.Fatalf("screen = %v, want actions", model.screen)
	}
	view := model.View()
	for _, wanted := range []string{"Open Work · Cursor", "Sync Work", "unavailable", "Remove Work"} {
		if !strings.Contains(view, wanted) {
			t.Fatalf("action palette missing %q:\n%s", wanted, view)
		}
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	if model.screen != workOperation || cmd == nil {
		t.Fatalf("Open Work did not start: screen=%v cmd=%v", model.screen, cmd)
	}
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	if model.screen != workActionResult || !strings.Contains(model.View(), "Work opened") {
		t.Fatalf("Open Work result:\n%s", model.View())
	}
	if request.WorkName != "ticket-42" || request.WorkRoot != "/works/ticket-42" || request.Program != "cursor" {
		t.Fatalf("Open Work request = %+v", request)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	if model.screen != workOverview {
		t.Fatalf("Open Work result returned to screen %v", model.screen)
	}
}

func TestWorkAppCompletesNewWorkInputPlanAndExecutionFlow(t *testing.T) {
	t.Parallel()

	snapshot := testWorksSnapshot("existing-work")
	workName, _ := work.ParseName("ticket-99")
	plan := newwork.Plan{
		WorkName: workName, WorkRoot: "/works/ticket-99", Mode: newwork.ModeOffline, NoRemoteMutation: true,
		Repositories: []newwork.RepositoryPlan{{
			ID: "api", BaseRef: "refs/heads/main", BaseOID: strings.Repeat("b", 40),
			TargetBranchRef: "refs/heads/ticket-99", Destination: "/works/ticket-99/api", IncludeInGoWork: true,
		}},
	}
	var plannedRequest newwork.Request
	var executedPlan newwork.Plan
	model := newWorkAppModel(WorkAppActions{
		Load:         func(context.Context) (inspectworks.Snapshot, error) { return snapshot, nil },
		Repositories: []WorkRepositoryOption{{ID: "api", Name: "API", Path: "/repos/api"}},
		PlanNewWork: func(_ context.Context, request newwork.Request) (newwork.Plan, error) {
			plannedRequest = request
			return plan, nil
		},
		CreateNewWork: func(_ context.Context, value newwork.Plan) (newwork.ExecutionResult, error) {
			executedPlan = value
			return newwork.ExecutionResult{
				Status: newwork.ExecutionCreated, WorkRoot: value.WorkRoot,
				VerifiedRepositories: []string{"api"},
			}, nil
		},
	}, context.Background(), nil)
	model.snapshot = snapshot
	model.setHome("")

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	model = updated.(workAppModel)
	if model.screen != workNewName {
		t.Fatalf("n opened screen %v", model.screen)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ticket-99")})
	model = updated.(workAppModel)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	if model.screen != workNewRepositories {
		t.Fatalf("name continued to screen %v", model.screen)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = updated.(workAppModel)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	model = updated.(workAppModel)
	if model.newWorkMode != newwork.ModeOffline {
		t.Fatalf("mode = %s, want offline", model.newWorkMode)
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	if model.screen != workOperation || cmd == nil {
		t.Fatalf("planning did not start: screen=%v cmd=%v", model.screen, cmd)
	}
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	if model.screen != workNewPlan || !strings.Contains(model.View(), "Remote mutation") || !strings.Contains(model.View(), "refs/heads/ticket-99") {
		t.Fatalf("plan view:\n%s", model.View())
	}
	if plannedRequest.Name != "ticket-99" || plannedRequest.Mode != newwork.ModeOffline || strings.Join(plannedRequest.RepositoryIDs, ",") != "api" {
		t.Fatalf("planning request = %+v", plannedRequest)
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	if model.screen != workOperation || cmd == nil {
		t.Fatalf("creation did not start: screen=%v cmd=%v", model.screen, cmd)
	}
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	if model.screen != workActionResult || !strings.Contains(model.View(), "Work created") || !strings.Contains(model.View(), "api") {
		t.Fatalf("creation result:\n%s", model.View())
	}
	if executedPlan.WorkName.String() != "ticket-99" {
		t.Fatalf("executed plan = %+v", executedPlan)
	}
}

func TestWorkAppCtrlCCancelsOperationWithoutQuitting(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	model := newWorkAppModel(WorkAppActions{}, ctx, cancel)
	operationCtx, operationCancel := context.WithCancel(ctx)
	model.screen = workOperation
	model.operationCancel = operationCancel
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	model = updated.(workAppModel)
	if cmd != nil || model.quitting || model.screen != workOperation || operationCtx.Err() != nil {
		t.Fatalf("q changed running operation: cmd=%v quitting=%v screen=%v err=%v", cmd, model.quitting, model.screen, operationCtx.Err())
	}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(workAppModel)
	if cmd != nil || model.quitting || model.screen != workOperation {
		t.Fatalf("Ctrl+C quit operation: cmd=%v quitting=%v screen=%v", cmd, model.quitting, model.screen)
	}
	if operationCtx.Err() != context.Canceled || !strings.Contains(model.operationMessage, "Cancellation requested") {
		t.Fatalf("operation cancellation = %v, message=%q", operationCtx.Err(), model.operationMessage)
	}
}

func TestWorkAppOpensConfiguredResolverAndRetriesRecordedSyncConflict(t *testing.T) {
	t.Parallel()

	var opened SyncConflictOpenRequest
	plan := syncwork.Plan{WorkName: "ticket-42", WorkID: "id"}
	model := newWorkAppModel(WorkAppActions{
		OpenConflict: func(_ context.Context, request SyncConflictOpenRequest) (string, error) {
			opened = request
			return "GoLand", nil
		},
		PlanSyncWork: func(_ context.Context, name string) (syncwork.Plan, error) {
			if name != "ticket-42" {
				t.Fatalf("planned Work = %q", name)
			}
			return plan, nil
		},
	}, context.Background(), nil)
	model.selectedWorkName = "ticket-42"
	model.operationID = 1
	model.operationKind = "run-sync-work"
	result := syncwork.Result{WorkName: "ticket-42", Repositories: []syncwork.RepositoryResult{{
		ID: "api", Destination: "/works/ticket-42/api", Status: gitops.SyncConflict,
	}}}

	updated, cmd := model.Update(syncWorkCompletedMsg{generation: 1, result: result})
	model = updated.(workAppModel)
	if cmd == nil || model.screen != workActionResult || model.syncConflictPath == "" {
		t.Fatalf("conflict result did not start resolver: screen=%v path=%q cmd=%v", model.screen, model.syncConflictPath, cmd)
	}
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	if opened.RepositoryPath != "/works/ticket-42/api" || opened.RepositoryID != "api" || opened.WorkName != "ticket-42" {
		t.Fatalf("open request = %+v", opened)
	}
	view := model.View()
	for _, wanted := range []string{"GoLand opened", "git add", "press s to retry Sync", "o open resolver", "s retry Sync"} {
		if !strings.Contains(view, wanted) {
			t.Fatalf("conflict view missing %q:\n%s", wanted, view)
		}
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	model = updated.(workAppModel)
	if cmd == nil || model.screen != workOperation || model.operationKind != "plan-sync-work" {
		t.Fatalf("retry did not re-plan Sync: screen=%v kind=%q cmd=%v", model.screen, model.operationKind, cmd)
	}
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	if model.screen != workSyncPlan || model.syncWorkPlan.WorkName != "ticket-42" {
		t.Fatalf("retry plan = %+v; screen=%v", model.syncWorkPlan, model.screen)
	}
}

func TestWorkAppConfiguresConflictResolverFromActionPalette(t *testing.T) {
	t.Parallel()

	var saved string
	model := newWorkAppModel(WorkAppActions{
		Programs: []WorkProgramOption{{ID: "codex", Name: "Codex"}, {ID: "goland", Name: "GoLand", Default: true}},
		SetConflictProgram: func(_ context.Context, programID string) error {
			saved = programID
			return nil
		},
	}, context.Background(), nil)
	model.screen = workHome
	model.openActionPalette()
	for index, item := range model.list.Items() {
		if value, ok := item.(workItem); ok && value.id == actionConflictProgram {
			model.list.Select(index)
			break
		}
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	if model.screen != workConflictProgram {
		t.Fatalf("screen = %v, want conflict program picker", model.screen)
	}
	model.list.Select(2)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	if cmd == nil || model.screen != workOperation {
		t.Fatalf("save did not start: screen=%v cmd=%v", model.screen, cmd)
	}
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	if saved != "goland" || model.actions.ConflictProgram != "goland" || !strings.Contains(model.View(), "Conflict resolver updated") {
		t.Fatalf("saved=%q configured=%q view:\n%s", saved, model.actions.ConflictProgram, model.View())
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
