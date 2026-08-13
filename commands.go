package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pershin-daniil/goworktree/internal/config"
	"github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/project"
	"github.com/pershin-daniil/goworktree/internal/tui"
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

// runCreateTasks is the command-mode equivalent of the interactive progress
// screen. It keeps resumable manifest state while writing normal CLI output.
func runCreateTasks(projectDir string, m *project.Manifest, tasks []tui.CreateTask) error {
	for _, task := range tasks {
		fmt.Printf("  creating %s\n", task.Folder)
		if err := git.AddProjectWorktree(task.RepoPath, task.Dest, task.Branch, task.BaseBranch); err != nil {
			m.SetStatus(task.ID, project.StatusFailed, err.Error())
			_ = m.Save(projectDir)
			return fmt.Errorf("%s: %w", task.Folder, err)
		}
		m.SetStatus(task.ID, project.StatusReady, "")
		if err := m.Save(projectDir); err != nil {
			return fmt.Errorf("save manifest: %w", err)
		}
	}
	return nil
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
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--open" {
			openDefault = true
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
		return fmt.Errorf("usage: goworktree start <name> --repos id,id [--open]")
	}
	projectDir, err := project.Path(cfg, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return err
	}
	lock, err := project.AcquireLock(projectDir)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	// Resume path: existing incomplete manifest.
	if m, err := project.LoadManifest(projectDir); err == nil {
		if removed := m.DeduplicateRepos(); removed > 0 {
			if err := m.Save(projectDir); err != nil {
				return fmt.Errorf("repair duplicate repositories in manifest: %w", err)
			}
			fmt.Printf("repaired %q: removed %d duplicate repository entry(s)\n", name, removed)
		}
		pending := m.NeedsWork()
		if len(pending) == 0 {
			fmt.Printf("project %q already complete: %s\n", name, projectDir)
			if openDefault {
				return openDefaultProgram(cfg, projectDir)
			}
			return nil
		}
		fmt.Printf("resuming %q (%d pending)\n", name, len(pending))
		tasks := tui.TasksFromManifest(projectDir, m, true)
		if err := runCreateTasks(projectDir, m, tasks); err != nil {
			return err
		}
		if openDefault {
			return openDefaultProgram(cfg, projectDir)
		}
		fmt.Printf("\ndone: %s\n", projectDir)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load manifest: %w (run `goworktree repair %s` to recover)", err, name)
	}

	if len(cfg.RepoNames()) == 0 {
		return fmt.Errorf("no repositories — run `goworktree repos scan`")
	}

	selected := csvFlag(args, "--repos")
	if len(selected) == 0 {
		return fmt.Errorf("usage: goworktree start <name> --repos id,id [--open]")
	}
	if err := uniqueIDs(selected); err != nil {
		return err
	}

	repos := make([]project.ManifestRepo, 0, len(selected))
	for _, id := range selected {
		repoPath, ok := cfg.RepoPath(id)
		if !ok {
			return fmt.Errorf("unknown repo %q", id)
		}
		preferred := cfg.RepoBranch(id)
		base, err := git.ResolveBaseBranch(repoPath, preferred)
		if err != nil {
			return fmt.Errorf("%s: %w", cfg.DisplayName(id), err)
		}
		if preferred != "" && !git.SameBranchRef(preferred, base) {
			fmt.Printf("  %s: base %q not found, using %s\n", cfg.DisplayName(id), preferred, base)
		}
		folder := cfg.FolderName(id, selected)
		repos = append(repos, project.ManifestRepo{
			ID:     id,
			Folder: folder,
			Path:   repoPath,
			Branch: name,
			Base:   base,
			Status: project.StatusPending,
		})
	}

	m := project.NewManifest(name, repos)
	if err := m.Save(projectDir); err != nil {
		return err
	}

	tasks := tui.TasksFromManifest(projectDir, m, true)
	if err := runCreateTasks(projectDir, m, tasks); err != nil {
		return err
	}

	if openDefault {
		if err := openDefaultProgram(cfg, projectDir); err != nil {
			return err
		}
	}

	fmt.Printf("\ndone: %s\n", projectDir)
	return nil
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	name := ""
	for i := 0; i < len(args); i++ {
		if isFlagWithValue(args, i, "--repos") {
			if args[i] == "--repos" {
				i++
			}
			continue
		}
		name = args[i]
		break
	}
	if name == "" {
		return fmt.Errorf("usage: goworktree add <project> --repos id,id")
	}

	projectDir, m, lock, err := requireManifest(cfg, name)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	inProject := map[string]struct{}{}
	for _, id := range m.IDs() {
		inProject[id] = struct{}{}
	}

	candidates := make([]string, 0)
	for _, id := range cfg.RepoNames() {
		if _, ok := inProject[id]; ok {
			continue
		}
		if repoPath, ok := cfg.RepoPath(id); ok && m.ContainsPath(repoPath) {
			continue
		}
		candidates = append(candidates, id)
	}
	sort.Strings(candidates)
	if len(candidates) == 0 {
		return fmt.Errorf("no more repositories to add — all configured repos are already in the project")
	}

	selected := csvFlag(args, "--repos")
	if len(selected) == 0 {
		return fmt.Errorf("usage: goworktree add <project> --repos id,id")
	}
	if err := uniqueIDs(selected); err != nil {
		return err
	}
	allowed := make(map[string]bool, len(candidates))
	for _, id := range candidates {
		allowed[id] = true
	}
	for _, id := range selected {
		if !allowed[id] {
			return fmt.Errorf("repository %q cannot be added to project %q", id, name)
		}
	}

	folderPool := append(m.IDs(), selected...)
	for _, id := range selected {
		repoPath, ok := cfg.RepoPath(id)
		if !ok {
			return fmt.Errorf("unknown repo %q", id)
		}
		preferred := cfg.RepoBranch(id)
		base, err := git.ResolveBaseBranch(repoPath, preferred)
		if err != nil {
			return fmt.Errorf("%s: %w", cfg.DisplayName(id), err)
		}
		if preferred != "" && !git.SameBranchRef(preferred, base) {
			fmt.Printf("  %s: base %q not found, using %s\n", cfg.DisplayName(id), preferred, base)
		}
		folder := cfg.FolderName(id, folderPool)
		dest, err := project.WorktreePath(projectDir, folder)
		if err != nil {
			return err
		}
		if _, err := os.Stat(dest); err == nil {
			return fmt.Errorf("destination already exists: %s", dest)
		}
		m.AddRepo(project.ManifestRepo{
			ID:     id,
			Folder: folder,
			Path:   repoPath,
			Branch: m.Name,
			Base:   base,
			Status: project.StatusPending,
		})
	}

	if err := m.Save(projectDir); err != nil {
		return err
	}

	tasks := tui.TasksFromManifest(projectDir, m, true)
	if err := runCreateTasks(projectDir, m, tasks); err != nil {
		return err
	}

	fmt.Printf("\nadded to %s\n", projectDir)
	return nil
}

func runDrop(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	deleteBranches := false
	assumeYes := false
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--delete-branches", "-D":
			deleteBranches = true
		case "--yes":
			assumeYes = true
		default:
			if isFlagWithValue(args, i, "--repos") {
				if arg == "--repos" {
					i++
				}
				continue
			}
			positional = append(positional, arg)
		}
	}

	name := ""
	if len(positional) > 0 {
		name = positional[0]
	}
	if len(positional) != 1 || name == "" {
		return fmt.Errorf("usage: goworktree drop <project> --repos id,id [-D] [--yes]")
	}

	projectDir, m, lock, err := requireManifest(cfg, name)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	if len(m.Repos) == 0 {
		return fmt.Errorf("project %q has no repositories", name)
	}

	selected := csvFlag(args, "--repos")
	if len(selected) == 0 {
		return fmt.Errorf("usage: goworktree drop <project> --repos id,id [-D] [--yes]")
	}
	if err := uniqueIDs(selected); err != nil {
		return err
	}

	msg := fmt.Sprintf("Drop %d repo(s) from project %q?\n\nProject folder stays; other worktrees are untouched.",
		len(selected), m.Name)
	if deleteBranches {
		msg += "\n\nAlso deletes LOCAL project branches (-D). Remote branches are not touched."
	}
	if !assumeYes {
		return fmt.Errorf("refusing destructive operation without --yes: %s", strings.ReplaceAll(msg, "\n", " "))
	}

	for _, id := range selected {
		r, ok := m.RemoveRepo(id)
		if !ok {
			continue
		}
		folder := r.Folder
		if folder == "" {
			folder = r.ID
		}
		wtPath, err := project.WorktreePath(projectDir, folder)
		if err != nil {
			return err
		}
		fmt.Printf("  dropping %s\n", folder)
		m.AddRepo(r)
		m.SetStatus(id, project.StatusRemoving, "")
		if err := m.Save(projectDir); err != nil {
			return fmt.Errorf("save manifest: %w", err)
		}

		if git.IsRepo(wtPath) {
			var gitDir string
			if deleteBranches {
				gitDir, err = git.CommonDir(wtPath)
				if err != nil {
					m.SetStatus(id, project.StatusFailed, err.Error())
					_ = m.Save(projectDir)
					return fmt.Errorf("%s: %w", folder, err)
				}
			}
			branch := r.Branch
			if err := git.RemoveWorktree(wtPath); err != nil {
				m.SetStatus(id, project.StatusFailed, err.Error())
				_ = m.Save(projectDir)
				return fmt.Errorf("%s: %w", folder, err)
			}
			if deleteBranches && gitDir != "" && branch != "" && branch != "HEAD" {
				if err := git.DeleteBranch(gitDir, branch); err != nil {
					m.SetStatus(id, project.StatusFailed, err.Error())
					_ = m.Save(projectDir)
					return fmt.Errorf("%s: %w", folder, err)
				}
			}
		} else {
			if err := os.RemoveAll(wtPath); err != nil {
				m.SetStatus(id, project.StatusFailed, err.Error())
				_ = m.Save(projectDir)
				return fmt.Errorf("%s: %w", folder, err)
			}
		}
		m.RemoveRepo(id)
		if err := m.Save(projectDir); err != nil {
			return fmt.Errorf("save manifest: %w", err)
		}
	}
	fmt.Printf("\ndropped from %s (%d repos left)\n", m.Name, len(m.Repos))
	return nil
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
	if result.Failed() {
		return fmt.Errorf("one or more repositories need attention; inspect the results and retry Sync Work")
	}
	return nil
}

// runBranch adopts the branch currently checked out in one project worktree.
// The manifest remains explicit per repository while sync can keep rejecting an
// accidental checkout instead of rebasing an unexpected branch.
func runBranch(args []string) error {
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
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", program.Name, err)
	}
	return nil
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
	fmt.Printf("removing %q: %d worktrees and local branches; remote refs are untouched\n", plan.WorkName, len(plan.Repositories))
	result, err := runConfiguredRemoveWork(context.Background(), plan, confirmation)
	for _, repository := range result.Repositories {
		fmt.Printf("  %-14s %s\n", repository.Status, repository.ID)
	}
	if err != nil {
		return err
	}
	fmt.Printf("removed: %s\n", result.WorkName)
	return nil
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	scanned, err := git.ScanRepos(cfg.ReposRoot, cfg.ScanDepth)
	if err != nil {
		return err
	}

	added := 0
	updated := 0
	for _, r := range scanned {
		id := config.RepoIDFromPath(cfg.ReposRoot, r.Path)

		// Reuse existing entry that already points at this path (legacy basename keys).
		existingID := id
		for otherID, other := range cfg.Repos {
			if other.Path == r.Path {
				existingID = otherID
				break
			}
		}

		if existing, exists := cfg.Repos[existingID]; exists {
			existing.Path = r.Path
			if existing.Alias == "" {
				existing.Alias = r.Alias
			}
			cfg.Repos[existingID] = existing
			// Migrate legacy basename key → stable id
			if existingID != id {
				cfg.Repos[id] = cfg.Repos[existingID]
				delete(cfg.Repos, existingID)
				updated++
			}
			continue
		}

		branch, err := git.DefaultBranch(r.Path)
		if err != nil {
			branch = cfg.DefaultBranch
		}
		cfg.Repos[id] = config.Repo{
			Path:          r.Path,
			DefaultBranch: branch,
			Alias:         r.Alias,
		}
		added++
	}

	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("scan complete: %d new, %d migrated, %d total (depth=%d)\n",
		added, updated, len(cfg.Repos), cfg.ScanDepth)
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
