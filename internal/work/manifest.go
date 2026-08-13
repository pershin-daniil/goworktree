package work

import (
	"encoding/hex"
	"fmt"
	"path"
	"strings"
	"time"
)

const ManifestSchemaVersion = 1

type Manifest struct {
	SchemaVersion      int                `json:"schema_version"`
	WorkID             Identity           `json:"work_id"`
	NewWorkOperationID string             `json:"new_work_operation_id"`
	Name               Name               `json:"name"`
	CreatedAt          time.Time          `json:"created_at"`
	Repositories       []RepositoryIntent `json:"repositories"`
	Harness            HarnessIntent      `json:"harness"`
}

type RepositoryIntent struct {
	ID              string `json:"id"`
	SourcePath      string `json:"source_path"`
	GitCommonDir    string `json:"git_common_dir"`
	BaseRef         string `json:"base_ref"`
	BaseOID         string `json:"base_oid"`
	BranchRef       string `json:"branch_ref"`
	Destination     string `json:"destination"`
	IncludeInGoWork bool   `json:"include_in_go_work"`
}

type HarnessIntent struct {
	Kind            string   `json:"kind"`
	RootModulesOnly bool     `json:"root_modules_only"`
	RepositoryIDs   []string `json:"repository_ids,omitempty"`
	UsePaths        []string `json:"use_paths,omitempty"`
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("unsupported manifest schema %d", m.SchemaVersion)
	}
	if m.WorkID == "" {
		return fmt.Errorf("manifest Work ID is empty")
	}
	if len(m.NewWorkOperationID) != 32 {
		return fmt.Errorf("manifest New Work operation ID is invalid")
	}
	if _, err := hex.DecodeString(m.NewWorkOperationID); err != nil {
		return fmt.Errorf("manifest New Work operation ID is invalid")
	}
	if _, err := ParseName(m.Name.String()); err != nil {
		return fmt.Errorf("manifest Work name: %w", err)
	}
	if m.CreatedAt.IsZero() {
		return fmt.Errorf("manifest creation time is empty")
	}
	if len(m.Repositories) == 0 {
		return fmt.Errorf("manifest has no repositories")
	}
	seen := make(map[string]struct{}, len(m.Repositories))
	var expectedModuleIDs []string
	for i, repo := range m.Repositories {
		if repo.ID == "" {
			return fmt.Errorf("manifest repository %d has empty ID", i)
		}
		if _, exists := seen[repo.ID]; exists {
			return fmt.Errorf("manifest repository ID %q is duplicated", repo.ID)
		}
		seen[repo.ID] = struct{}{}
		if repo.SourcePath == "" || repo.GitCommonDir == "" || repo.BaseRef == "" ||
			repo.BaseOID == "" || repo.BranchRef == "" || repo.Destination == "" {
			return fmt.Errorf("manifest repository %q has incomplete intent", repo.ID)
		}
		if repo.IncludeInGoWork {
			expectedModuleIDs = append(expectedModuleIDs, repo.ID)
		}
	}
	if m.Harness.Kind != "go.work" {
		return fmt.Errorf("unsupported manifest harness intent")
	}
	if m.Harness.RootModulesOnly && len(m.Harness.UsePaths) > 0 {
		return fmt.Errorf("root-modules-only harness cannot define explicit use paths")
	}
	if !m.Harness.RootModulesOnly && len(m.Harness.UsePaths) == 0 {
		return fmt.Errorf("custom harness has no explicit use paths")
	}
	seenUsePaths := make(map[string]struct{}, len(m.Harness.UsePaths))
	for _, usePath := range m.Harness.UsePaths {
		if !strings.HasPrefix(usePath, "./") || path.IsAbs(usePath) {
			return fmt.Errorf("manifest harness use path %q is not Work-relative", usePath)
		}
		clean := path.Clean(strings.TrimPrefix(usePath, "./"))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || "./"+clean != usePath {
			return fmt.Errorf("manifest harness use path %q is unsafe or non-canonical", usePath)
		}
		if _, exists := seenUsePaths[usePath]; exists {
			return fmt.Errorf("manifest harness use path %q is duplicated", usePath)
		}
		seenUsePaths[usePath] = struct{}{}
	}
	if len(m.Harness.RepositoryIDs) != len(expectedModuleIDs) {
		return fmt.Errorf("manifest harness repositories do not match repository intent")
	}
	for i := range expectedModuleIDs {
		if m.Harness.RepositoryIDs[i] != expectedModuleIDs[i] {
			return fmt.Errorf("manifest harness repositories do not match repository intent")
		}
	}
	return nil
}
