package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pershin-daniil/goworktree/internal/workflow/changework"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectworks"
	"github.com/pershin-daniil/goworktree/internal/workflow/sourcerepos"
)

func TestWorkAppSourceTabAndRepositoryChangesCoexist(t *testing.T) {
	works := testWorksSnapshot("ticket-42")
	sources := sourcerepos.Snapshot{Repositories: []sourcerepos.RepositorySnapshot{{
		RepositoryConfig: sourcerepos.RepositoryConfig{ID: "worker", Name: "Worker", Path: "/repos/worker", Remote: "origin", DefaultBranch: "main"},
		Relation:         sourcerepos.RelationEqual,
	}}}
	model := newWorkAppModel(WorkAppActions{
		Load:            func(context.Context) (inspectworks.Snapshot, error) { return works, nil },
		LoadSourceRepos: func(context.Context) (sourcerepos.Snapshot, error) { return sources, nil },
		Repositories: []WorkRepositoryOption{
			{ID: "api", Name: "API", Path: "/repos/api"},
			{ID: "worker", Name: "Worker", Path: "/repos/worker"},
		},
		PlanAddRepositories: func(context.Context, changework.AddRequest) (changework.Plan, error) {
			return changework.Plan{}, nil
		},
		RunRepositoryChange: func(context.Context, changework.Plan) (changework.Result, error) {
			return changework.Result{}, nil
		},
	}, context.Background(), nil)
	model.width, model.height = 100, 30
	model.snapshot = works
	model.setHome("ticket-42")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(workAppModel)
	if model.screen != workSourceLoading || cmd == nil {
		t.Fatalf("source tab = screen %v, cmd %v", model.screen, cmd)
	}
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	if model.screen != workSourceHome || len(model.list.Items()) != 1 {
		t.Fatalf("source home = screen %v, items %d", model.screen, len(model.list.Items()))
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(workAppModel)
	if model.screen != workHome {
		t.Fatalf("return from source tab = screen %v", model.screen)
	}
	model.setWork("ticket-42", "")
	model.openActionPalette()
	selectWorkItem(t, &model.list, actionAddRepositories)
	item := model.list.SelectedItem().(workItem)
	if item.blockedReason != "" {
		t.Fatalf("Add repositories unexpectedly blocked: %s", item.blockedReason)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	if model.screen != workChangeRepositories || len(model.list.Items()) != 1 || model.list.Items()[0].(workItem).id != "worker" {
		t.Fatalf("change picker = screen %v, items %+v", model.screen, model.list.Items())
	}
}

func TestWorkAppRepositoryTabFiltersOneCatalogByGroup(t *testing.T) {
	model := sourceTestModel(t, WorkAppActions{})
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(workAppModel)
	if model.screen != workSourceLoading || cmd == nil {
		t.Fatalf("source tab = screen %v, cmd %v", model.screen, cmd)
	}
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	if model.screen != workSourceHome || len(model.list.Items()) != 2 {
		t.Fatalf("source home = screen %v, items %d", model.screen, len(model.list.Items()))
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	model = updated.(workAppModel)
	if model.screen != workSourceFilter {
		t.Fatalf("filter screen = %v", model.screen)
	}
	selectWorkItem(t, &model.list, sourceFilterPrefix+"team-a")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	if model.screen != workSourceHome || len(model.list.Items()) != 1 {
		t.Fatalf("filtered source home = screen %v, items %d", model.screen, len(model.list.Items()))
	}
	item := model.list.Items()[0].(workItem)
	if item.id != "api" || !strings.Contains(model.View(), "group: team-a") {
		t.Fatalf("filtered item/view = %q\n%s", item.id, model.View())
	}
}

func TestWorkAppRepositoryUpdateUsesSelectedScopeAndPlan(t *testing.T) {
	var plannedIDs []string
	actions := WorkAppActions{
		PlanSourceUpdate: func(_ context.Context, ids []string) (sourcerepos.Plan, error) {
			plannedIDs = append([]string(nil), ids...)
			return sourcerepos.Plan{Repositories: []sourcerepos.RepositoryPlan{{
				RepositoryConfig: sourcerepos.RepositoryConfig{ID: "api", Path: "/repos/api", Remote: "origin", DefaultBranch: "main"},
				LocalRef:         "refs/heads/main", LocalOID: "old", RemoteRef: "refs/remotes/origin/main", TargetOID: "new", Action: sourcerepos.ActionUpdateCheckout,
			}}}, nil
		},
		RunSourceUpdate: func(_ context.Context, _ sourcerepos.Plan) (sourcerepos.Result, error) {
			return sourcerepos.Result{Repositories: []sourcerepos.RepositoryResult{{ID: "api", Status: sourcerepos.StatusUpdated}}}, nil
		},
	}
	model := sourceTestModel(t, actions)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(workAppModel)
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	selectWorkItem(t, &model.list, "api")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = updated.(workAppModel)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(":")})
	model = updated.(workAppModel)
	selectWorkItem(t, &model.list, sourceActionUpdate)
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	if model.screen != workOperation || cmd == nil {
		t.Fatalf("planning = screen %v, cmd %v", model.screen, cmd)
	}
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	if model.screen != workSourceUpdatePlan || len(plannedIDs) != 1 || plannedIDs[0] != "api" {
		t.Fatalf("plan screen/ids = %v, %v", model.screen, plannedIDs)
	}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	if model.screen != workActionResult || !strings.Contains(model.detailContent, "updated") {
		t.Fatalf("result = screen %v\n%s", model.screen, model.detailContent)
	}
}

func TestWorkAppRepositoryGroupFilterKeepsHiddenSelectionVisibleInHeader(t *testing.T) {
	model := sourceTestModel(t, WorkAppActions{})
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(workAppModel)
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	selectWorkItem(t, &model.list, "worker")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = updated.(workAppModel)
	model.sourceFilter = "team-a"
	model.setSourceHome("api")
	if !strings.Contains(model.sourceHomeView(), "1 hidden by filter") {
		t.Fatalf("hidden selection missing from header:\n%s", model.sourceHomeView())
	}
}

func TestWorkAppRepositorySelectAllTogglesOnlyVisibleGroup(t *testing.T) {
	model := sourceTestModel(t, WorkAppActions{})
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(workAppModel)
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	model.selectedSources["worker"] = true
	model.sourceFilter = "team-a"
	model.setSourceHome("api")

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model = updated.(workAppModel)
	if !model.selectedSources["api"] || !model.selectedSources["worker"] {
		t.Fatalf("select visible = %#v, want visible api selected and hidden worker unchanged", model.selectedSources)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model = updated.(workAppModel)
	if model.selectedSources["api"] || !model.selectedSources["worker"] {
		t.Fatalf("deselect visible = %#v, want visible api cleared and hidden worker unchanged", model.selectedSources)
	}
}

func TestWorkAppRepositorySelectAllPreservesTextSearch(t *testing.T) {
	model := sourceTestModel(t, WorkAppActions{})
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(workAppModel)
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	model.list.SetFilterText("API")
	if got := len(model.list.VisibleItems()); got != 1 {
		t.Fatalf("visible search results = %d, want 1", got)
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model = updated.(workAppModel)
	if cmd != nil {
		updated, _ = model.Update(cmd())
		model = updated.(workAppModel)
	}
	if !model.selectedSources["api"] || model.selectedSources["worker"] {
		t.Fatalf("search selection = %#v, want only api selected", model.selectedSources)
	}
	if got := model.list.FilterInput.Value(); got != "API" {
		t.Fatalf("search text = %q, want API", got)
	}
	if got := len(model.list.VisibleItems()); got != 1 {
		t.Fatalf("visible search results after selection = %d, want 1", got)
	}
	if !strings.Contains(model.sourceHomeView(), "1 of 2") {
		t.Fatalf("search count missing from header:\n%s", model.sourceHomeView())
	}
}

func TestWorkAppSourceViewFitsMinimumTerminal(t *testing.T) {
	model := sourceTestModel(t, WorkAppActions{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: minimumWorkAppWidth, Height: minimumWorkAppHeight})
	model = updated.(workAppModel)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(workAppModel)
	updated, _ = model.Update(cmd())
	model = updated.(workAppModel)
	if width := lipgloss.Width(model.View()); width > minimumWorkAppWidth {
		t.Fatalf("source catalog width = %d at %dx%d:\n%s", width, minimumWorkAppWidth, minimumWorkAppHeight, model.View())
	}
}

func TestWorkAppSourceListNavigationUsesRenderedMinimumHeight(t *testing.T) {
	model := sourceTestModel(t, WorkAppActions{})
	model.width, model.height = minimumWorkAppWidth, minimumWorkAppHeight
	repositories := make([]sourcerepos.RepositorySnapshot, 0, 24)
	for index := 0; index < 24; index++ {
		id := fmt.Sprintf("repo-%02d", index)
		repositories = append(repositories, sourcerepos.RepositorySnapshot{
			RepositoryConfig: sourcerepos.RepositoryConfig{ID: id, Name: id, Path: "/repos/" + id, DefaultBranch: "main"},
		})
	}
	model.sourceSnapshot = sourcerepos.Snapshot{Repositories: repositories}
	model.sourceLoaded = true
	model.setSourceHome("")
	if got, want := model.list.Height(), minimumWorkAppHeight-workAppChrome; got != want {
		t.Fatalf("list height = %d, want %d", got, want)
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	model = updated.(workAppModel)
	item, ok := model.list.SelectedItem().(workItem)
	if !ok || item.id != "repo-23" {
		t.Fatalf("bottom selection = %#v", model.list.SelectedItem())
	}
	if !strings.Contains(model.View(), "repo-23") {
		t.Fatalf("bottom item is not rendered:\n%s", model.View())
	}
}

func TestWorkAppDetailNavigationUsesRenderedMinimumHeight(t *testing.T) {
	model := sourceTestModel(t, WorkAppActions{})
	model.width, model.height = minimumWorkAppWidth, minimumWorkAppHeight
	model.sourceSnapshot = sourcerepos.Snapshot{Repositories: []sourcerepos.RepositorySnapshot{{
		RepositoryConfig: sourcerepos.RepositoryConfig{ID: "api", Name: "API", Path: "/repos/api", DefaultBranch: "main"},
	}}}
	model.sourceLoaded = true
	model.setSourceHome("api")
	if !model.openSourceRepository("api") {
		t.Fatal("missing source repository")
	}
	lines := make([]string, 0, 40)
	for index := 0; index < 40; index++ {
		lines = append(lines, fmt.Sprintf("row-%02d", index))
	}
	model.setDetail("Source repository · api", strings.Join(lines, "\n"))
	if got, want := model.viewport.Height, minimumWorkAppHeight-workAppChrome; got != want {
		t.Fatalf("viewport height = %d, want %d", got, want)
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	model = updated.(workAppModel)
	if !model.viewport.AtBottom() {
		t.Fatalf("viewport did not reach bottom: offset=%d", model.viewport.YOffset)
	}
	if !strings.Contains(model.View(), "row-39") {
		t.Fatalf("bottom detail content is not rendered:\n%s", model.View())
	}
}

func TestWorkAppSourceLoadingCancelReturnsToWorks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := newWorkAppModel(WorkAppActions{
		Load: func(context.Context) (inspectworks.Snapshot, error) { return inspectworks.Snapshot{}, nil },
		LoadSourceRepos: func(loadCtx context.Context) (sourcerepos.Snapshot, error) {
			<-loadCtx.Done()
			return sourcerepos.Snapshot{}, loadCtx.Err()
		},
	}, ctx, cancel)
	model.width, model.height = 100, 30
	model.setHome("")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(workAppModel)
	if model.screen != workSourceLoading || cmd == nil {
		t.Fatalf("loading screen=%v cmd=%v", model.screen, cmd)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(workAppModel)
	if model.screen != workHome {
		t.Fatalf("cancel returned screen=%v", model.screen)
	}
}

func TestWorkAppSourceUpdatePlanCancelRestoresDetail(t *testing.T) {
	model := sourceTestModel(t, WorkAppActions{})
	model.sourceSnapshot = sourcerepos.Snapshot{Repositories: []sourcerepos.RepositorySnapshot{{
		RepositoryConfig: sourcerepos.RepositoryConfig{ID: "api", Path: "/repos/api", Remote: "origin", DefaultBranch: "main"},
	}}}
	model.sourceLoaded = true
	model.setSourceHome("api")
	if !model.openSourceRepository("api") {
		t.Fatal("missing source repository")
	}
	model.openSourceActions()
	model.screen = workSourceUpdatePlan
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(workAppModel)
	if model.screen != workSourceRepository || model.selectedSourceID != "api" {
		t.Fatalf("cancel restored screen=%v repository=%q", model.screen, model.selectedSourceID)
	}
}

func TestWorkAppSourceHelpReturnsToCatalog(t *testing.T) {
	model := sourceTestModel(t, WorkAppActions{})
	model.width, model.height = minimumWorkAppWidth, minimumWorkAppHeight
	model.sourceLoaded = true
	model.setSourceHome("api")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	model = updated.(workAppModel)
	if model.screen != workHelp || !strings.Contains(model.detailContent, "Space selects one") {
		t.Fatalf("help screen=%v content=%q", model.screen, model.detailContent)
	}
	if width := lipgloss.Width(model.View()); width > minimumWorkAppWidth {
		t.Fatalf("help width = %d at %dx%d:\n%s", width, minimumWorkAppWidth, minimumWorkAppHeight, model.View())
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	model = updated.(workAppModel)
	if cmd != nil || model.screen != workHelp {
		t.Fatalf("refresh escaped help: screen=%v cmd=%v", model.screen, cmd)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(workAppModel)
	if model.screen != workSourceHome {
		t.Fatalf("help return screen=%v", model.screen)
	}
}

func TestWorkAppSourceHelpPreservesActionReturn(t *testing.T) {
	model := sourceTestModel(t, WorkAppActions{})
	model.sourceSnapshot = sourcerepos.Snapshot{Repositories: []sourcerepos.RepositorySnapshot{{
		RepositoryConfig: sourcerepos.RepositoryConfig{ID: "api", Name: "API", Path: "/repos/api", DefaultBranch: "main"},
	}}}
	model.sourceLoaded = true
	model.setSourceHome("api")
	model.openSourceActions()
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	model = updated.(workAppModel)
	if model.screen != workHelp {
		t.Fatalf("help screen=%v", model.screen)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(workAppModel)
	if model.screen != workSourceActions {
		t.Fatalf("help return screen=%v", model.screen)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(workAppModel)
	if model.screen != workSourceHome {
		t.Fatalf("action close screen=%v", model.screen)
	}
}

func TestWorkAppQuestionMarkRemainsLiteralFilterText(t *testing.T) {
	model := sourceTestModel(t, WorkAppActions{})
	model.sourceSnapshot = sourcerepos.Snapshot{Repositories: []sourcerepos.RepositorySnapshot{{
		RepositoryConfig: sourcerepos.RepositoryConfig{ID: "api", Name: "API", Path: "/repos/api", DefaultBranch: "main"},
	}}}
	model.sourceLoaded = true
	model.setSourceHome("api")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	model = updated.(workAppModel)
	if model.list.FilterState() != list.Filtering {
		t.Fatalf("filter state=%v", model.list.FilterState())
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	model = updated.(workAppModel)
	if model.screen != workSourceHome || model.list.FilterInput.Value() != "?" {
		t.Fatalf("question mark opened help or was lost: screen=%v filter=%q", model.screen, model.list.FilterInput.Value())
	}
}

func TestFormatSourcePlanShowsScopeAndSafetyFacts(t *testing.T) {
	plan := sourcerepos.Plan{Repositories: []sourcerepos.RepositoryPlan{
		{RepositoryConfig: sourcerepos.RepositoryConfig{ID: "api", Path: "/repos/api", Remote: "origin", DefaultBranch: "main"}, GitCommonDir: "/repos/api/.git", Relation: sourcerepos.RelationBehind, Action: sourcerepos.ActionUpdateCheckout, LocalRef: "refs/heads/main", LocalOID: "old", RemoteRef: "refs/remotes/origin/main", TargetOID: "new"},
		{RepositoryConfig: sourcerepos.RepositoryConfig{ID: "web", Path: "/repos/web", Remote: "origin", DefaultBranch: "main"}, Relation: sourcerepos.RelationEqual, Action: sourcerepos.ActionUnchanged},
		{RepositoryConfig: sourcerepos.RepositoryConfig{ID: "worker", Path: "/repos/worker", Remote: "origin", DefaultBranch: "main"}, Relation: sourcerepos.RelationDiverged, Action: sourcerepos.ActionSkip, Reason: "diverged"},
		{RepositoryConfig: sourcerepos.RepositoryConfig{ID: "legacy", Path: "/repos/legacy", Remote: "origin", DefaultBranch: "main"}, Action: sourcerepos.ActionFailed, Reason: "missing remote"},
	}}
	formatted := formatSourcePlan(plan)
	for _, wanted := range []string{"Scope:", "api, web, worker, legacy", "Updates:         1", "Unchanged:       1", "Skipped:         1", "Failed:          1", "source: /repos/api", "identity: /repos/api/.git", "configured: origin / main", "relation: fetched behind"} {
		if !strings.Contains(formatted, wanted) {
			t.Fatalf("plan missing %q:\n%s", wanted, formatted)
		}
	}
}

func TestNewWorkRepositoryPickerFiltersByGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := newWorkAppModel(WorkAppActions{
		Load: func(context.Context) (inspectworks.Snapshot, error) { return inspectworks.Snapshot{}, nil },
		Repositories: []WorkRepositoryOption{
			{ID: "api", Name: "API", Path: "/repos/api", Groups: []string{"team-a"}},
			{ID: "worker", Name: "Worker", Path: "/repos/worker", Groups: []string{"team-b"}},
		},
	}, ctx, cancel)
	model.width, model.height = 100, 30
	model.snapshot = inspectworks.Snapshot{}
	model.setHome("")
	model.beginNewWork()
	model.input.SetValue("ticket")
	model.setNewWorkRepositories(0)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	model = updated.(workAppModel)
	if model.screen != workNewRepositoryFilter {
		t.Fatalf("filter screen = %v", model.screen)
	}
	selectWorkItem(t, &model.list, sourceFilterPrefix+"team-b")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(workAppModel)
	if model.screen != workNewRepositories || len(model.list.Items()) != 1 || model.list.Items()[0].(workItem).id != "worker" {
		t.Fatalf("filtered picker = screen %v, items %+v", model.screen, model.list.Items())
	}
}

func sourceTestModel(t *testing.T, overrides WorkAppActions) workAppModel {
	t.Helper()
	snapshot := sourcerepos.Snapshot{Repositories: []sourcerepos.RepositorySnapshot{
		{RepositoryConfig: sourcerepos.RepositoryConfig{ID: "api", Name: "API", Path: "/repos/api", Remote: "origin", DefaultBranch: "main", Groups: []string{"team-a"}}, Relation: sourcerepos.RelationBehind},
		{RepositoryConfig: sourcerepos.RepositoryConfig{ID: "worker", Name: "Worker", Path: "/repos/worker", Remote: "origin", DefaultBranch: "main", Groups: []string{"team-b"}}, Relation: sourcerepos.RelationEqual},
	}}
	actions := WorkAppActions{
		Load:              func(context.Context) (inspectworks.Snapshot, error) { return inspectworks.Snapshot{}, nil },
		LoadSourceRepos:   func(context.Context) (sourcerepos.Snapshot, error) { return snapshot, nil },
		FetchSourceRepos:  overrides.FetchSourceRepos,
		PlanSourceUpdate:  overrides.PlanSourceUpdate,
		RunSourceUpdate:   overrides.RunSourceUpdate,
		OpenSourceRepo:    overrides.OpenSourceRepo,
		AddSourceGroup:    overrides.AddSourceGroup,
		RemoveSourceGroup: overrides.RemoveSourceGroup,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := newWorkAppModel(actions, ctx, cancel)
	model.width, model.height = 100, 30
	model.snapshot = inspectworks.Snapshot{}
	model.setHome("")
	return model
}

func selectWorkItem(t *testing.T, listModel *list.Model, id string) {
	t.Helper()
	for index, raw := range listModel.Items() {
		item, ok := raw.(workItem)
		if ok && item.id == id {
			listModel.Select(index)
			return
		}
	}
	t.Fatalf("item %q not found", id)
}
