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
	for _, arg := range args {
		if arg == "--open" {
			openCursor = true
			continue
		}
		positional = append(positional, arg)
	}

	name := ""
	if len(positional) > 0 {
		name = positional[0]
	} else {
		name, err = tui.AskProjectName()
		if err != nil {
			return err
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("project name is required")
	}

	projectDir := filepath.Join(cfg.ProjectsRoot, name)

	// Resume path: existing incomplete manifest.
	if m, err := project.LoadManifest(projectDir); err == nil {
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
		if err := tui.RunCreate(projectDir, m, tasks, true); err != nil {
			return err
		}
		if openCursor {
			return cursor.OpenFolder(cfg.CursorPath, projectDir)
		}
		fmt.Printf("\ndone: %s\n", projectDir)
		return nil
	}

	ids := cfg.RepoNames()
	sort.Strings(ids)
	if len(ids) == 0 {
		return fmt.Errorf("no repositories — run `goworktree repos scan`")
	}

	items := make([]tui.Item, 0, len(ids))
	for _, id := range ids {
		base := cfg.RepoBranch(id)
		items = append(items, tui.Item{
			ID:    id,
			Title: cfg.DisplayName(id),
			Desc:  fmt.Sprintf("%s → %s", base, name),
		})
	}

	selected, err := tui.PickMulti("Select repositories", items)
	if err != nil {
		return err
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
	if err := tui.RunCreate(projectDir, m, tasks, true); err != nil {
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
	return projectDir, m, nil
}

func runAdd(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	if name == "" {
		name, err = pickProject(cfg, "Add to project")
		if err != nil {
			return err
		}
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
		candidates = append(candidates, id)
	}
	sort.Strings(candidates)
	if len(candidates) == 0 {
		return fmt.Errorf("no more repositories to add — all configured repos are already in the project")
	}

	items := make([]tui.Item, 0, len(candidates))
	for _, id := range candidates {
		base := cfg.RepoBranch(id)
		items = append(items, tui.Item{
			ID:    id,
			Title: cfg.DisplayName(id),
			Desc:  fmt.Sprintf("%s → %s", base, m.Name),
		})
	}

	selected, err := tui.PickMulti("Select repositories to add", items)
	if err != nil {
		return err
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
	if err := tui.RunCreate(projectDir, m, tasks, false); err != nil {
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
	var positional []string
	for _, arg := range args {
		switch arg {
		case "--delete-branches", "-D":
			deleteBranches = true
		default:
			positional = append(positional, arg)
		}
	}

	name := ""
	if len(positional) > 0 {
		name = positional[0]
	}
	if name == "" {
		name, err = pickProject(cfg, "Drop from project")
		if err != nil {
			return err
		}
	}

	projectDir, m, err := requireManifest(cfg, name)
	if err != nil {
		return err
	}
	if len(m.Repos) == 0 {
		return fmt.Errorf("project %q has no repositories", name)
	}

	items := make([]tui.Item, 0, len(m.Repos))
	for _, r := range m.Repos {
		folder := r.Folder
		if folder == "" {
			folder = r.ID
		}
		items = append(items, tui.Item{
			ID:    r.ID,
			Title: folder,
			Desc:  r.Branch,
		})
	}

	selected, err := tui.PickMulti("Select repositories to drop", items)
	if err != nil {
		return err
	}

	msg := fmt.Sprintf("Drop %d repo(s) from project %q?\n\nProject folder stays; other worktrees are untouched.",
		len(selected), m.Name)
	if deleteBranches {
		msg += "\n\nAlso deletes LOCAL project branches (-D). Remote branches are not touched."
	}
	ok, err := tui.Confirm(msg)
	if err != nil {
		return err
	}
	if !ok {
		return tui.ErrCancelled
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
		name, err = pickProject(cfg, "Open in Cursor")
		if err != nil {
			return err
		}
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
		name, err = pickProject(cfg, "Open in GoLand")
		if err != nil {
			return err
		}
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
	var positional []string
	for _, arg := range args {
		switch arg {
		case "--delete-branches", "-D":
			deleteBranches = true
		default:
			positional = append(positional, arg)
		}
	}

	name := ""
	if len(positional) > 0 {
		name = positional[0]
	}
	if name == "" {
		name, err = pickProject(cfg, "Remove project")
		if err != nil {
			return err
		}
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
	ok, err := tui.Confirm(msg)
	if err != nil {
		return err
	}
	if !ok {
		return tui.ErrCancelled
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
