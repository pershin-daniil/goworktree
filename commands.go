package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pershin-daniil/goworktree/internal/archive"
	"github.com/pershin-daniil/goworktree/internal/config"
	"github.com/pershin-daniil/goworktree/internal/git"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
	"github.com/pershin-daniil/goworktree/internal/project"
	"github.com/pershin-daniil/goworktree/internal/tui"
	"github.com/pershin-daniil/goworktree/internal/workflow/changework"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
)

func csvFlag(args []string, name string) []string {
	v := flagValue(args, name)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func uniqueIDs(ids []string) error {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			return fmt.Errorf("repository %q was selected more than once", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func isFlagWithValue(args []string, index int, name string) bool {
	return args[index] == name || strings.HasPrefix(args[index], name+"=")
}

func runInit() error {
	return tui.RunInit()
}

func runStart(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	openDefault := false
	mode := newwork.ModeOnline
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--open" {
			openDefault = true
			continue
		}
		if arg == "--offline" {
			mode = newwork.ModeOffline
			continue
		}
		if isFlagWithValue(args, i, "--repos") {
			if arg == "--repos" {
				i++
			}
			continue
		}
		positional = append(positional, arg)
	}

	name := ""
	if len(positional) > 0 {
		name = positional[0]
	}
	name = strings.TrimSpace(name)
	if len(positional) != 1 || name == "" {
		return fmt.Errorf("usage: goworktree start <name> --repos id,id [--offline] [--open]")
	}

	selected := csvFlag(args, "--repos")
	if len(selected) == 0 {
		return fmt.Errorf("usage: goworktree start <name> --repos id,id [--offline] [--open]")
	}
	if err := uniqueIDs(selected); err != nil {
		return err
	}
	ctx := context.Background()
	if works, inspectErr := inspectConfiguredWorks(ctx); inspectErr == nil {
		for _, entry := range works.Works {
			if entry.Name == name && entry.Snapshot != nil && entry.Snapshot.Operation.ResumeSuggested {
				fmt.Printf("resuming New Work %q\n", name)
				result, err := resumeConfiguredNewWork(ctx, entry.Snapshot.Operation.Path)
				if err != nil {
					return err
				}
				fmt.Printf("status: %s\nroot: %s\n", result.Status, result.WorkRoot)
				if openDefault {
					return openDefaultProgram(cfg, result.WorkRoot)
				}
				return nil
			}
		}
	}
	request := newwork.Request{Name: name, RepositoryIDs: selected, BaseOverrides: map[string]string{}, Mode: mode}
	plan, err := planConfiguredNewWork(ctx, request)
	if err != nil {
		return err
	}
	fmt.Printf("creating New Work %q from %d immutable base commits\n", name, len(plan.Repositories))
	for _, repository := range plan.Repositories {
		fmt.Printf("  %-24s %s @ %s\n", repository.ID, repository.BaseRef, shortCLIRevision(repository.BaseOID))
	}
	result, err := createConfiguredNewWork(ctx, plan)
	if err != nil {
		return err
	}
	fmt.Printf("status: %s\nroot: %s\n", result.Status, result.WorkRoot)
	if openDefault {
		return openDefaultProgram(cfg, result.WorkRoot)
	}
	return nil
}

func shortCLIRevision(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func requireManifest(cfg *config.Config, name string) (projectDir string, m *project.Manifest, lock *project.Lock, err error) {
	projectDir, err = project.Dir(cfg, name)
	if err != nil {
		return "", nil, nil, err
	}
	lock, err = project.AcquireLock(projectDir)
	if err != nil {
		return "", nil, nil, err
	}
	hadManifest := false
	if _, e := project.LoadManifest(projectDir); e == nil {
		hadManifest = true
	}
	m, err = project.EnsureManifest(cfg, projectDir, name)
	if err != nil {
		_ = lock.Release()
		return "", nil, nil, err
	}
	if !hadManifest {
		fmt.Printf("migrated manifest for %q (%d repos)\n", name, len(m.Repos))
	}
	if removed := m.DeduplicateRepos(); removed > 0 {
		if err := m.Save(projectDir); err != nil {
			_ = lock.Release()
			return "", nil, nil, fmt.Errorf("repair duplicate repositories in manifest: %w", err)
		}
		fmt.Printf("repaired %q: removed %d duplicate repository entry(s)\n", name, removed)
	}
	return projectDir, m, lock, nil
}

func runAdd(args []string) error {
	name, selected, mode, err := parseAddArgs(args)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if resumed, err := resumePendingRepositoryChange(ctx, changework.ResumeRequest{
		WorkName: name, Kind: changework.KindAdd, RepositoryIDs: selected, Mode: mode,
	}); resumed || err != nil {
		return err
	}
	plan, err := planConfiguredAddRepositories(ctx, changework.AddRequest{
		WorkName: name, RepositoryIDs: selected, Mode: mode,
	})
	if err != nil {
		return err
	}
	fmt.Printf("adding %d repository(s) to %q (%s)\n", len(plan.Repositories), name, mode)
	for _, repository := range plan.Repositories {
		action := "create"
		if repository.Reattach {
			action = "reattach"
		}
		fmt.Printf("  %-10s %-24s %s @ %s\n", action, repository.ID, repository.BranchRef, shortCLIRevision(repository.BranchOID))
	}
	result, err := runConfiguredRepositoryChange(ctx, plan)
	printRepositoryChangeResult(result)
	return err
}

func runDrop(args []string) error {
	name, selected, deleteBranches, assumeYes, err := parseDropArgs(args)
	if err != nil {
		return err
	}

	if !assumeYes {
		return fmt.Errorf("refusing destructive operation without --yes")
	}
	ctx := context.Background()
	if resumed, err := resumePendingRepositoryChange(ctx, changework.ResumeRequest{
		WorkName: name, Kind: changework.KindRemove, RepositoryIDs: selected, DeleteBranches: deleteBranches,
	}); resumed || err != nil {
		return err
	}
	plan, err := planConfiguredRemoveRepositories(ctx, changework.RemoveRequest{
		WorkName: name, RepositoryIDs: selected, DeleteBranches: deleteBranches,
	})
	if err != nil {
		return err
	}
	fmt.Printf("removing %d repository(s) from %q\n", len(plan.Repositories), name)
	for _, repository := range plan.Repositories {
		branch := "keep " + repository.BranchRef
		if repository.DeleteBranch {
			branch = "delete " + repository.BranchRef + " @ " + shortCLIRevision(repository.BranchOID)
		}
		fmt.Printf("  %-24s %s\n", repository.ID, branch)
	}
	result, err := runConfiguredRepositoryChange(ctx, plan)
	printRepositoryChangeResult(result)
	return err
}

func parseAddArgs(args []string) (string, []string, newwork.Mode, error) {
	mode := newwork.ModeOnline
	name, repositoryValue, err := parseRepositoryCommandArgs(args, func(arg string) (bool, error) {
		if arg == "--offline" {
			mode = newwork.ModeOffline
			return true, nil
		}
		return false, nil
	})
	if err != nil || name == "" || repositoryValue == "" {
		if err != nil {
			return "", nil, "", err
		}
		return "", nil, "", fmt.Errorf("usage: goworktree add <work> --repos id,id [--offline]")
	}
	selected := splitRepositoryIDs(repositoryValue)
	if len(selected) == 0 {
		return "", nil, "", fmt.Errorf("usage: goworktree add <work> --repos id,id [--offline]")
	}
	if err := uniqueIDs(selected); err != nil {
		return "", nil, "", err
	}
	return name, selected, mode, nil
}

func parseDropArgs(args []string) (string, []string, bool, bool, error) {
	deleteBranches, assumeYes := false, false
	name, repositoryValue, err := parseRepositoryCommandArgs(args, func(arg string) (bool, error) {
		switch arg {
		case "--delete-branches", "-D":
			deleteBranches = true
			return true, nil
		case "--yes":
			assumeYes = true
			return true, nil
		default:
			return false, nil
		}
	})
	if err != nil || name == "" || repositoryValue == "" {
		if err != nil {
			return "", nil, false, false, err
		}
		return "", nil, false, false, fmt.Errorf("usage: goworktree drop <work> --repos id,id [-D] --yes")
	}
	selected := splitRepositoryIDs(repositoryValue)
	if len(selected) == 0 {
		return "", nil, false, false, fmt.Errorf("usage: goworktree drop <work> --repos id,id [-D] --yes")
	}
	if err := uniqueIDs(selected); err != nil {
		return "", nil, false, false, err
	}
	return name, selected, deleteBranches, assumeYes, nil
}

func parseRepositoryCommandArgs(args []string, parseFlag func(string) (bool, error)) (string, string, error) {
	var positional []string
	repositoryValue := ""
	repositorySet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		handled, err := parseFlag(arg)
		if err != nil {
			return "", "", err
		}
		if handled {
			continue
		}
		switch {
		case arg == "--repos":
			if repositorySet || index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return "", "", fmt.Errorf("--repos requires one comma-separated value")
			}
			index++
			repositoryValue = args[index]
			repositorySet = true
		case strings.HasPrefix(arg, "--repos="):
			if repositorySet {
				return "", "", fmt.Errorf("--repos may be specified only once")
			}
			repositoryValue = strings.TrimPrefix(arg, "--repos=")
			repositorySet = true
		case strings.HasPrefix(arg, "-"):
			return "", "", fmt.Errorf("unknown flag %q", arg)
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) != 1 || strings.TrimSpace(positional[0]) == "" {
		return "", "", nil
	}
	return strings.TrimSpace(positional[0]), repositoryValue, nil
}

func splitRepositoryIDs(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func resumePendingRepositoryChange(ctx context.Context, request changework.ResumeRequest) (bool, error) {
	snapshot, err := inspectConfiguredWorks(ctx)
	if err != nil {
		return false, err
	}
	for _, entry := range snapshot.Works {
		if entry.Name != request.WorkName || entry.Snapshot == nil || !entry.Snapshot.ChangeOperation.ResumeSuggested {
			continue
		}
		record, err := changework.LoadRecord(entry.Snapshot.ChangeOperation.Path)
		if err != nil {
			return true, err
		}
		if request.Kind == changework.KindAdopt && len(request.RepositoryIDs) == 1 {
			id, err := resolveWorkRepositoryID(record.Plan.Before, request.RepositoryIDs[0])
			if err != nil {
				return true, err
			}
			request.RepositoryIDs = []string{id}
		}
		if err := record.Plan.MatchRequest(request); err != nil {
			return true, err
		}
		fmt.Printf("resuming %s for %q: %s\n", record.Kind, request.WorkName, strings.Join(request.RepositoryIDs, ", "))
		result, err := resumeConfiguredMatchingRepositoryChange(ctx, entry.Snapshot.ChangeOperation.Path, request)
		printRepositoryChangeResult(result)
		return true, err
	}
	return false, nil
}

func printRepositoryChangeResult(result changework.Result) {
	if result.WorkName == "" {
		return
	}
	fmt.Printf("status: revision %d\n", result.Revision)
	for _, repository := range result.Repositories {
		if repository.Err != nil {
			fmt.Printf("  %-12s %s: %v\n", repository.Status, repository.ID, repository.Err)
		} else {
			fmt.Printf("  %-12s %s\n", repository.Status, repository.ID)
		}
	}
}

func runSync(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return fmt.Errorf("usage: goworktree sync <work>")
	}
	plan, err := planConfiguredSyncWork(context.Background(), args[0])
	if err != nil {
		return err
	}
	fmt.Printf("syncing %q against fetched commits\n", plan.WorkName)
	result, err := runConfiguredSyncWork(context.Background(), plan)
	if err != nil {
		return err
	}
	for _, repository := range result.Repositories {
		if repository.Err == nil {
			fmt.Printf("  %-12s %s\n", repository.Status, repository.ID)
		} else {
			fmt.Printf("  %-12s %s: %v\n", repository.Status, repository.ID, repository.Err)
		}
	}
	for _, repository := range result.Repositories {
		if repository.Status != git.SyncConflict || repository.Destination == "" {
			continue
		}
		program, openErr := openConfiguredConflict(context.Background(), tui.SyncConflictOpenRequest{
			WorkName: result.WorkName, RepositoryID: repository.ID, RepositoryPath: repository.Destination,
		})
		if openErr != nil {
			fmt.Printf("\nconflict resolver could not be opened: %v\n", openErr)
		} else {
			fmt.Printf("\nopened %s at %s\n", program, repository.Destination)
		}
		fmt.Printf("resolve conflicts, stage files with git add, then run `goworktree sync %s` again\n", result.WorkName)
		break
	}
	if result.Failed() {
		return fmt.Errorf("one or more repositories need attention; inspect the results and retry Sync Work")
	}
	return nil
}

// runLegacyBranch keeps compatibility with manifests predating typed Works.
// Current manifests are handled by the journaled Change Work workflow.
func runLegacyBranch(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	if len(args) != 2 || name == "" {
		return fmt.Errorf("usage: goworktree branch <project> <repo>")
	}

	projectDir, m, lock, err := requireManifest(cfg, name)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	if len(m.Repos) == 0 {
		return fmt.Errorf("project %q has no repositories", name)
	}

	repoID := ""
	if len(args) > 1 {
		repoID = args[1]
	}
	if repoID == "" {
		return fmt.Errorf("usage: goworktree branch <project> <repo>")
	}

	index := -1
	for i, r := range m.Repos {
		folder := r.Folder
		if folder == "" {
			folder = r.ID
		}
		if r.ID == repoID || folder == repoID {
			if index != -1 {
				return fmt.Errorf("repository %q is ambiguous; use its manifest id", repoID)
			}
			index = i
		}
	}
	if index == -1 {
		return fmt.Errorf("repository %q is not in project %q", repoID, name)
	}

	r := m.Repos[index]
	folder := r.Folder
	if folder == "" {
		folder = r.ID
	}
	wtPath, err := project.WorktreePath(projectDir, folder)
	if err != nil {
		return err
	}
	branch, err := git.CurrentBranch(wtPath)
	if err != nil {
		return fmt.Errorf("%s: read current branch: %w", folder, err)
	}
	if branch == "HEAD" {
		return fmt.Errorf("%s: detached HEAD cannot be adopted", folder)
	}
	old := r.Branch
	if !m.SetBranch(r.ID, branch) {
		return fmt.Errorf("repository %q disappeared from manifest", r.ID)
	}
	if err := m.Save(projectDir); err != nil {
		return err
	}
	if old == branch {
		fmt.Printf("%s already uses branch %q\n", folder, branch)
		return nil
	}
	if old == "" {
		fmt.Printf("%s: branch set to %q\n", folder, branch)
		return nil
	}
	fmt.Printf("%s: branch %q → %q\n", folder, old, branch)
	return nil
}

func runCursor(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	if len(args) != 1 || name == "" {
		return fmt.Errorf("usage: goworktree cursor <project>")
	}

	dir, err := project.Dir(cfg, name)
	if err != nil {
		return err
	}
	return openProgram(cfg, config.ProgramCursor, dir)
}

func runGoland(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	if len(args) != 1 || name == "" {
		return fmt.Errorf("usage: goworktree goland <project>")
	}

	dir, err := project.Dir(cfg, name)
	if err != nil {
		return err
	}
	return openProgram(cfg, config.ProgramGoland, dir)
}

func runOpen(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("usage: goworktree open <project>")
	}
	dir, err := project.Dir(cfg, args[0])
	if err != nil {
		return err
	}
	return openDefaultProgram(cfg, dir)
}

func runProgramsList() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, id := range cfg.ProgramIDs() {
		p, _ := cfg.Program(id)
		state := "disabled"
		if p.Enabled {
			state = "enabled"
		}
		def := ""
		if cfg.DefaultProgram == id {
			def = " default"
		}
		fmt.Printf("%s\t%s\t%s\t%s%s\n", id, p.Name, strings.TrimSpace(p.Path+" "+strings.Join(p.Args, " ")), state, def)
	}
	return nil
}

func runProgramsAdd(id, name, path, args string) error {
	if !config.ValidProgramID(id) {
		return fmt.Errorf("invalid program id %q (use lowercase letters, digits, hyphens, or underscores)", id)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("program name is required")
	}
	if err := config.ValidateProgramPath(path); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, exists := cfg.Program(id); exists {
		return fmt.Errorf("program %q already exists", id)
	}
	cfg.OpenWith[id] = config.Program{Name: strings.TrimSpace(name), Path: strings.TrimSpace(path), Args: strings.Fields(args), Enabled: true}
	if err := cfg.Save(); err != nil {
		return err
	}
	return nil
}

func runProgramsUpdate(id, name, path, args string, setArgs bool, enabled string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p, ok := cfg.Program(id)
	if !ok {
		return fmt.Errorf("program %q is not configured", id)
	}
	if name != "" {
		p.Name = strings.TrimSpace(name)
		if p.Name == "" {
			return fmt.Errorf("program name is required")
		}
	}
	if path != "" {
		if err := config.ValidateProgramPath(path); err != nil {
			return err
		}
		p.Path = strings.TrimSpace(path)
	}
	if setArgs {
		p.Args = strings.Fields(args)
	}
	if enabled != "" {
		v, err := strconv.ParseBool(enabled)
		if err != nil {
			return fmt.Errorf("enabled must be true or false")
		}
		p.Enabled = v
	}
	cfg.OpenWith[id] = p
	return cfg.Save()
}

func runProgramsDelete(id string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, ok := cfg.Program(id); !ok {
		return fmt.Errorf("program %q is not configured", id)
	}
	delete(cfg.OpenWith, id)
	return cfg.Save()
}

func runProgramsDefault(id string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p, ok := cfg.Program(id)
	if !ok {
		return fmt.Errorf("program %q is not configured", id)
	}
	if !p.Enabled {
		return fmt.Errorf("program %q is disabled", id)
	}
	cfg.DefaultProgram = id
	return cfg.Save()
}

func runProgramsOpen(id, name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dir, err := project.Dir(cfg, name)
	if err != nil {
		return err
	}
	return openProgram(cfg, id, dir)
}

func runProgramsSearch(query string) error {
	query = strings.ToLower(strings.TrimSpace(query))
	seen := map[string]string{}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if _, exists := seen[name]; exists || (query != "" && !strings.Contains(strings.ToLower(name), query)) {
				continue
			}
			info, err := entry.Info()
			if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
				continue
			}
			seen[name] = filepath.Join(dir, name)
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("%s\t%s\n", name, seen[name])
	}
	return nil
}

func openDefaultProgram(cfg *config.Config, dir string) error {
	if cfg.DefaultProgram == "" {
		return fmt.Errorf("no default program is enabled — add or enable one in Open with settings")
	}
	return openProgram(cfg, cfg.DefaultProgram, dir)
}

func openProgram(cfg *config.Config, id, dir string) error {
	program, ok := cfg.Program(id)
	if !ok {
		return fmt.Errorf("open-with program %q is not configured", id)
	}
	if !program.Enabled {
		return fmt.Errorf("open-with program %q is disabled", id)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	args := append(append([]string(nil), program.Args...), abs)
	cmd := exec.Command(program.Path, args...)
	cmd.Stdout = io.Discard
	if waitForLauncher(program) {
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			detail := strings.TrimSpace(stderr.String())
			if detail != "" {
				return fmt.Errorf("open %s: %w: %s", program.Name, err, detail)
			}
			return fmt.Errorf("open %s: %w", program.Name, err)
		}
		return nil
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", program.Name, err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("detach %s: %w", program.Name, err)
	}
	return nil
}

// waitForLauncher identifies commands whose only job is to hand the folder to
// another application. Waiting for these short-lived launchers lets us report
// their real exit status instead of treating a successful fork as a successful
// open operation.
func waitForLauncher(program config.Program) bool {
	name := strings.ToLower(filepath.Base(program.Path))
	if name == "open" || name == "open.exe" || name == "xdg-open" {
		return true
	}
	return (name == "codex" || name == "codex.exe") && len(program.Args) > 0 && program.Args[0] == "app"
}

func runList() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	projects, err := project.List(cfg)
	if err != nil {
		return err
	}
	fmt.Print(tui.FormatProjects(projects, cfg.ProjectsRoot))
	return nil
}

func runRemove(args []string) error {
	if len(args) < 2 || args[0] == "" {
		return fmt.Errorf("usage: goworktree remove <work> --confirm <exact-work-name>")
	}
	name, confirmation := args[0], flagValue(args[1:], "--confirm")
	if confirmation == "" {
		return fmt.Errorf("Remove Work requires --confirm <exact-work-name>")
	}
	plan, err := planConfiguredRemoveWork(context.Background(), name)
	if err != nil {
		return err
	}
	worktrees := 0
	for _, repository := range plan.Repositories {
		if !repository.BranchOnly {
			worktrees++
		}
	}
	fmt.Printf("removing %q: %d worktrees and %d local branch targets; remote refs are untouched\n", plan.WorkName, worktrees, len(plan.Repositories))
	for _, repository := range plan.Repositories {
		target := "worktree and branch"
		if repository.BranchOnly {
			target = "retained branch only"
		}
		fmt.Printf("  %s: %s — %s @ %s\n", repository.ID, target, repository.BranchRef, shortCLIRevision(repository.BranchOID))
	}
	result, err := runConfiguredRemoveWork(context.Background(), plan, confirmation)
	for _, repository := range result.Repositories {
		fmt.Printf("  %-14s %s\n", repository.Status, repository.ID)
	}
	if err != nil {
		return err
	}
	fmt.Printf("removed: %s\n", result.WorkName)
	if result.ArchiveID != "" {
		fmt.Printf("archive: %s\narchive path: %s\n", result.ArchiveID, result.ArchivePath)
	}
	return nil
}

func runArchives(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: goworktree archives <list|show|delete>")
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return fmt.Errorf("resolve control root: %w", err)
	}
	store := archive.Store{ControlRoot: controlRoot, Locks: lockops.Set{Root: controlRoot}}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("usage: goworktree archives list")
		}
		archives, err := store.List()
		if err != nil {
			return fmt.Errorf("list removed-Work archives: %w", err)
		}
		if len(archives) == 0 {
			fmt.Println("no removed-Work archives")
			return nil
		}
		for _, summary := range archives {
			fmt.Printf("%s\t%s\t%s\n", summary.Manifest.ArchiveID, summary.Manifest.WorkName, summary.Manifest.CreatedAt.UTC().Format(time.RFC3339))
		}
		return nil
	case "show":
		if len(args) != 2 || args[1] == "" {
			return fmt.Errorf("usage: goworktree archives show <archive-id>")
		}
		summary, err := store.Show(args[1])
		if err != nil {
			return fmt.Errorf("show removed-Work archive %q: %w", args[1], err)
		}
		fmt.Printf("archive: %s\nwork: %s\nwork ID: %s\nremoval ID: %s\ncreated: %s\npath: %s\nfiles:\n",
			summary.Manifest.ArchiveID, summary.Manifest.WorkName, summary.Manifest.WorkID, summary.Manifest.RemovalID,
			summary.Manifest.CreatedAt.UTC().Format(time.RFC3339), summary.Path)
		for _, file := range summary.Manifest.Files {
			fmt.Printf("  %s  %s  %d bytes\n", file.Name, file.SHA256, file.Size)
		}
		return nil
	case "delete":
		if len(args) < 2 || args[1] == "" {
			return fmt.Errorf("usage: goworktree archives delete <archive-id> --confirm <archive-id>")
		}
		id, confirmation := args[1], flagValue(args[2:], "--confirm")
		if confirmation == "" {
			return fmt.Errorf("archive deletion requires --confirm <exact-archive-id>")
		}
		if err := store.Delete(context.Background(), id, confirmation); err != nil {
			return fmt.Errorf("delete removed-Work archive %q: %w", id, err)
		}
		fmt.Printf("deleted archive: %s\n", id)
		return nil
	default:
		return fmt.Errorf("unknown archives subcommand: %s", args[0])
	}
}

func runDoctor() error {
	fmt.Println(tui.Title("goworktree doctor"))
	fmt.Println()

	checkBin := func(name string) {
		if path, err := exec.LookPath(name); err == nil {
			fmt.Printf("  ok  %-8s %s\n", name, path)
		} else {
			fmt.Printf("  miss %-8s not in PATH\n", name)
		}
	}
	checkBin("git")

	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("  miss config   %v\n", err)
		return nil
	}

	path, _ := config.Path()
	fmt.Printf("  ok  config   %s\n", path)
	fmt.Printf("  ok  repos    %s (%d configured, scan_depth=%d)\n",
		cfg.ReposRoot, len(cfg.Repos), cfg.ScanDepth)
	fmt.Printf("  ok  projects %s\n", cfg.ProjectsRoot)

	checkPath := func(label, p string) {
		if p == "" {
			fmt.Printf("  miss %-8s empty\n", label)
			return
		}
		if _, err := os.Stat(p); err == nil {
			fmt.Printf("  ok  %-8s %s\n", label, p)
			return
		}
		if _, err := exec.LookPath(p); err == nil {
			fmt.Printf("  ok  %-8s %s\n", label, p)
			return
		}
		fmt.Printf("  warn %-8s %s (not found)\n", label, p)
	}
	for _, id := range cfg.ProgramIDs() {
		p, _ := cfg.Program(id)
		checkPath(id, p.Path)
	}

	if projects, err := project.List(cfg); err == nil {
		fmt.Printf("  ok  groups   %d project(s)\n", len(projects))
	}
	return nil
}

func runRepair(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: goworktree repair <work>")
	}
	plan, err := planConfiguredRepairWork(context.Background(), args[0])
	if err != nil {
		return err
	}
	fmt.Printf("repairing %q with %d deterministic actions\n", plan.WorkName, len(plan.Actions))
	result, err := runConfiguredRepairWork(context.Background(), plan)
	if err != nil {
		return err
	}
	for _, action := range result.Actions {
		name := action.RepositoryID
		if name == "" {
			name = plan.WorkName
		}
		fmt.Printf("  %-26s %s\n", action.Kind, name)
	}
	return nil
}

func runConfigShow() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	path, _ := config.Path()
	fmt.Println(tui.FormatConfig(cfg, path))
	return nil
}

func runConfigSet(key, value string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	switch key {
	case "default_program":
		if !cfg.ProgramEnabled(value) {
			return fmt.Errorf("default_program %q is not enabled", value)
		}
		cfg.DefaultProgram = value
	case "conflict_program":
		if value == "default" {
			cfg.ConflictProgram = ""
			break
		}
		if !cfg.ProgramEnabled(value) {
			return fmt.Errorf("conflict_program %q is not enabled", value)
		}
		cfg.ConflictProgram = value
	case "repos_root":
		cfg.ReposRoot = value
	case "projects_root":
		cfg.ProjectsRoot = value
	case "default_branch":
		cfg.DefaultBranch = value
	case "scan_depth":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("scan_depth must be a positive integer")
		}
		cfg.ScanDepth = n
	case "command_timeout_seconds":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("command_timeout_seconds must be a positive integer")
		}
		cfg.CommandTimeoutSeconds = n
	default:
		return fmt.Errorf("unknown key %q", key)
	}
	return cfg.Save()
}

func runConfigEdit() error {
	if _, err := config.Load(); err != nil {
		return err
	}
	path, err := config.Path()
	if err != nil {
		return err
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runReposScan() error {
	result, err := scanConfiguredSourceRepositories(context.Background())
	if err != nil {
		return err
	}
	fmt.Println("scan complete: " + result)
	return nil
}

func runReposSet(name, path, branch string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if path == "" && branch == "" {
		return fmt.Errorf("specify --path and/or --branch")
	}

	repo, ok := cfg.Repos[name]
	if !ok {
		repo = config.Repo{}
	}
	if path != "" {
		if err := requirePathWithinReposRoot(cfg.ReposRoot, path); err != nil {
			return err
		}
		repo.Path = path
		if repo.Alias == "" {
			repo.Alias = filepath.Base(path)
		}
	}
	if branch != "" {
		repo.DefaultBranch = branch
	}
	cfg.Repos[name] = repo
	return cfg.Save()
}
