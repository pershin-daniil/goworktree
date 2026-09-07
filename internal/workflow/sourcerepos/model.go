package sourcerepos

import (
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
)

type Relation string

const (
	RelationUnknown  Relation = "unknown"
	RelationMissing  Relation = "missing"
	RelationEqual    Relation = "equal"
	RelationBehind   Relation = "behind"
	RelationAhead    Relation = "ahead"
	RelationDiverged Relation = "diverged"
)

type Action string

const (
	ActionUpdateCheckout Action = "update-checkout"
	ActionUpdateRef      Action = "update-ref"
	ActionUnchanged      Action = "unchanged"
	ActionSkip           Action = "skip"
	ActionFailed         Action = "failed"
)

type Status string

const (
	StatusUpdated   Status = "updated"
	StatusUnchanged Status = "unchanged"
	StatusSkipped   Status = "skipped"
	StatusFailed    Status = "failed"
	StatusFetched   Status = "fetched"
)

type RepositoryConfig struct {
	ID            string
	Name          string
	Path          string
	Remote        string
	DefaultBranch string
	Groups        []string
}

type RepositorySnapshot struct {
	RepositoryConfig
	InspectedAt time.Time
	FetchedAt   time.Time

	Identity        gitops.RepositoryIdentity
	IdentityKnown   bool
	Checkout        gitops.Checkout
	CheckoutKnown   bool
	WorkingTree     gitops.WorkingTreeStatus
	WorkingKnown    bool
	Operations      []gitops.ActiveOperation
	OperationsKnown bool
	Worktrees       []gitops.WorktreeRegistration
	WorktreesKnown  bool

	LocalRef    string
	LocalOID    string
	LocalKnown  bool
	RemoteRef   string
	RemoteOID   string
	RemoteKnown bool
	Relation    Relation
	Problem     string
}

type Snapshot struct {
	InspectedAt  time.Time
	Repositories []RepositorySnapshot
}

type RepositoryPlan struct {
	RepositoryConfig
	GitCommonDir string
	FetchedAt    time.Time
	LocalRef     string
	LocalOID     string
	RemoteRef    string
	TargetOID    string
	Relation     Relation
	Action       Action
	CheckoutPath string
	Reason       string
}

type Plan struct {
	BuiltAt      time.Time
	Repositories []RepositoryPlan
}

type RepositoryResult struct {
	ID       string
	Path     string
	Status   Status
	From     string
	To       string
	Relation Relation
	Err      error
	Snapshot RepositorySnapshot
}

type Result struct {
	Repositories []RepositoryResult
}

func (r Result) NeedsAttention() bool {
	for _, repository := range r.Repositories {
		if repository.Status == StatusSkipped || repository.Status == StatusFailed {
			return true
		}
	}
	return false
}
