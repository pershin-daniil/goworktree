package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pershin-daniil/goworktree/internal/config"
	"github.com/pershin-daniil/goworktree/internal/git"
)

// EnsureManifest loads .goworktree.json or migrates it from existing worktree folders.
func EnsureManifest(cfg *config.Config, projectDir, name string) (*Manifest, error) {
	if m, err := LoadManifest(projectDir); err == nil {
		return m, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	m, err := MigrateManifest(cfg, projectDir, name)
	if err != nil {
		return nil, err
	}
	if err := m.Save(projectDir); err != nil {
		return nil, err
	}
	return m, nil
}

// MigrateManifest builds a manifest from directories that are git worktrees under projectDir.
func MigrateManifest(cfg *config.Config, projectDir, name string) (*Manifest, error) {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil, err
	}

	var repos []ManifestRepo
	for _, e := range entries {
		if !e.IsDir() || e.Name() == ManifestFile {
			continue
		}
		wtPath := filepath.Join(projectDir, e.Name())
		if !git.IsRepo(wtPath) {
			continue
		}

		mainPath, err := git.MainRepo(wtPath)
		if err != nil {
			return nil, fmt.Errorf("%s: resolve main repo: %w", e.Name(), err)
		}
		id, ok := cfg.FindRepoID(mainPath)
		if !ok {
			// synthesize id from relative path / basename so drop/add can still work
			id = config.RepoIDFromPath(cfg.ReposRoot, mainPath)
			if _, exists := cfg.Repos[id]; !exists {
				base, berr := git.DefaultBranch(mainPath)
				if berr != nil {
					base = cfg.DefaultBranch
				}
				cfg.Repos[id] = config.Repo{
					Path:          mainPath,
					DefaultBranch: base,
					Alias:         filepath.Base(mainPath),
				}
				_ = cfg.Save()
			}
		}

		branch, err := git.CurrentBranch(wtPath)
		if err != nil {
			branch = name
		}
		base, rerr := git.ResolveBaseBranch(mainPath, cfg.RepoBranch(id))
		if rerr != nil {
			base = cfg.RepoBranch(id)
		}
		repos = append(repos, ManifestRepo{
			ID:     id,
			Folder: e.Name(),
			Path:   mainPath,
			Branch: branch,
			Base:   base,
			Status: StatusReady,
		})
	}

	if len(repos) == 0 {
		return nil, fmt.Errorf("project %q has no git worktrees to migrate", name)
	}
	return NewManifest(name, repos), nil
}
