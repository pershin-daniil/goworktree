package changework

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
)

type Kind string

const (
	KindAdd    Kind = "add-repositories"
	KindRemove Kind = "remove-repositories"
	KindAdopt  Kind = "adopt-branch"
)

type AddRequest struct {
	WorkName      string
	RepositoryIDs []string
	Mode          newwork.Mode
}

type RemoveRequest struct {
	WorkName       string
	RepositoryIDs  []string
	DeleteBranches bool
}

type AdoptRequest struct {
	WorkName     string
	RepositoryID string
}

// ResumeRequest binds an explicit CLI retry to the entire recorded request.
// Dashboard Resume intentionally uses the previously displayed operation.
type ResumeRequest struct {
	WorkName       string
	Kind           Kind
	RepositoryIDs  []string
	Mode           newwork.Mode
	DeleteBranches bool
}

func (p Plan) MatchRequest(request ResumeRequest) error {
	ids := make([]string, 0, len(p.Repositories))
	for _, repository := range p.Repositories {
		ids = append(ids, repository.ID)
		if request.Kind == KindRemove && repository.DeleteBranch != request.DeleteBranches {
			return fmt.Errorf("pending repository change has a different branch-deletion policy; repeat the original command or use Resume in the dashboard")
		}
	}
	slices.Sort(ids)
	wanted := append([]string(nil), request.RepositoryIDs...)
	slices.Sort(wanted)
	if request.WorkName != p.WorkName.String() || request.Kind != p.Kind || !slices.Equal(ids, wanted) ||
		(request.Kind == KindAdd && request.Mode != p.Mode) {
		return fmt.Errorf("pending %s selects [%s] in %s mode; request differs, repeat the original command or use Resume in the dashboard", p.Kind, strings.Join(ids, ","), p.Mode)
	}
	return nil
}

type Catalog struct {
	WorkRoot     string
	ControlRoot  string
	Manifest     work.Manifest
	Repositories map[string]Repository
}

type Repository struct {
	ID             string
	SourcePath     string
	Folder         string
	Remote         string
	BasePreference string
}

type Plan struct {
	OperationID         string           `json:"operation_id"`
	OperationPath       string           `json:"operation_path"`
	Kind                Kind             `json:"kind"`
	Mode                newwork.Mode     `json:"mode,omitempty"`
	WorkName            work.Name        `json:"work_name"`
	WorkID              work.Identity    `json:"work_id"`
	WorkRoot            string           `json:"work_root"`
	Before              work.Manifest    `json:"before"`
	After               work.Manifest    `json:"after"`
	Repositories        []RepositoryPlan `json:"repositories"`
	HarnessBefore       []byte           `json:"harness_before,omitempty"`
	HarnessBeforeExists bool             `json:"harness_before_exists"`
	NoRemoteMutation    bool             `json:"no_remote_mutation"`
}

type RepositoryPlan struct {
	ID              string                   `json:"id"`
	SourcePath      string                   `json:"source_path"`
	GitCommonDir    string                   `json:"git_common_dir"`
	BaseRef         string                   `json:"base_ref"`
	BaseOID         string                   `json:"base_oid"`
	BranchRef       string                   `json:"branch_ref"`
	BranchOID       string                   `json:"branch_oid"`
	Destination     string                   `json:"destination"`
	IncludeInGoWork bool                     `json:"include_in_go_work"`
	Reattach        bool                     `json:"reattach,omitempty"`
	DeleteBranch    bool                     `json:"delete_branch,omitempty"`
	WorkingTree     gitops.WorkingTreeStatus `json:"working_tree,omitempty"`
	Fingerprint     string                   `json:"fingerprint,omitempty"`
	FetchedAt       *time.Time               `json:"fetched_at,omitempty"`
}

type Result struct {
	WorkName      string
	Kind          Kind
	Revision      uint64
	OperationPath string
	Repositories  []RepositoryResult
}

type RepositoryResult struct {
	ID     string
	Status string
	Err    error
}

func (p Plan) Validate() error {
	if p.OperationID == "" || p.OperationPath == "" || p.WorkName == "" || p.WorkID == "" || p.WorkRoot == "" {
		return fmt.Errorf("change Work plan is incomplete")
	}
	if !filepath.IsAbs(p.WorkRoot) || work.NewIdentity(filepath.Dir(p.WorkRoot), p.WorkName) != p.WorkID {
		return fmt.Errorf("change Work root does not match Work identity")
	}
	if len(p.OperationID) != 32 {
		return fmt.Errorf("change Work operation ID is invalid")
	}
	if _, err := hex.DecodeString(p.OperationID); err != nil {
		return fmt.Errorf("change Work operation ID is invalid")
	}
	if p.Kind != KindAdd && p.Kind != KindRemove && p.Kind != KindAdopt {
		return fmt.Errorf("unsupported change Work kind %q", p.Kind)
	}
	if !p.NoRemoteMutation {
		return fmt.Errorf("change Work plan does not prohibit remote mutation")
	}
	if p.Kind == KindAdd && p.Mode != newwork.ModeOnline && p.Mode != newwork.ModeOffline {
		return fmt.Errorf("invalid add mode %q", p.Mode)
	}
	if len(p.Repositories) == 0 {
		return fmt.Errorf("change Work plan has no repositories")
	}
	if !filepath.IsAbs(p.OperationPath) || filepath.Base(p.OperationPath) != p.WorkID.String()+".json" ||
		filepath.Base(filepath.Dir(p.OperationPath)) != "change-work" ||
		filepath.Base(filepath.Dir(filepath.Dir(p.OperationPath))) != "operations" {
		return fmt.Errorf("change Work operation path does not match Work identity")
	}
	if err := p.Before.Validate(); err != nil {
		return fmt.Errorf("validate before manifest: %w", err)
	}
	if err := p.After.Validate(); err != nil {
		return fmt.Errorf("validate after manifest: %w", err)
	}
	if p.Before.WorkID != p.WorkID || p.After.WorkID != p.WorkID || p.Before.Name != p.WorkName || p.After.Name != p.WorkName {
		return fmt.Errorf("change Work manifest identity mismatch")
	}
	if p.Before.NewWorkOperationID != p.After.NewWorkOperationID || !p.Before.CreatedAt.Equal(p.After.CreatedAt) {
		return fmt.Errorf("change Work plan modifies immutable New Work provenance")
	}
	if p.After.SchemaVersion != work.ManifestSchemaVersion || p.After.Revision != p.Before.Revision+1 || p.After.LastChangeID != p.OperationID {
		return fmt.Errorf("change Work manifest revision mismatch")
	}
	seenIDs := make(map[string]struct{}, len(p.Repositories))
	seenIdentities := make(map[string]struct{}, len(p.Repositories))
	seenDestinations := make(map[string]struct{}, len(p.Repositories))
	for _, repository := range p.Repositories {
		if repository.ID == "" || repository.SourcePath == "" || repository.GitCommonDir == "" ||
			repository.BaseRef == "" || !validGitOID(repository.BaseOID) || !validGitOID(repository.BranchOID) || repository.Destination == "" ||
			!strings.HasPrefix(repository.BranchRef, "refs/heads/") {
			return fmt.Errorf("repository %q plan is incomplete", repository.ID)
		}
		if !filepath.IsAbs(repository.SourcePath) || !filepath.IsAbs(repository.GitCommonDir) || !filepath.IsAbs(repository.Destination) {
			return fmt.Errorf("repository %q plan paths are not absolute", repository.ID)
		}
		if filepath.Dir(filepath.Clean(repository.Destination)) != filepath.Clean(p.WorkRoot) {
			return fmt.Errorf("repository %q destination is outside Work root", repository.ID)
		}
		if _, exists := seenIDs[repository.ID]; exists {
			return fmt.Errorf("repository ID %q is duplicated", repository.ID)
		}
		if _, exists := seenIdentities[repository.GitCommonDir]; exists {
			return fmt.Errorf("repository Git identity %q is duplicated", repository.GitCommonDir)
		}
		if _, exists := seenDestinations[filepath.Clean(repository.Destination)]; exists {
			return fmt.Errorf("repository destination %q is duplicated", repository.Destination)
		}
		seenIDs[repository.ID] = struct{}{}
		seenIdentities[repository.GitCommonDir] = struct{}{}
		seenDestinations[filepath.Clean(repository.Destination)] = struct{}{}
		if p.Kind == KindRemove && repository.Fingerprint == "" {
			return fmt.Errorf("repository %q removal fingerprint is empty", repository.ID)
		}
		if p.Kind != KindRemove && repository.DeleteBranch {
			return fmt.Errorf("repository %q non-removal plan deletes a branch", repository.ID)
		}
		if p.Kind != KindAdd && repository.Reattach {
			return fmt.Errorf("repository %q non-add plan reattaches a branch", repository.ID)
		}
	}
	if err := validateManifestTransition(p); err != nil {
		return err
	}
	return nil
}

func validateManifestTransition(plan Plan) error {
	expected := cloneManifest(plan.Before)
	expected.SchemaVersion = work.ManifestSchemaVersion
	expected.Revision++
	expected.LastChangeID = plan.OperationID
	active := activeByID(plan.Before)
	inactive := inactiveByID(plan.Before)
	for _, repository := range plan.Repositories {
		intent := work.RepositoryIntent{
			ID: repository.ID, SourcePath: repository.SourcePath, GitCommonDir: repository.GitCommonDir,
			BaseRef: repository.BaseRef, BaseOID: repository.BaseOID, BranchRef: repository.BranchRef,
			Destination: repository.Destination, IncludeInGoWork: repository.IncludeInGoWork,
		}
		switch plan.Kind {
		case KindAdopt:
			before, exists := active[repository.ID]
			before.BranchRef = intent.BranchRef
			if !exists || before != intent {
				return fmt.Errorf("repository %q branch adoption changes other Work intent", repository.ID)
			}
			for index := range expected.Repositories {
				if expected.Repositories[index].ID == repository.ID {
					expected.Repositories[index].BranchRef = intent.BranchRef
				}
			}
		case KindAdd:
			if _, exists := active[repository.ID]; exists {
				return fmt.Errorf("repository %q add transition is already active", repository.ID)
			}
			if tombstone, exists := inactive[repository.ID]; exists {
				if tombstone.BranchRetained != repository.Reattach {
					return fmt.Errorf("repository %q reattach intent does not match its tombstone", repository.ID)
				}
				if repository.Reattach && (tombstone.Repository != intent || tombstone.BranchOID != repository.BranchOID) {
					return fmt.Errorf("repository %q reattach intent changed retained ownership", repository.ID)
				}
			} else if repository.Reattach {
				return fmt.Errorf("repository %q reattach plan has no tombstone", repository.ID)
			}
			expected.Repositories = append(expected.Repositories, intent)
			removeInactive(&expected, repository.ID)
			addHarnessRepository(&expected, intent)
		case KindRemove:
			before, exists := active[repository.ID]
			if !exists || before != intent {
				return fmt.Errorf("repository %q remove intent does not match active manifest", repository.ID)
			}
			removeActive(&expected, repository.ID)
			removeInactive(&expected, repository.ID)
			expected.InactiveRepositories = append(expected.InactiveRepositories, work.InactiveRepositoryIntent{
				Repository: before, BranchRetained: !repository.DeleteBranch,
				BranchOID: retainedBranchOID(repository.DeleteBranch, repository.BranchOID),
			})
			removeHarnessRepository(&expected, before)
		}
	}
	stableManifest(&expected)
	if !sameJSON(expected, plan.After) {
		return fmt.Errorf("change Work after-manifest does not match repository plan")
	}
	return nil
}

func validGitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
