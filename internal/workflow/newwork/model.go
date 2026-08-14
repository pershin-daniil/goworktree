package newwork

import (
	"fmt"
	"strings"
	"time"

	"github.com/pershin-daniil/goworktree/internal/work"
)

type Mode string

const (
	ModeOnline  Mode = "online"
	ModeOffline Mode = "offline"
)

type Request struct {
	Name          string            `json:"name"`
	RepositoryIDs []string          `json:"repository_ids"`
	BaseOverrides map[string]string `json:"base_overrides,omitempty"`
	Mode          Mode              `json:"mode"`
	OpenProgram   string            `json:"open_program,omitempty"`
}

type Catalog struct {
	WorksRoot    string
	ControlRoot  string
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
	WorkName            work.Name        `json:"work_name"`
	WorkID              work.Identity    `json:"work_id"`
	WorkRoot            string           `json:"work_root"`
	ManifestPath        string           `json:"manifest_path"`
	OperationRecordPath string           `json:"operation_record_path"`
	Mode                Mode             `json:"mode"`
	OpenProgram         string           `json:"open_program,omitempty"`
	Repositories        []RepositoryPlan `json:"repositories"`
	HarnessUsePaths     []string         `json:"harness_use_paths,omitempty"`
	NoRemoteMutation    bool             `json:"no_remote_mutation"`
}

type RepositoryPlan struct {
	ID              string     `json:"id"`
	SourcePath      string     `json:"source_path"`
	GitCommonDir    string     `json:"git_common_dir"`
	Remote          string     `json:"remote,omitempty"`
	FetchedAt       *time.Time `json:"fetched_at,omitempty"`
	BaseRef         string     `json:"base_ref"`
	BaseOID         string     `json:"base_oid"`
	TargetBranchRef string     `json:"target_branch_ref"`
	Destination     string     `json:"destination"`
	IncludeInGoWork bool       `json:"include_in_go_work"`
}

type ErrorCode string

const (
	CodeInvalidInput    ErrorCode = "invalid-input"
	CodeAlreadyExists   ErrorCode = "already-exists"
	CodeNotFound        ErrorCode = "not-found"
	CodeUnsafePath      ErrorCode = "unsafe-path"
	CodeLocked          ErrorCode = "locked"
	CodeExternalFailure ErrorCode = "external-failure"
	CodeStateConflict   ErrorCode = "state-conflict"
	CodeInvalidState    ErrorCode = "invalid-state"
	CodeTimeout         ErrorCode = "timeout"
	CodeInterrupted     ErrorCode = "interrupted"
	CodeInternal        ErrorCode = "internal"
)

type Problem struct {
	Code         ErrorCode
	Operation    string
	RepositoryID string
	Path         string
	Cause        error
}

func (p Problem) Error() string {
	var context []string
	if p.RepositoryID != "" {
		context = append(context, "repository="+p.RepositoryID)
	}
	if p.Path != "" {
		context = append(context, "path="+p.Path)
	}
	prefix := string(p.Code)
	if p.Operation != "" {
		prefix += " " + p.Operation
	}
	if len(context) > 0 {
		prefix += " [" + strings.Join(context, " ") + "]"
	}
	if p.Cause != nil {
		return prefix + ": " + p.Cause.Error()
	}
	return prefix
}

func (p Problem) Unwrap() error { return p.Cause }

type ProblemsError struct {
	Problems []Problem
}

func (e *ProblemsError) Error() string {
	if e == nil || len(e.Problems) == 0 {
		return "new Work planning failed"
	}
	if len(e.Problems) == 1 {
		return e.Problems[0].Error()
	}
	return fmt.Sprintf("new Work planning failed with %d problems; first: %s", len(e.Problems), e.Problems[0].Error())
}

func (e *ProblemsError) Unwrap() []error {
	if e == nil {
		return nil
	}
	result := make([]error, 0, len(e.Problems))
	for i := range e.Problems {
		result = append(result, e.Problems[i])
	}
	return result
}
