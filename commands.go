package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pershin-daniil/goworktree/internal/config"
	"github.com/pershin-daniil/goworktree/internal/cursor"
	"github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/goland"
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

	openCursor := false
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--open" {
			openCursor = true
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
	if name == "" {
		return fmt.Errorf("usage: goworktree start <name> --repos id,id [--open]")
	}

	projectDir := filepath.Join(cfg.ProjectsRoot, name)

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
			if openCursor {
				return cursor.OpenFolder(cfg.CursorPath, projectDir)
			}
			return nil
		}
		fmt.Printf("resuming %q (%d pending)\n", name, len(pending))
		tasks := tui.TasksFromManifest(projectDir, m, true)
		if err := runCreateTasks(projectDir, m, tasks); err != nil {
			return err
		}
		if openCursor {
			return cursor.OpenFolder(cfg.CursorPath, projectDir)
		}
		fmt.Printf("\ndone: %s\n", projectDir)
		return nil
	}

	if len(cfg.RepoNames()) == 0 {
		return fmt.Errorf("no repositories — run `goworktree repos scan`")
	}

	selected := csvFlag(args, "--repos")
	if len(selected) == 0 {
		return fmt.Errorf("usage: goworktree start <name> --repos id,id [--open]")
	}

	if err := os.MkdirAll(projectDir, 0o755); err != nil {
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

	if openCursor {
		if err := cursor.OpenFolder(cfg.CursorPath, projectDir); err != nil {
			return err
		}
	}

	fmt.Printf("\ndone: %s\n", projectDir)
	return nil
}

func pickProject(cfg *config.Config, title string) (string, error) {
	projects, err := project.List(cfg)
	if err != nil {
		return "", err
	}
	if len(projects) == 0 {
		return "", fmt.Errorf("no projects found")
	}

	items := make([]tui.Item, 0, len(projects))
	for _, p := range projects {
		meta := fmt.Sprintf("%d repos", p.Repos)
		if p.Manifest != nil {
			ready := p.Manifest.ReadyCount()
			total := len(p.Manifest.Repos)
			if ready < total {
				meta = fmt.Sprintf("%d/%d ready", ready, total)
			}
		}
		items = append(items, tui.Item{
			ID:    p.Name,
			Title: p.Name,
			Desc:  meta,
		})
	}

	return tui.Pick(title, items)
}

func requireManifest(cfg *config.Config, name string) (projectDir string, m *project.Manifest, err error) {
	projectDir, err = project.Dir(cfg, name)
	if err != nil {
		return "", nil, err
	}
	hadManifest := false
	if _, e := project.LoadManifest(projectDir); e == nil {
		hadManifest = true
	}
	m, err = project.EnsureManifest(cfg, projectDir, name)
	if err != nil {
		return "", nil, err
	}
	if !hadManifest {
		fmt.Printf("migrated manifest for %q (%d repos)\n", name, len(m.Repos))
	}
	if removed := m.DeduplicateRepos(); removed > 0 {
		if err := m.Save(projectDir); err != nil {
			return "", nil, fmt.Errorf("repair duplicate repositories in manifest: %w", err)
		}
		fmt.Printf("repaired %q: removed %d duplicate repository entry(s)\n", name, removed)
	}
	return projectDir, m, nil
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

	projectDir, m, err := requireManifest(cfg, name)
	if err != nil {
		return err
	}
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
		dest := filepath.Join(projectDir, folder)
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
	if name == "" {
		return fmt.Errorf("usage: goworktree drop <project> --repos id,id [-D] [--yes]")
	}

	projectDir, m, err := requireManifest(cfg, name)
	if err != nil {
		return err
	}
	if len(m.Repos) == 0 {
		return fmt.Errorf("project %q has no repositories", name)
	}

	selected := csvFlag(args, "--repos")
	if len(selected) == 0 {
		return fmt.Errorf("usage: goworktree drop <project> --repos id,id [-D] [--yes]")
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
		wtPath := filepath.Join(projectDir, folder)
		fmt.Printf("  dropping %s\n", folder)

		if git.IsRepo(wtPath) {
			var gitDir string
			if deleteBranches {
				gitDir, err = git.CommonDir(wtPath)
				if err != nil {
					return fmt.Errorf("%s: %w", folder, err)
				}
			}
			branch := r.Branch
			if err := git.RemoveWorktree(wtPath); err != nil {
				return fmt.Errorf("%s: %w", folder, err)
			}
			if deleteBranches && gitDir != "" && branch != "" && branch != "HEAD" {
				if err := git.DeleteBranch(gitDir, branch); err != nil {
					return fmt.Errorf("%s: %w", folder, err)
				}
			}
		} else {
			_ = os.RemoveAll(wtPath)
		}
	}

	if err := m.Save(projectDir); err != nil {
		return err
	}
	fmt.Printf("\ndropped from %s (%d repos left)\n", m.Name, len(m.Repos))
	return nil
}

func runSync(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	if name == "" {
		return fmt.Errorf("usage: goworktree sync <project>")
	}

	projectDir, m, err := requireManifest(cfg, name)
	if err != nil {
		return err
	}
	if len(m.Repos) == 0 {
		return fmt.Errorf("project %q has no repositories", name)
	}

	counts := map[git.SyncStatus]int{}
	failed := false
	fmt.Printf("syncing %q\n", name)
	for _, r := range m.Repos {
		folder := r.Folder
		if folder == "" {
			folder = r.ID
		}
		wtPath := filepath.Join(projectDir, folder)
		result := git.SyncWorktree(wtPath, r.Branch, r.Base)
		counts[result.Status]++
		switch result.Status {
		case git.SyncRebased:
			fmt.Printf("  rebased      %s\n", folder)
		case git.SyncUpToDate:
			fmt.Printf("  up-to-date   %s\n", folder)
		case git.SyncRolledBack:
			failed = true
			fmt.Printf("  rolled-back  %s: %v\n", folder, result.Err)
		default:
			failed = true
			fmt.Printf("  failed       %s: %v\n", folder, result.Err)
		}
	}

	fmt.Printf("\nsummary: %d rebased, %d up-to-date, %d rolled back, %d failed\n",
		counts[git.SyncRebased], counts[git.SyncUpToDate], counts[git.SyncRolledBack],
		counts[git.SyncFailed])
	if failed {
		return fmt.Errorf("one or more repositories could not be synchronized")
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
	if name == "" {
		return fmt.Errorf("usage: goworktree branch <project> <repo>")
	}

	projectDir, m, err := requireManifest(cfg, name)
	if err != nil {
		return err
	}
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
	branch, err := git.CurrentBranch(filepath.Join(projectDir, folder))
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
	if name == "" {
		return fmt.Errorf("usage: goworktree cursor <project>")
	}

	dir, err := project.Dir(cfg, name)
	if err != nil {
		return err
	}
	return cursor.OpenFolder(cfg.CursorPath, dir)
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
	if name == "" {
		return fmt.Errorf("usage: goworktree goland <project>")
	}

	dir, err := project.Dir(cfg, name)
	if err != nil {
		return err
	}
	return goland.OpenFolder(cfg.GolandPath, dir)
}

func runOpen(args []string) error {
	return runCursor(args)
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	deleteBranches := false
	assumeYes := false
	var positional []string
	for _, arg := range args {
		switch arg {
		case "--delete-branches", "-D":
			deleteBranches = true
		case "--yes":
			assumeYes = true
		default:
			positional = append(positional, arg)
		}
	}

	name := ""
	if len(positional) > 0 {
		name = positional[0]
	}
	if name == "" {
		return fmt.Errorf("usage: goworktree remove <project> [-D] [--yes]")
	}

	entry, err := project.Find(cfg, name)
	if err != nil {
		return err
	}

	msg := fmt.Sprintf("Remove project %q?\n\n%s\n%d worktree(s) will be deleted.",
		entry.Name, entry.Path, len(entry.Worktrees))
	if deleteBranches {
		msg += "\n\nAlso deletes LOCAL project branches (-D). Remote branches are not touched."
	}
	if !assumeYes {
		return fmt.Errorf("refusing destructive operation without --yes: %s", strings.ReplaceAll(msg, "\n", " "))
	}

	for _, wt := range entry.Worktrees {
		fmt.Printf("  removing %s\n", wt.Name)
	}
	if err := project.Remove(entry, deleteBranches); err != nil {
		return err
	}
	fmt.Printf("\nremoved: %s\n", entry.Name)
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
		if _, err := os.Stat(p); err == nil || p == "cursor" || p == "goland" {
			fmt.Printf("  ok  %-8s %s\n", label, p)
			return
		}
		if _, err := exec.LookPath(p); err == nil {
			fmt.Printf("  ok  %-8s %s\n", label, p)
			return
		}
		fmt.Printf("  warn %-8s %s (not found)\n", label, p)
	}
	checkPath("cursor", cfg.CursorPath)
	checkPath("goland", cfg.GolandPath)

	if projects, err := project.List(cfg); err == nil {
		fmt.Printf("  ok  groups   %d project(s)\n", len(projects))
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
	case "cursor_path":
		cfg.CursorPath = value
	case "goland_path":
		cfg.GolandPath = value
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
