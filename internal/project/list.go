package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/pershin-daniil/goworktree/internal/config"
	"github.com/pershin-daniil/goworktree/internal/git"
)

type Worktree struct {
	Name   string
	Path   string
	Branch string
	Status string
}

type Entry struct {
	Name      string
	Path      string
	Repos     int
	Worktrees []Worktree
	Manifest  *Manifest
}

func List(cfg *config.Config) ([]Entry, error) {
	entries, err := os.ReadDir(cfg.ProjectsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("projects root not found: %s", cfg.ProjectsRoot)
		}
		return nil, err
	}

	var projects []Entry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(cfg.ProjectsRoot, e.Name())
		entry := loadEntry(e.Name(), path)
		projects = append(projects, entry)
	}

	if len(projects) == 0 {
		return nil, fmt.Errorf("no projects in %s — run `goworktree start`", cfg.ProjectsRoot)
	}

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Name < projects[j].Name
	})
	return projects, nil
}

func Find(cfg *config.Config, name string) (Entry, error) {
	projects, err := List(cfg)
	if err != nil {
		return Entry{}, err
	}
	for _, p := range projects {
		if p.Name == name {
			return p, nil
		}
	}
	return Entry{}, fmt.Errorf("project not found: %s", name)
}

func loadEntry(name, path string) Entry {
	if m, err := LoadManifest(path); err == nil {
		trees := make([]Worktree, 0, len(m.Repos))
		for _, r := range m.Repos {
			folder := r.Folder
			if folder == "" {
				folder = r.ID
			}
			wtPath := filepath.Join(path, folder)
			branch := r.Branch
			if git.IsRepo(wtPath) {
				if b, err := git.CurrentBranch(wtPath); err == nil {
					branch = b
				}
			}
			trees = append(trees, Worktree{
				Name:   folder,
				Path:   wtPath,
				Branch: branch,
				Status: r.Status,
			})
		}
		return Entry{
			Name:      name,
			Path:      path,
			Repos:     len(trees),
			Worktrees: trees,
			Manifest:  m,
		}
	}

	trees := listWorktrees(path)
	return Entry{
		Name:      name,
		Path:      path,
		Repos:     len(trees),
		Worktrees: trees,
	}
}

// Remove deletes every worktree in the project group, then the project folder.
func Remove(entry Entry, deleteBranches bool) error {
	targets := entry.Worktrees
	if entry.Manifest != nil {
		targets = nil
		for _, r := range entry.Manifest.Repos {
			folder := r.Folder
			if folder == "" {
				folder = r.ID
			}
			targets = append(targets, Worktree{
				Name:   folder,
				Path:   filepath.Join(entry.Path, folder),
				Branch: r.Branch,
			})
		}
	}

	for _, wt := range targets {
		if !git.IsRepo(wt.Path) {
			_ = os.RemoveAll(wt.Path)
			continue
		}
		var gitDir string
		if deleteBranches {
			var err error
			gitDir, err = git.CommonDir(wt.Path)
			if err != nil {
				return fmt.Errorf("%s: %w", wt.Name, err)
			}
		}
		branch := wt.Branch
		if err := git.RemoveWorktree(wt.Path); err != nil {
			return fmt.Errorf("%s: %w", wt.Name, err)
		}
		if deleteBranches && gitDir != "" && branch != "" && branch != "HEAD" {
			if err := git.DeleteBranch(gitDir, branch); err != nil {
				return fmt.Errorf("%s: %w", wt.Name, err)
			}
		}
	}

	return os.RemoveAll(entry.Path)
}

func listWorktrees(dir string) []Worktree {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var trees []Worktree
	for _, e := range entries {
		if !e.IsDir() || e.Name() == ManifestFile {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if !git.IsRepo(path) {
			continue
		}
		branch, _ := git.CurrentBranch(path)
		trees = append(trees, Worktree{
			Name:   e.Name(),
			Path:   path,
			Branch: branch,
			Status: StatusReady,
		})
	}

	sort.Slice(trees, func(i, j int) bool {
		return trees[i].Name < trees[j].Name
	})
	return trees
}
