package project

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pershin-daniil/goworktree/internal/config"
	"github.com/pershin-daniil/goworktree/internal/git"
)

type RepairResult struct {
	Deduplicated int
	Missing      int
	Pruned       int
	Recovered    bool
}

// Repair reconciles user state with the filesystem. Invalid manifests are
// preserved under a timestamped name before rebuilding from registered folders.
func Repair(cfg *config.Config, projectDir, name string) (*Manifest, RepairResult, error) {
	var result RepairResult
	m, err := LoadManifest(projectDir)
	if err != nil {
		manifestPath := ManifestPath(projectDir)
		if _, statErr := os.Stat(manifestPath); statErr == nil {
			backup := manifestPath + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
			if renameErr := os.Rename(manifestPath, backup); renameErr != nil {
				return nil, result, fmt.Errorf("preserve corrupt manifest: %w", renameErr)
			}
			result.Recovered = true
		}
		m, err = MigrateManifest(cfg, projectDir, name)
		if err != nil {
			return nil, result, err
		}
	}
	result.Deduplicated = m.DeduplicateRepos()
	for i := range m.Repos {
		folder := m.Repos[i].Folder
		if folder == "" {
			folder = m.Repos[i].ID
		}
		wtPath, err := WorktreePath(projectDir, folder)
		if err != nil {
			return nil, result, err
		}
		if !git.IsRepo(wtPath) {
			m.Repos[i].Status = StatusFailed
			m.Repos[i].Error = "worktree missing; rerun start to recreate it"
			result.Missing++
		}
		if m.Repos[i].Path != "" && git.IsRepo(m.Repos[i].Path) {
			if err := git.PruneWorktrees(m.Repos[i].Path); err != nil {
				return nil, result, fmt.Errorf("prune %s: %w", m.Repos[i].ID, err)
			}
			result.Pruned++
		}
	}
	if err := m.Save(projectDir); err != nil {
		return nil, result, err
	}
	return m, result, nil
}

// CorruptManifestBackups returns preserved manifests for diagnostic tooling.
func CorruptManifestBackups(projectDir string) ([]string, error) {
	return filepath.Glob(ManifestPath(projectDir) + ".corrupt-*")
}
