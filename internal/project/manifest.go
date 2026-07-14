package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const ManifestFile = ".goworktree.json"

const (
	StatusPending = "pending"
	StatusReady   = "ready"
	StatusFailed  = "failed"
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
	return &m, nil
}

func (m *Manifest) Save(projectDir string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(ManifestPath(projectDir), data, 0o644)
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
	if m.Contains(r.ID) {
		return
	}
	m.Repos = append(m.Repos, r)
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
