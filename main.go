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
		OpenConflict: openConfiguredConflict,
		PlanSyncWork: planConfiguredSyncWork, RunSyncWork: runConfiguredSyncWork,
		PlanRemoveWork: planConfiguredRemoveWork, RunRemoveWork: runConfiguredRemoveWork,
		PlanRepairWork: planConfiguredRepairWork, RunRepairWork: runConfiguredRepairWork,
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
			ID: id, Name: cfg.RepoAlias(id), Path: path,
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
	}).Inspect(operationCtx, inspectwork.Request{WorksRoot: cfg.ProjectsRoot, ControlRoot: controlRoot, Name: name})
	if err != nil {
		return repairwork.Plan{}, err
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
	}).Inspect(operationCtx, inspectwork.Request{WorksRoot: cfg.ProjectsRoot, ControlRoot: controlRoot, Name: name})
	if err != nil {
		return removework.Plan{}, err
	}
	return (removework.Planner{}).Build(snapshot, controlRoot)
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
	}).Inspect(operationCtx, inspectwork.Request{WorksRoot: cfg.ProjectsRoot, ControlRoot: controlRoot, Name: name})
	if err != nil {
		return syncwork.Plan{}, err
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
		return runConfigShow()
	}
	switch args[0] {
	case "list":
		return runConfigShow()
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
  goworktree add <project> --repos id,id add repos to an existing project
  goworktree drop <project> --repos id,id [-D] [--yes] remove repos from a project
  goworktree sync <work>           plan from fetched OIDs and rebase Work branches
  goworktree branch <project> <repo> adopt a worktree's current branch
  goworktree list
  goworktree remove <work> --confirm <exact-work-name> delete Work worktrees and local branches
  goworktree cursor <project>
  goworktree goland <project>
  goworktree open <project>        open project in default program
  goworktree doctor
  goworktree repair <work>         apply deterministic repairs from inspected facts
  goworktree config [show|edit|set <key> <value>]
  goworktree repos [list|scan|set <id> --path PATH --branch BRANCH]
  goworktree programs <list|add|update|delete|default|search>

notes:
  dashboard inspection does not mutate or fetch until an explicit action is selected
  use n for New Work and : for the scoped action palette
  use j/k, h/l, g/G, ctrl+u/d, /, r, and q to navigate the dashboard
  explicit commands never open pickers; pass required arguments and --repos
  start resumes typed New Work from its external operation record
  Repair Work refuses ambiguous identity, branch, OID, and corrupt-manifest states
  Remove Work always deletes its confirmed LOCAL branches; remotes are untouched
  drop -D deletes only LOCAL branches; remotes are untouched
  add/drop auto-migrate legacy projects (write .goworktree.json from existing worktrees)

`, version)
}
