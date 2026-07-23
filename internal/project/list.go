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
		seen := make(map[string]struct{}, len(m.Repos))
		for _, r := range m.Repos {
			folder := r.Folder
			if folder == "" {
				folder = r.ID
			}
			wtPath, pathErr := WorktreePath(path, folder)
			if pathErr != nil {
				continue
			}
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
			seen[filepath.Clean(wtPath)] = struct{}{}
		}
		// A manifest is resumable state, not an authority over Git. Include
		// worktrees found on disk as well so remove can clean up leftovers from
		// interrupted creates and older manifests.
		for _, wt := range listWorktrees(path) {
			if _, ok := seen[filepath.Clean(wt.Path)]; !ok {
				trees = append(trees, wt)
			}
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
	mark := func(wt Worktree, status, message string) {
		if entry.Manifest == nil {
			return
		}
		for _, r := range entry.Manifest.Repos {
			folder := r.Folder
			if folder == "" {
				folder = r.ID
			}
			if folder == wt.Name {
				entry.Manifest.SetStatus(r.ID, status, message)
				_ = entry.Manifest.Save(entry.Path)
				return
			}
		}
	}
	targets := append([]Worktree(nil), entry.Worktrees...)
	if entry.Manifest != nil {
		seen := make(map[string]struct{}, len(targets))
		for _, wt := range targets {
			seen[filepath.Clean(wt.Path)] = struct{}{}
		}
		for _, r := range entry.Manifest.Repos {
			folder := r.Folder
			if folder == "" {
				folder = r.ID
			}
			wtPath, err := WorktreePath(entry.Path, folder)
			if err != nil {
				return err
			}
			wt := Worktree{
				Name:   folder,
				Path:   wtPath,
				Branch: r.Branch,
			}
			if _, ok := seen[filepath.Clean(wt.Path)]; !ok {
				targets = append(targets, wt)
				seen[filepath.Clean(wt.Path)] = struct{}{}
			}
		}
	}

	for _, wt := range targets {
		mark(wt, StatusRemoving, "")
		if !git.IsRepo(wt.Path) {
			if err := os.RemoveAll(wt.Path); err != nil {
				mark(wt, StatusFailed, err.Error())
				return fmt.Errorf("%s: %w", wt.Name, err)
			}
			continue
		}
		var gitDir string
		if deleteBranches {
			var err error
			gitDir, err = git.CommonDir(wt.Path)
			if err != nil {
				mark(wt, StatusFailed, err.Error())
				return fmt.Errorf("%s: %w", wt.Name, err)
			}
		}
		branch := wt.Branch
		if err := git.RemoveWorktree(wt.Path); err != nil {
			mark(wt, StatusFailed, err.Error())
			return fmt.Errorf("%s: %w", wt.Name, err)
		}
		if deleteBranches && gitDir != "" && branch != "" && branch != "HEAD" {
			if err := git.DeleteBranch(gitDir, branch); err != nil {
				mark(wt, StatusFailed, err.Error())
				return fmt.Errorf("%s: %w", wt.Name, err)
			}
		}
	}

	// A broken linked worktree may no longer be usable as a Git command
	// directory, so RemoveWorktree cannot determine its common Git directory.
	// Prune every repository named by the manifest as a final cleanup pass.
	if entry.Manifest != nil {
		for _, r := range entry.Manifest.Repos {
			if r.Path == "" || !git.IsRepo(r.Path) {
				continue
			}
			if err := git.PruneWorktrees(r.Path); err != nil {
				return fmt.Errorf("prune %s: %w", r.ID, err)
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
		path, err := WorktreePath(dir, e.Name())
		if err != nil {
			continue
		}
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
