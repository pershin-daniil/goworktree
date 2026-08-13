package newwork

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pershin-daniil/goworktree/internal/work"
)

const OperationSchemaVersion = 1

type Phase string

const (
	PhaseRecordingIntent             Phase = "recording-intent"
	PhaseCreatingWorkRoot            Phase = "creating-work-root"
	PhaseCreatingRepositoryWorktrees Phase = "creating-repository-worktrees"
	PhaseGeneratingHarness           Phase = "generating-harness"
	PhaseVerifying                   Phase = "verifying"
	PhaseCreated                     Phase = "created"
	PhasePartial                     Phase = "partial"
	PhaseInterrupted                 Phase = "interrupted"
	PhaseFailedKnownState            Phase = "failed-known-state"
)

type StepState string

const (
	StepPending  StepState = "pending"
	StepIntent   StepState = "intent-recorded"
	StepVerified StepState = "verified"
)

type OperationRecord struct {
	SchemaVersion int                    `json:"schema_version"`
	OperationID   string                 `json:"operation_id"`
	Kind          string                 `json:"kind"`
	WorkID        work.Identity          `json:"work_id"`
	Phase         Phase                  `json:"phase"`
	Plan          Plan                   `json:"plan"`
	WorkRootReady bool                   `json:"work_root_ready"`
	Repositories  []RepositoryCheckpoint `json:"repositories"`
	Harness       HarnessCheckpoint      `json:"harness"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
	LastProblem   *PersistedProblem      `json:"last_problem,omitempty"`
}

type RepositoryCheckpoint struct {
	ID         string     `json:"id"`
	State      StepState  `json:"state"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	HeadOID    string     `json:"head_oid,omitempty"`
}

type HarnessCheckpoint struct {
	State      StepState  `json:"state"`
	Generated  bool       `json:"generated"`
	GoVersion  string     `json:"go_version,omitempty"`
	UsePaths   []string   `json:"use_paths,omitempty"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
}

type PersistedProblem struct {
	Code         ErrorCode `json:"code"`
	Operation    string    `json:"operation"`
	RepositoryID string    `json:"repository_id,omitempty"`
	Path         string    `json:"path,omitempty"`
	Message      string    `json:"message"`
}

func newOperationRecord(plan Plan, operationID string, now time.Time) OperationRecord {
	repositories := make([]RepositoryCheckpoint, 0, len(plan.Repositories))
	for _, repo := range plan.Repositories {
		repositories = append(repositories, RepositoryCheckpoint{ID: repo.ID, State: StepPending})
	}
	return OperationRecord{
		SchemaVersion: OperationSchemaVersion,
		OperationID:   operationID,
		Kind:          "new-work",
		WorkID:        plan.WorkID,
		Phase:         PhaseRecordingIntent,
		Plan:          plan,
		Repositories:  repositories,
		Harness:       HarnessCheckpoint{State: StepPending},
		CreatedAt:     now.UTC(),
		UpdatedAt:     now.UTC(),
	}
}

func (r OperationRecord) Validate() error {
	if r.SchemaVersion != OperationSchemaVersion {
		return fmt.Errorf("unsupported operation schema %d", r.SchemaVersion)
	}
	if len(r.OperationID) != 32 {
		return fmt.Errorf("invalid operation ID %q", r.OperationID)
	}
	if _, err := hex.DecodeString(r.OperationID); err != nil {
		return fmt.Errorf("invalid operation ID %q", r.OperationID)
	}
	if r.Kind != "new-work" {
		return fmt.Errorf("unsupported operation kind %q", r.Kind)
	}
	if r.WorkID == "" || r.WorkID != r.Plan.WorkID {
		return fmt.Errorf("operation Work identity does not match its plan")
	}
	if r.Plan.WorkName == "" || r.Plan.WorkRoot == "" || r.Plan.ManifestPath == "" || r.Plan.OperationRecordPath == "" {
		return fmt.Errorf("operation plan is incomplete")
	}
	if len(r.Plan.Repositories) == 0 || len(r.Repositories) != len(r.Plan.Repositories) {
		return fmt.Errorf("operation repository checkpoints do not match plan")
	}
	for i, checkpoint := range r.Repositories {
		if checkpoint.ID != r.Plan.Repositories[i].ID {
			return fmt.Errorf("operation repository checkpoint %d does not match plan", i)
		}
		switch checkpoint.State {
		case StepPending, StepIntent, StepVerified:
		default:
			return fmt.Errorf("repository %q has invalid checkpoint %q", checkpoint.ID, checkpoint.State)
		}
		if checkpoint.State == StepVerified && (checkpoint.VerifiedAt == nil || checkpoint.HeadOID == "") {
			return fmt.Errorf("repository %q has incomplete verified checkpoint", checkpoint.ID)
		}
		if checkpoint.State == StepVerified && checkpoint.HeadOID != r.Plan.Repositories[i].BaseOID {
			return fmt.Errorf("repository %q verified checkpoint does not match planned base", checkpoint.ID)
		}
	}
	switch r.Harness.State {
	case StepPending, StepIntent, StepVerified:
	default:
		return fmt.Errorf("harness has invalid checkpoint %q", r.Harness.State)
	}
	if r.Harness.State == StepVerified && r.Harness.VerifiedAt == nil {
		return fmt.Errorf("harness has incomplete verified checkpoint")
	}
	switch r.Phase {
	case PhaseRecordingIntent, PhaseCreatingWorkRoot, PhaseCreatingRepositoryWorktrees,
		PhaseGeneratingHarness, PhaseVerifying, PhaseCreated, PhasePartial,
		PhaseInterrupted, PhaseFailedKnownState:
	default:
		return fmt.Errorf("operation has invalid phase %q", r.Phase)
	}
	if r.Phase == PhaseCreated {
		if !r.WorkRootReady || r.Harness.State != StepVerified {
			return fmt.Errorf("created operation has incomplete Work checkpoints")
		}
		for _, checkpoint := range r.Repositories {
			if checkpoint.State != StepVerified {
				return fmt.Errorf("created operation has incomplete repository checkpoints")
			}
		}
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return fmt.Errorf("operation timestamps are incomplete")
	}
	return nil
}

type OperationStore struct{}

func (OperationStore) Create(record OperationRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	path := record.Plan.OperationRecordPath
	if err := ensureOperationDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("create operation directory: %w", err)
	}
	if err := work.CleanupAtomicTemps(path); err != nil {
		return fmt.Errorf("clean operation temporary files: %w", err)
	}
	return work.CreateJSON(path, record, 0o600)
}

func ensureOperationDirectory(path string) error {
	newWorkDir := filepath.Clean(path)
	operationsDir := filepath.Dir(newWorkDir)
	controlRoot := filepath.Dir(operationsDir)
	if filepath.Base(newWorkDir) != "new-work" || filepath.Base(operationsDir) != "operations" {
		return fmt.Errorf("operation path has invalid layout")
	}
	for _, dir := range []string{controlRoot, operationsDir, newWorkDir} {
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			if dir == controlRoot {
				return fmt.Errorf("control root does not exist: %s", controlRoot)
			}
			if err := os.Mkdir(dir, 0o700); err != nil {
				if !errors.Is(err, os.ErrExist) {
					return err
				}
				info, err = os.Lstat(dir)
				if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("concurrently created operation path is unsafe: %s", dir)
				}
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("operation path component is not an owned directory: %s", dir)
		}
	}
	return nil
}

func (OperationStore) Load(path string) (OperationRecord, error) {
	var record OperationRecord
	if err := work.LoadJSON(path, &record); err != nil {
		return OperationRecord{}, err
	}
	if err := record.Validate(); err != nil {
		return OperationRecord{}, fmt.Errorf("validate operation record %s: %w", path, err)
	}
	if filepath.Clean(record.Plan.OperationRecordPath) != filepath.Clean(path) {
		return OperationRecord{}, fmt.Errorf("operation record path does not match embedded plan")
	}
	return record, nil
}

func (OperationStore) Save(record OperationRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	return work.ReplaceJSON(record.Plan.OperationRecordPath, record, 0o600, func(current []byte) error {
		var stored OperationRecord
		if err := json.Unmarshal(current, &stored); err != nil {
			return err
		}
		if stored.OperationID != record.OperationID || stored.WorkID != record.WorkID {
			return fmt.Errorf("operation ownership changed")
		}
		storedPlan, storedErr := json.Marshal(stored.Plan)
		recordPlan, recordErr := json.Marshal(record.Plan)
		if storedErr != nil || recordErr != nil {
			return fmt.Errorf("encode operation plan: %w", errors.Join(storedErr, recordErr))
		}
		if !bytes.Equal(storedPlan, recordPlan) {
			return fmt.Errorf("immutable operation plan changed")
		}
		return nil
	})
}

func randomOperationID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func manifestFromOperation(record OperationRecord) work.Manifest {
	repositories := make([]work.RepositoryIntent, 0, len(record.Plan.Repositories))
	moduleIDs := make([]string, 0, len(record.Plan.Repositories))
	for _, repo := range record.Plan.Repositories {
		repositories = append(repositories, work.RepositoryIntent{
			ID:              repo.ID,
			SourcePath:      repo.SourcePath,
			GitCommonDir:    repo.GitCommonDir,
			BaseRef:         repo.BaseRef,
			BaseOID:         repo.BaseOID,
			BranchRef:       repo.TargetBranchRef,
			Destination:     repo.Destination,
			IncludeInGoWork: repo.IncludeInGoWork,
		})
		if repo.IncludeInGoWork {
			moduleIDs = append(moduleIDs, repo.ID)
		}
	}
	return work.Manifest{
		SchemaVersion:      work.ManifestSchemaVersion,
		WorkID:             record.WorkID,
		NewWorkOperationID: record.OperationID,
		Name:               record.Plan.WorkName,
		CreatedAt:          record.CreatedAt,
		Repositories:       repositories,
		Harness: work.HarnessIntent{
			Kind:            "go.work",
			RootModulesOnly: true,
			RepositoryIDs:   moduleIDs,
		},
	}
}

// RecoverCompletedOperation reconstructs the immutable completed New Work
// record from a valid manifest after that external record was lost. It does
// not infer repository intent beyond fields already stored in the manifest.
func RecoverCompletedOperation(manifest work.Manifest, workRoot, operationPath string, now time.Time) (OperationRecord, error) {
	plan, err := PlanFromManifest(manifest, workRoot, operationPath)
	if err != nil {
		return OperationRecord{}, err
	}
	_, harness, err := expectedGoWork(plan)
	if err != nil {
		return OperationRecord{}, err
	}
	verifiedAt := now.UTC()
	harness.State, harness.VerifiedAt = StepVerified, &verifiedAt
	if err := (GoWorkHarness{}).Verify(plan, harness); err != nil {
		return OperationRecord{}, fmt.Errorf("verify current harness: %w", err)
	}
	record := OperationRecord{
		SchemaVersion: OperationSchemaVersion, OperationID: manifest.NewWorkOperationID, Kind: "new-work",
		WorkID: manifest.WorkID, Phase: PhaseCreated, Plan: plan, WorkRootReady: true,
		Harness: harness, CreatedAt: manifest.CreatedAt.UTC(), UpdatedAt: verifiedAt,
	}
	for _, repository := range plan.Repositories {
		record.Repositories = append(record.Repositories, RepositoryCheckpoint{
			ID: repository.ID, State: StepVerified, VerifiedAt: &verifiedAt, HeadOID: repository.BaseOID,
		})
	}
	if err := record.Validate(); err != nil {
		return OperationRecord{}, err
	}
	return record, nil
}

// PlanFromManifest reconstructs only intent already present in a valid Work
// manifest. It performs no Git or filesystem mutation.
func PlanFromManifest(manifest work.Manifest, workRoot, operationPath string) (Plan, error) {
	if err := manifest.Validate(); err != nil {
		return Plan{}, fmt.Errorf("validate manifest: %w", err)
	}
	plan := Plan{
		WorkName: manifest.Name, WorkID: manifest.WorkID, WorkRoot: workRoot,
		ManifestPath: filepath.Join(workRoot, ".goworktree.json"), OperationRecordPath: operationPath,
		Mode: ModeOffline, NoRemoteMutation: true,
	}
	for _, repository := range manifest.Repositories {
		plan.Repositories = append(plan.Repositories, RepositoryPlan{
			ID: repository.ID, SourcePath: repository.SourcePath, GitCommonDir: repository.GitCommonDir,
			BaseRef: repository.BaseRef, BaseOID: repository.BaseOID, TargetBranchRef: repository.BranchRef,
			Destination: repository.Destination, IncludeInGoWork: repository.IncludeInGoWork,
		})
	}
	return plan, nil
}
