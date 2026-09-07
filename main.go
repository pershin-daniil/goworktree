package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pershin-daniil/goworktree/internal/config"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
	"github.com/pershin-daniil/goworktree/internal/project"
	"github.com/pershin-daniil/goworktree/internal/tui"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/changework"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectworks"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/removework"
	"github.com/pershin-daniil/goworktree/internal/workflow/repairwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/syncwork"
)

func exitOnError(err error) {
	if err == nil {
		return
	}
	if tui.IsCancelled(err) {
		fmt.Println("cancelled")
		return
	}
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		exitOnError(tui.RunWorkApp(configuredWorkAppActions()))
		return
	}

	var err error
	switch os.Args[1] {
	case "init":
		err = runInit()
	case "start":
		err = runStart(os.Args[2:])
	case "add":
		err = runAdd(os.Args[2:])
	case "drop":
		err = runDrop(os.Args[2:])
	case "sync":
		err = runSync(os.Args[2:])
	case "branch":
		err = runBranch(os.Args[2:])
	case "list", "ls":
		err = runList()
	case "remove", "rm":
		err = runRemove(os.Args[2:])
	case "archives":
		err = runArchives(os.Args[2:])
	case "cursor":
		err = runCursor(os.Args[2:])
	case "goland":
		err = runGoland(os.Args[2:])
	case "open":
		err = runOpen(os.Args[2:])
	case "doctor":
		err = runDoctor()
	case "repair":
		err = runRepair(os.Args[2:])
	case "config":
		err = runConfig(os.Args[2:])
	case "repos":
		err = runRepos(os.Args[2:])
	case "programs":
		err = runPrograms(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	case "version", "-v", "--version":
		fmt.Println(version)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}

	exitOnError(err)
}

func configuredWorkAppActions() tui.WorkAppActions {
	actions := tui.WorkAppActions{
		Version: version, Load: inspectConfiguredWorks,
		PlanNewWork: planConfiguredNewWork, CreateNewWork: createConfiguredNewWork,
		ResumeNewWork: resumeConfiguredNewWork, OpenWork: openConfiguredWork,
		PlanAddRepositories:    planConfiguredAddRepositories,
		PlanRemoveRepositories: planConfiguredRemoveRepositories,
		RunRepositoryChange:    runConfiguredRepositoryChange,
		ResumeRepositoryChange: resumeConfiguredRepositoryChange,
		OpenConflict:           openConfiguredConflict,
		PlanSyncWork:           planConfiguredSyncWork, RunSyncWork: runConfiguredSyncWork,
		PlanRemoveWork: planConfiguredRemoveWork, RunRemoveWork: runConfiguredRemoveWork,
		PlanRepairWork: planConfiguredRepairWork, RunRepairWork: runConfiguredRepairWork,
		LoadSourceRepos:   inspectAllConfiguredSourceRepositories,
		FetchSourceRepos:  fetchConfiguredSourceRepositories,
		PlanSourceUpdate:  planConfiguredSourceRepositoryUpdate,
		RunSourceUpdate:   runConfiguredSourceRepositoryUpdate,
		OpenSourceRepo:    openConfiguredSourceRepository,
		AddSourceGroup:    addConfiguredSourceRepositoryGroup,
		RemoveSourceGroup: removeConfiguredSourceRepositoryGroup,
		ScanSourceRepos:   scanConfiguredSourceRepositories,
	}
	cfg, err := config.Load()
	if err != nil {
		return actions
	}
	actions.ConflictProgram = cfg.ConflictProgram
	actions.SetConflictProgram = setConfiguredConflictProgram
	repositoryIDs := cfg.RepoNames()
	sort.Strings(repositoryIDs)
	for _, id := range repositoryIDs {
		path, ok := cfg.RepoPath(id)
		if !ok {
			continue
		}
		actions.Repositories = append(actions.Repositories, tui.WorkRepositoryOption{
			ID: id, Name: cfg.RepoAlias(id), Path: path, Groups: cfg.RepoGroups(id),
		})
	}
	for _, id := range cfg.EnabledPrograms() {
		program, ok := cfg.Program(id)
		if !ok {
			continue
		}
		actions.Programs = append(actions.Programs, tui.WorkProgramOption{
			ID: id, Name: program.Name, Default: id == cfg.DefaultProgram,
		})
	}
	return actions
}

func setConfiguredConflictProgram(ctx context.Context, programID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if programID != "" && !cfg.ProgramEnabled(programID) {
		return fmt.Errorf("conflict resolver %q is not enabled", programID)
	}
	cfg.ConflictProgram = programID
	return cfg.Save()
}

func planConfiguredRepairWork(ctx context.Context, name string) (repairwork.Plan, error) {
	cfg, err := config.Load()
	if err != nil {
		return repairwork.Plan{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return repairwork.Plan{}, fmt.Errorf("resolve control root: %w", err)
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	snapshot, err := (inspectwork.Inspector{
		Git: inspectwork.SystemGit{}, Operations: inspectwork.SystemOperationReader{},
		ChangeOperations: inspectwork.SystemChangeOperationReader{},
	}).Inspect(operationCtx, inspectwork.Request{WorksRoot: cfg.ProjectsRoot, ControlRoot: controlRoot, Name: name})
	if err != nil {
		return repairwork.Plan{}, err
	}
	if snapshot.ChangeOperation.ResumeSuggested {
		return repairwork.Plan{}, fmt.Errorf("unfinished repository change blocks Repair Work; Resume it first")
	}
	return (repairwork.Planner{}).Build(snapshot)
}

func runConfiguredRepairWork(ctx context.Context, plan repairwork.Plan) (repairwork.Result, error) {
	cfg, err := config.Load()
	if err != nil {
		return repairwork.Result{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return repairwork.Result{}, fmt.Errorf("resolve control root: %w", err)
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	return (repairwork.Executor{
		Git: repairwork.SystemGit{}, Locker: repairwork.FileLocker{Set: lockops.Set{Root: controlRoot}},
		Store: newwork.OperationStore{},
	}).Execute(operationCtx, plan)
}

func planConfiguredRemoveWork(ctx context.Context, name string) (removework.Plan, error) {
	cfg, err := config.Load()
	if err != nil {
		return removework.Plan{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return removework.Plan{}, fmt.Errorf("resolve control root: %w", err)
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	snapshot, err := (inspectwork.Inspector{
		Git: inspectwork.SystemGit{}, Operations: inspectwork.SystemOperationReader{},
		ChangeOperations: inspectwork.SystemChangeOperationReader{},
	}).Inspect(operationCtx, inspectwork.Request{WorksRoot: cfg.ProjectsRoot, ControlRoot: controlRoot, Name: name})
	if err != nil {
		return removework.Plan{}, err
	}
	if snapshot.ChangeOperation.ResumeSuggested {
		return removework.Plan{}, fmt.Errorf("unfinished repository change blocks Remove Work; Resume it first")
	}
	return (removework.Planner{Git: removework.SystemGit{}}).Build(operationCtx, snapshot, controlRoot)
}

func runConfiguredRemoveWork(ctx context.Context, plan removework.Plan, confirmation string) (removework.Result, error) {
	cfg, err := config.Load()
	if err != nil {
		return removework.Result{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return removework.Result{}, fmt.Errorf("resolve control root: %w", err)
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	return (removework.Executor{
		Git: removework.SystemGit{}, Locker: removework.FileLocker{Set: lockops.Set{Root: controlRoot}},
		Safety: removework.SystemSafety{},
	}).Execute(operationCtx, plan, confirmation)
}

func planConfiguredSyncWork(ctx context.Context, name string) (syncwork.Plan, error) {
	cfg, err := config.Load()
	if err != nil {
		return syncwork.Plan{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return syncwork.Plan{}, fmt.Errorf("resolve control root: %w", err)
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	snapshot, err := (inspectwork.Inspector{
		Git: inspectwork.SystemGit{}, Operations: inspectwork.SystemOperationReader{},
		ChangeOperations: inspectwork.SystemChangeOperationReader{},
	}).Inspect(operationCtx, inspectwork.Request{WorksRoot: cfg.ProjectsRoot, ControlRoot: controlRoot, Name: name})
	if err != nil {
		return syncwork.Plan{}, err
	}
	if snapshot.ChangeOperation.ResumeSuggested {
		return syncwork.Plan{}, fmt.Errorf("unfinished repository change blocks Sync Work; Resume it first")
	}
	configs := make([]syncwork.RepositoryConfig, 0, len(snapshot.Repositories))
	for _, repository := range snapshot.Repositories {
		configs = append(configs, syncwork.RepositoryConfig{
			ID: repository.ID, Remote: cfg.RepoRemote(repository.ID), BasePreference: cfg.RepoBranch(repository.ID),
		})
	}
	return (syncwork.Planner{Git: syncwork.SystemGit{}}).Build(operationCtx, snapshot, controlRoot, configs)
}

func runConfiguredSyncWork(ctx context.Context, plan syncwork.Plan) (syncwork.Result, error) {
	cfg, err := config.Load()
	if err != nil {
		return syncwork.Result{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return syncwork.Result{}, fmt.Errorf("resolve control root: %w", err)
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	return (syncwork.Executor{
		Git: syncwork.SystemGit{}, Locker: syncwork.FileLocker{Set: lockops.Set{Root: controlRoot}},
	}).Execute(operationCtx, plan)
}

func planConfiguredNewWork(ctx context.Context, request newwork.Request) (newwork.Plan, error) {
	cfg, err := config.Load()
	if err != nil {
		return newwork.Plan{}, err
	}
	catalog, err := newwork.CatalogFromConfig(cfg, request.RepositoryIDs)
	if err != nil {
		return newwork.Plan{}, err
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	locks := lockops.Set{Root: catalog.ControlRoot}
	planner := newwork.Planner{
		Git: newwork.SystemGit{}, Locker: newwork.FileRepositoryLocker{Set: locks},
	}
	return planner.Build(operationCtx, catalog, request)
}

func planConfiguredAddRepositories(ctx context.Context, request changework.AddRequest) (changework.Plan, error) {
	cfg, catalog, err := configuredChangeCatalog(ctx, request.WorkName, request.RepositoryIDs, changework.KindAdd)
	if err != nil {
		return changework.Plan{}, err
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	return (changework.Planner{
		Git:    changework.SystemGit{},
		Locker: newwork.FileRepositoryLocker{Set: lockops.Set{Root: catalog.ControlRoot}},
	}).BuildAdd(operationCtx, catalog, request)
}

func planConfiguredRemoveRepositories(ctx context.Context, request changework.RemoveRequest) (changework.Plan, error) {
	cfg, catalog, err := configuredChangeCatalog(ctx, request.WorkName, request.RepositoryIDs, changework.KindRemove)
	if err != nil {
		return changework.Plan{}, err
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	return (changework.Planner{Git: changework.SystemGit{}}).BuildRemove(operationCtx, catalog, request)
}

func runConfiguredRepositoryChange(ctx context.Context, plan changework.Plan) (changework.Result, error) {
	cfg, err := config.Load()
	if err != nil {
		return changework.Result{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return changework.Result{}, fmt.Errorf("resolve control root: %w", err)
	}
	if err := validateConfiguredChangeOperationPath(controlRoot, plan.OperationPath); err != nil {
		return changework.Result{}, err
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	return configuredChangeExecutor(controlRoot).Execute(operationCtx, plan)
}

func resumeConfiguredRepositoryChange(ctx context.Context, operationPath string) (changework.Result, error) {
	return resumeConfiguredChange(ctx, operationPath, nil)
}

func resumeConfiguredMatchingRepositoryChange(ctx context.Context, operationPath string, request changework.ResumeRequest) (changework.Result, error) {
	return resumeConfiguredChange(ctx, operationPath, &request)
}

func resumeConfiguredChange(ctx context.Context, operationPath string, request *changework.ResumeRequest) (changework.Result, error) {
	cfg, err := config.Load()
	if err != nil {
		return changework.Result{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return changework.Result{}, fmt.Errorf("resolve control root: %w", err)
	}
	if err := validateConfiguredChangeOperationPath(controlRoot, operationPath); err != nil {
		return changework.Result{}, err
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	executor := configuredChangeExecutor(controlRoot)
	if request != nil {
		return executor.ResumeMatching(operationCtx, operationPath, *request)
	}
	return executor.Resume(operationCtx, operationPath)
}

func validateConfiguredChangeOperationPath(controlRoot, operationPath string) error {
	expectedDir := filepath.Join(controlRoot, "operations", "change-work")
	if filepath.Clean(filepath.Dir(operationPath)) != filepath.Clean(expectedDir) {
		return fmt.Errorf("Change Work operation path is outside the configured control root")
	}
	return nil
}

func configuredChangeExecutor(controlRoot string) changework.Executor {
	return changework.Executor{
		Git: changework.SystemGit{}, Locker: changework.FileLocker{Set: lockops.Set{Root: controlRoot}},
	}
}

func configuredChangeCatalog(ctx context.Context, name string, selectedIDs []string, kind changework.Kind) (*config.Config, changework.Catalog, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, changework.Catalog{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return nil, changework.Catalog{}, fmt.Errorf("resolve control root: %w", err)
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	snapshot, err := (inspectwork.Inspector{
		Git: inspectwork.SystemGit{}, Operations: inspectwork.SystemOperationReader{},
		ChangeOperations: inspectwork.SystemChangeOperationReader{},
	}).Inspect(operationCtx, inspectwork.Request{
		WorksRoot: cfg.ProjectsRoot, ControlRoot: controlRoot, Name: name,
	})
	if err != nil {
		return nil, changework.Catalog{}, err
	}
	if snapshot.Manifest.State != inspectwork.MetadataValid || snapshot.Manifest.Value == nil {
		return nil, changework.Catalog{}, fmt.Errorf("Work manifest is not valid; run Repair Work")
	}
	if snapshot.Operation.ResumeSuggested {
		return nil, changework.Catalog{}, fmt.Errorf("New Work is incomplete; Resume it before changing repositories")
	}
	if snapshot.ChangeOperation.ResumeSuggested {
		return nil, changework.Catalog{}, fmt.Errorf("repository change is incomplete; Resume it first")
	}
	manifest := cloneConfiguredManifest(*snapshot.Manifest.Value)
	adoptingID := ""
	if kind == changework.KindAdopt && len(selectedIDs) == 1 {
		adoptingID, err = resolveWorkRepositoryID(manifest, selectedIDs[0])
		if err != nil {
			return nil, changework.Catalog{}, err
		}
		selectedIDs = []string{adoptingID}
	}
	for _, problem := range snapshot.Problems {
		if adoptingID != "" && problem.RepositoryID == adoptingID && branchAdoptionProblem(problem.Code) {
			continue
		}
		return nil, changework.Catalog{}, fmt.Errorf("Work needs attention before changing repositories: %s", problem.Message)
	}
	if err := requireTerminalWorkOperations(controlRoot, manifest.WorkID.String()); err != nil {
		return nil, changework.Catalog{}, err
	}
	pool := make([]string, 0, len(manifest.Repositories)+len(selectedIDs))
	for _, repository := range manifest.Repositories {
		pool = append(pool, repository.ID)
	}
	pool = append(pool, selectedIDs...)
	catalog := changework.Catalog{
		WorkRoot: snapshot.WorkRoot, ControlRoot: controlRoot, Manifest: manifest,
		Repositories: make(map[string]changework.Repository, len(selectedIDs)),
	}
	for _, id := range selectedIDs {
		if kind == changework.KindAdopt {
			continue // Adoption uses the source identity already owned by the Work.
		}
		path, ok := cfg.RepoPath(id)
		if !ok {
			return nil, changework.Catalog{}, fmt.Errorf("repository %q is not configured", id)
		}
		catalog.Repositories[id] = changework.Repository{
			ID: id, SourcePath: path, Folder: cfg.FolderName(id, pool),
			Remote: cfg.RepoRemote(id), BasePreference: cfg.RepoBranch(id),
		}
	}
	return cfg, catalog, nil
}

func cloneConfiguredManifest(manifest work.Manifest) work.Manifest {
	manifest.Repositories = append([]work.RepositoryIntent(nil), manifest.Repositories...)
	manifest.InactiveRepositories = append([]work.InactiveRepositoryIntent(nil), manifest.InactiveRepositories...)
	manifest.Harness.RepositoryIDs = append([]string(nil), manifest.Harness.RepositoryIDs...)
	manifest.Harness.UsePaths = append([]string(nil), manifest.Harness.UsePaths...)
	return manifest
}

func createConfiguredNewWork(ctx context.Context, plan newwork.Plan) (newwork.ExecutionResult, error) {
	cfg, err := config.Load()
	if err != nil {
		return newwork.ExecutionResult{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return newwork.ExecutionResult{}, fmt.Errorf("resolve control root: %w", err)
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	executor := configuredNewWorkExecutor(controlRoot)
	return executor.Execute(operationCtx, plan)
}

func resumeConfiguredNewWork(ctx context.Context, operationRecordPath string) (newwork.ExecutionResult, error) {
	cfg, err := config.Load()
	if err != nil {
		return newwork.ExecutionResult{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return newwork.ExecutionResult{}, fmt.Errorf("resolve control root: %w", err)
	}
	operationCtx, cancel := configuredOperationContext(ctx, cfg)
	defer cancel()
	executor := configuredNewWorkExecutor(controlRoot)
	return executor.Resume(operationCtx, operationRecordPath)
}

func configuredNewWorkExecutor(controlRoot string) newwork.Executor {
	return newwork.Executor{
		Git: newwork.SystemGit{}, Locker: newwork.FileExecutionLocker{Set: lockops.Set{Root: controlRoot}},
		Store: newwork.OperationStore{}, Harness: newwork.GoWorkHarness{},
	}
}

func configuredOperationContext(ctx context.Context, cfg *config.Config) (context.Context, context.CancelFunc) {
	timeout := time.Duration(cfg.CommandTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(config.DefaultCommandTimeoutSeconds) * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

func openConfiguredWork(ctx context.Context, request tui.WorkOpenRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	expectedRoot, err := project.Dir(cfg, request.WorkName)
	if err != nil {
		return err
	}
	expectedRoot, err = filepath.Abs(expectedRoot)
	if err != nil {
		return err
	}
	observedRoot, err := filepath.Abs(request.WorkRoot)
	if err != nil {
		return err
	}
	if filepath.Clean(expectedRoot) != filepath.Clean(observedRoot) {
		return fmt.Errorf("Work root changed from %s to %s", expectedRoot, observedRoot)
	}
	return openProgram(cfg, request.Program, observedRoot)
}

func openConfiguredConflict(ctx context.Context, request tui.SyncConflictOpenRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	workRoot, err := project.Dir(cfg, request.WorkName)
	if err != nil {
		return "", err
	}
	workRoot, err = filepath.Abs(workRoot)
	if err != nil {
		return "", err
	}
	repositoryPath, err := filepath.Abs(request.RepositoryPath)
	if err != nil {
		return "", err
	}
	if filepath.Dir(filepath.Clean(repositoryPath)) != filepath.Clean(workRoot) {
		return "", fmt.Errorf("conflict repository %s is outside Work %s", repositoryPath, workRoot)
	}
	programID := cfg.EffectiveConflictProgram()
	if programID == "" {
		return "", fmt.Errorf("no conflict resolver is enabled")
	}
	program, ok := cfg.Program(programID)
	if !ok || !program.Enabled {
		return "", fmt.Errorf("conflict resolver %q is not enabled", programID)
	}
	if err := openProgram(cfg, programID, repositoryPath); err != nil {
		return "", err
	}
	return program.Name, nil
}

func inspectConfiguredWorks(ctx context.Context) (inspectworks.Snapshot, error) {
	cfg, err := config.Load()
	if err != nil {
		return inspectworks.Snapshot{}, err
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return inspectworks.Snapshot{}, fmt.Errorf("resolve control root: %w", err)
	}
	inspector := inspectworks.NewSystemInspector()
	return inspector.Inspect(ctx, inspectworks.Request{
		WorksRoot: cfg.ProjectsRoot, ControlRoot: controlRoot,
	})
}

func runPrograms(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: goworktree programs <list|add|update|delete|default|search>")
	}
	switch args[0] {
	case "list":
		return runProgramsList()
	case "add":
		if len(args) < 2 || flagValue(args, "--name") == "" || flagValue(args, "--path") == "" {
			return fmt.Errorf("usage: goworktree programs add <id> --name NAME --path PATH [--args \"ARG …\"]")
		}
		return runProgramsAdd(args[1], flagValue(args, "--name"), flagValue(args, "--path"), flagValue(args, "--args"))
	case "update":
		if len(args) < 2 || (flagValue(args, "--name") == "" && flagValue(args, "--path") == "" && flagValue(args, "--enabled") == "" && !hasFlag(args, "--args")) {
			return fmt.Errorf("usage: goworktree programs update <id> [--name NAME] [--path PATH] [--args \"ARG …\"] [--enabled=true|false]")
		}
		return runProgramsUpdate(args[1], flagValue(args, "--name"), flagValue(args, "--path"), flagValue(args, "--args"), hasFlag(args, "--args"), flagValue(args, "--enabled"))
	case "delete":
		if len(args) != 2 {
			return fmt.Errorf("usage: goworktree programs delete <id>")
		}
		return runProgramsDelete(args[1])
	case "default":
		if len(args) != 2 {
			return fmt.Errorf("usage: goworktree programs default <id>")
		}
		return runProgramsDefault(args[1])
	case "search":
		if len(args) > 2 {
			return fmt.Errorf("usage: goworktree programs search [query]")
		}
		query := ""
		if len(args) == 2 {
			query = args[1]
		}
		return runProgramsSearch(query)
	case "open":
		if len(args) != 3 {
			return fmt.Errorf("usage: goworktree programs open <id> <project>")
		}
		return runProgramsOpen(args[1], args[2])
	default:
		return fmt.Errorf("unknown programs subcommand: %s", args[0])
	}
}

func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

func runConfig(args []string) error {
	if len(args) == 0 {
		return runConfigShow()
	}
	switch args[0] {
	case "show":
		return runConfigShow()
	case "edit":
		return runConfigEdit()
	case "set":
		if len(args) != 3 {
			return fmt.Errorf("usage: goworktree config set <key> <value>")
		}
		return runConfigSet(args[1], args[2])
	default:
		return fmt.Errorf("unknown config subcommand: %s", args[0])
	}
}

func runRepos(args []string) error {
	if len(args) == 0 {
		return runReposList(nil)
	}
	switch args[0] {
	case "list":
		return runReposList(args[1:])
	case "status":
		return runReposStatus(args[1:])
	case "fetch":
		return runReposFetch(args[1:])
	case "update":
		return runReposUpdate(args[1:])
	case "open":
		return runReposOpen(args[1:])
	case "group":
		return runReposGroup(args[1:])
	case "scan":
		return runReposScan()
	case "set":
		if len(args) < 2 {
			return fmt.Errorf("usage: goworktree repos set <id> [--path PATH] [--branch BRANCH]")
		}
		return runReposSet(args[1], flagValue(args, "--path"), flagValue(args, "--branch"))
	default:
		return fmt.Errorf("unknown repos subcommand: %s", args[0])
	}
}

func flagValue(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
		if len(arg) > len(name)+1 && arg[:len(name)+1] == name+"=" {
			return arg[len(name)+1:]
		}
	}
	return ""
}

func usage() {
	fmt.Fprintf(os.Stderr, `goworktree %s — git worktree helper for multi-repo projects

usage:
  goworktree                       Works dashboard, New Work, and scoped actions
  goworktree init
  goworktree start <name> --repos id,id [--offline] [--open] create or resume typed New Work
  goworktree add <work> --repos id,id [--offline] add repositories to an existing Work
  goworktree drop <work> --repos id,id [-D] --yes remove clean worktrees from a Work
  goworktree sync <work>           plan from fetched OIDs and rebase Work branches
  goworktree branch <work> <repo>  record a worktree's current local branch with resumable checkpoints
  goworktree list
  goworktree remove <work> --confirm <exact-work-name> delete Work worktrees and local branches
  goworktree archives <list|show|delete>             inspect or delete removed-Work metadata archives
  goworktree cursor <project>
  goworktree goland <project>
  goworktree open <project>        open project in default program
  goworktree doctor
  goworktree repair <work>         apply deterministic repairs from inspected facts
  goworktree config [show|edit|set <key> <value>]
  goworktree repos list [--group GROUP]
  goworktree repos status [--repos id,id|--group GROUP|--all]
  goworktree repos fetch (--repos id,id|--group GROUP|--all)
  goworktree repos update (--repos id,id|--group GROUP|--all)
  goworktree repos open <id> [--program ID]
  goworktree repos group <list|add|remove|rename|delete>
  goworktree repos scan
  goworktree repos set <id> [--path PATH] [--branch BRANCH]
  goworktree programs <list|add|update|delete|default|search>

notes:
  dashboard inspection does not mutate or fetch until an explicit action is selected
  use Tab to switch Works/Repositories, n for New Work, and : for scoped actions
  use j/k, h/l, g/G, ctrl+u/d, /, r, and q to navigate; ? opens contextual help
  explicit commands never open pickers; pass required arguments and --repos
  start resumes typed New Work from its external operation record
  Repair Work refuses ambiguous identity, branch, OID, and corrupt-manifest states
  Remove Work includes retained branches from drop in its confirmed LOCAL targets
  add is online by default; --offline resolves only locally known base refs
  drop keeps Work branches by default; -D deletes exact LOCAL branches only
  add/drop retries must match the original repository set, mode, and deletion flags
  interrupted add/drop/branch operations Resume from the external Change Work record

`, version)
}
