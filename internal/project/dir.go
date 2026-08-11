package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pershin-daniil/goworktree/internal/config"
)

func ValidateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) || filepath.IsAbs(name) {
		return fmt.Errorf("invalid project name %q", name)
	}
	return nil
}

// SafeJoin accepts one path component and verifies it remains below root.
func SafeJoin(root, child string) (string, error) {
	if err := ValidateName(child); err != nil {
		return "", err
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(base, child)
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root: %q", child)
	}
	return path, nil
}

func Path(cfg *config.Config, name string) (string, error) { return SafeJoin(cfg.ProjectsRoot, name) }

func WorktreePath(projectDir, folder string) (string, error) { return SafeJoin(projectDir, folder) }

func Dir(cfg *config.Config, name string) (string, error) {
	dir, err := Path(cfg, name)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("project not found: %s", dir)
	}
	return filepath.Abs(dir)
}
