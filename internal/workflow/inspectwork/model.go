package inspectwork

import (
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/work"
)

type Request struct {
	WorksRoot   string
	ControlRoot string
	Name        string
}

type MetadataState string

const (
	MetadataAbsent  MetadataState = "absent"
	MetadataValid   MetadataState = "valid"
	MetadataLegacy  MetadataState = "legacy"
	MetadataInvalid MetadataState = "invalid"
)

type PathKind string

const (
	PathUnknown   PathKind = "unknown"
	PathMissing   PathKind = "missing"
	PathDirectory PathKind = "directory"
	PathRegular   PathKind = "regular-file"
	PathSymlink   PathKind = "symlink"
	PathOther     PathKind = "other"
)

type IntentSource string

const (
	IntentNone            IntentSource = "none"
	IntentManifest        IntentSource = "manifest"
	IntentLegacyManifest  IntentSource = "legacy-manifest"
	IntentOperationRecord IntentSource = "operation-record"
)

type Action string

const (
	ActionNone                Action = "none"
	ActionResumeNewWork       Action = "resume-new-work"
	ActionResumeChangeWork    Action = "resume-change-work"
	ActionRepairWork          Action = "repair-work"
	ActionResolveGitOperation Action = "resolve-git-operation"
)

type ProblemCode string

const (
	ProblemWorkRootMissing          ProblemCode = "work-root-missing"
	ProblemWorkRootUnsafe           ProblemCode = "work-root-unsafe"
	ProblemManifestMissing          ProblemCode = "manifest-missing"
	ProblemManifestInvalid          ProblemCode = "manifest-invalid"
	ProblemWorkIdentityMismatch     ProblemCode = "work-identity-mismatch"
	ProblemOperationMissing         ProblemCode = "operation-record-missing"
	ProblemOperationInvalid         ProblemCode = "operation-record-invalid"
	ProblemOperationMismatch        ProblemCode = "operation-record-mismatch"
	ProblemNewWorkIncomplete        ProblemCode = "new-work-incomplete"
	ProblemChangeOperationMissing   ProblemCode = "change-operation-record-missing"
	ProblemChangeOperationInvalid   ProblemCode = "change-operation-record-invalid"
	ProblemChangeOperationMismatch  ProblemCode = "change-operation-record-mismatch"
	ProblemChangeWorkIncomplete     ProblemCode = "change-work-incomplete"
	ProblemSourceUnreadable         ProblemCode = "source-unreadable"
	ProblemSourceIdentityMismatch   ProblemCode = "source-identity-mismatch"
	ProblemBranchInspectionFailed   ProblemCode = "branch-inspection-failed"
	ProblemBranchMissing            ProblemCode = "branch-missing"
	ProblemDestinationMissing       ProblemCode = "worktree-directory-missing"
	ProblemDestinationUnsafe        ProblemCode = "worktree-path-unsafe"
	ProblemDestinationNotWorktree   ProblemCode = "destination-not-worktree"
	ProblemCheckoutIdentityMismatch ProblemCode = "checkout-identity-mismatch"
	ProblemBranchRefMismatch        ProblemCode = "branch-ref-mismatch"
	ProblemRegistrationInspection   ProblemCode = "registration-inspection-failed"
	ProblemRegistrationMissing      ProblemCode = "worktree-registration-missing"
	ProblemRegistrationStale        ProblemCode = "worktree-registration-stale"
	ProblemRegistrationConflict     ProblemCode = "worktree-registration-conflict"
	ProblemHeadMismatch             ProblemCode = "head-mismatch"
	ProblemWorkingTreeUnknown       ProblemCode = "working-tree-state-unknown"
	ProblemActiveGitOperation       ProblemCode = "active-git-operation"
	ProblemGitOperationUnknown      ProblemCode = "git-operation-state-unknown"
	ProblemHarnessMissing           ProblemCode = "harness-missing"
	ProblemHarnessUnexpected        ProblemCode = "harness-unexpected"
	ProblemHarnessMismatch          ProblemCode = "harness-mismatch"
)

type Problem struct {
	Code         ProblemCode
	RepositoryID string
	Path         string
	Message      string
	Next         Action
}

type Snapshot struct {
	InspectedAt     time.Time
	WorkName        work.Name
	WorkID          work.Identity
	WorksRoot       string
	WorkRoot        string
	WorkRootKind    PathKind
	Manifest        ManifestSnapshot
	Operation       OperationSnapshot
	ChangeOperation ChangeOperationSnapshot
	IntentSource    IntentSource
	Repositories    []RepositorySnapshot
	Harness         HarnessSnapshot
	Problems        []Problem
}

type ChangeOperationSnapshot struct {
	Path            string
	State           MetadataState
	OperationID     string
	Kind            string
	Phase           string
	LastProblem     string
	ResumeSuggested bool
}

type ManifestSnapshot struct {
	Path  string
	State MetadataState
	Value *work.Manifest
}

type OperationSnapshot struct {
	Path            string
	State           MetadataState
	OperationID     string
	Phase           string
	UpdatedAt       *time.Time
	LastProblem     string
	ResumeSuggested bool
}

type RepositorySnapshot struct {
	ID                      string
	Intent                  work.RepositoryIntent
	IntentSource            IntentSource
	SourceKnown             bool
	Source                  RepositoryIdentity
	BranchKnown             bool
	BranchExists            bool
	BranchOID               string
	RegistrationsKnown      bool
	DestinationRegistration *RegistrationSnapshot
	BranchRegistrations     []RegistrationSnapshot
	DestinationKind         PathKind
	CheckoutKnown           bool
	Checkout                CheckoutSnapshot
	WorkingTreeKnown        bool
	WorkingTree             gitops.WorkingTreeStatus
	GitOperationsKnown      bool
	GitOperations           []gitops.ActiveOperation
	Problems                []Problem
}

type RepositoryIdentity struct {
	SourcePath string
	CommonDir  string
}

type RegistrationSnapshot struct {
	Path     string
	HeadOID  string
	Branch   string
	Detached bool
	Locked   bool
	Prunable bool
}

type CheckoutSnapshot struct {
	SourcePath string
	CommonDir  string
	FullRef    string
	HeadOID    string
	Detached   bool
}

type HarnessSnapshot struct {
	Path                     string
	Kind                     PathKind
	VerifiedAgainstOperation bool
}
