package project

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pershin-daniil/goworktree/internal/config"
)

func Dir(cfg *config.Config, name string) (string, error) {
	dir := filepath.Join(cfg.ProjectsRoot, name)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("project not found: %s", dir)
	}
	return filepath.Abs(dir)
}
