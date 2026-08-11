package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const ManifestFile = ".goworktree.json"

const (
	StatusPending  = "pending"
	StatusReady    = "ready"
	StatusFailed   = "failed"
	StatusRemoving = "removing"
)

type ManifestRepo struct {
	ID     string `json:"id"`
	Folder string `json:"folder"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type Manifest struct {
	Name      string         `json:"name"`
	CreatedAt string         `json:"created_at"`
	Repos     []ManifestRepo `json:"repos"`
}

func ManifestPath(projectDir string) string {
	return filepath.Join(projectDir, ManifestFile)
}

func LoadManifest(projectDir string) (*Manifest, error) {
	data, err := os.ReadFile(ManifestPath(projectDir))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	return &m, nil
}

func (m *Manifest) Save(projectDir string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := m.Validate(); err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}
	return atomicWriteFile(ManifestPath(projectDir), data, 0o644)
}

// Validate is backward compatible with legacy manifests whose folder is
// empty, while rejecting traversal before a manifest reaches filesystem code.
func (m *Manifest) Validate() error {
	if err := ValidateName(m.Name); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(m.Repos))
	for _, r := range m.Repos {
		folder := r.Folder
		if folder == "" {
			folder = r.ID
		}
		if err := ValidateName(folder); err != nil {
			return fmt.Errorf("repository %q folder: %w", r.ID, err)
		}
		if _, ok := seen[folder]; ok {
			return fmt.Errorf("duplicate worktree folder %q", folder)
		}
		seen[folder] = struct{}{}
	}
	return nil
}

func NewManifest(name string, repos []ManifestRepo) *Manifest {
	return &Manifest{
		Name:      name,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Repos:     repos,
	}
}

func (m *Manifest) NeedsWork() []ManifestRepo {
	var out []ManifestRepo
	for _, r := range m.Repos {
		if r.Status == StatusPending || r.Status == StatusFailed {
			out = append(out, r)
		}
	}
	return out
}

func (m *Manifest) SetStatus(id, status, errMsg string) {
	for i := range m.Repos {
		if m.Repos[i].ID == id {
			m.Repos[i].Status = status
			m.Repos[i].Error = errMsg
			return
		}
	}
}

// SetBranch updates the expected checked-out branch for one repository.
func (m *Manifest) SetBranch(id, branch string) bool {
	for i := range m.Repos {
		if m.Repos[i].ID == id {
			m.Repos[i].Branch = branch
			return true
		}
	}
	return false
}

func (m *Manifest) ReadyCount() int {
	n := 0
	for _, r := range m.Repos {
		if r.Status == StatusReady {
			n++
		}
	}
	return n
}

func (m *Manifest) Contains(id string) bool {
	for _, r := range m.Repos {
		if r.ID == id {
			return true
		}
	}
	return false
}

func (m *Manifest) IDs() []string {
	ids := make([]string, 0, len(m.Repos))
	for _, r := range m.Repos {
		ids = append(ids, r.ID)
	}
	return ids
}

func (m *Manifest) AddRepo(r ManifestRepo) {
	if m.Contains(r.ID) || m.ContainsPath(r.Path) {
		return
	}
	m.Repos = append(m.Repos, r)
}

// ContainsPath reports whether the manifest already contains the same primary
// repository. Paths are made absolute and symlinks are resolved when possible
// so legacy and current repository IDs cannot add the same Git repository twice.
func (m *Manifest) ContainsPath(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	key := repoPathKey(path)
	for _, r := range m.Repos {
		if r.Path != "" && repoPathKey(r.Path) == key {
			return true
		}
	}
	return false
}

// DeduplicateRepos removes duplicate physical repositories left by older
// manifests. A ready entry wins over a pending or failed entry because it
// describes the worktree that Git has already registered.
func (m *Manifest) DeduplicateRepos() int {
	seen := make(map[string]int, len(m.Repos))
	out := make([]ManifestRepo, 0, len(m.Repos))
	removed := 0
	for _, r := range m.Repos {
		key := repoPathKey(r.Path)
		if r.Path == "" {
			key = "id:" + r.ID
		}
		if index, ok := seen[key]; ok {
			if out[index].Status != StatusReady && r.Status == StatusReady {
				out[index] = r
			}
			removed++
			continue
		}
		seen[key] = len(out)
		out = append(out, r)
	}
	m.Repos = out
	return removed
}

func repoPathKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs)
}

func (m *Manifest) RemoveRepo(id string) (ManifestRepo, bool) {
	for i, r := range m.Repos {
		if r.ID == id {
			m.Repos = append(m.Repos[:i], m.Repos[i+1:]...)
			return r, true
		}
	}
	return ManifestRepo{}, false
}
