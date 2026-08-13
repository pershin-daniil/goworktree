package newwork

import (
	"fmt"

	"github.com/pershin-daniil/goworktree/internal/config"
)

// CatalogFromConfig is the compatibility adapter between the current config
// schema and the new typed workflow. It has no Git or filesystem side effects.
func CatalogFromConfig(cfg *config.Config, selectedIDs []string) (Catalog, error) {
	if cfg == nil {
		return Catalog{}, fmt.Errorf("config is nil")
	}
	controlRoot, err := config.Dir()
	if err != nil {
		return Catalog{}, fmt.Errorf("resolve control root: %w", err)
	}
	catalog := Catalog{
		WorksRoot:    cfg.ProjectsRoot,
		ControlRoot:  controlRoot,
		Repositories: make(map[string]Repository, len(selectedIDs)),
	}
	for _, id := range selectedIDs {
		path, ok := cfg.RepoPath(id)
		if !ok {
			return Catalog{}, fmt.Errorf("repository %q is not configured", id)
		}
		catalog.Repositories[id] = Repository{
			ID:             id,
			SourcePath:     path,
			Folder:         cfg.FolderName(id, selectedIDs),
			Remote:         cfg.RepoRemote(id),
			BasePreference: cfg.RepoBranch(id),
		}
	}
	return catalog, nil
}
