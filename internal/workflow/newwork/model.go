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
	Name          string
	RepositoryIDs []string
	BaseOverrides map[string]string
	Mode          Mode
	OpenProgram   string
}

type Catalog struct {
	WorksRoot    string
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
	WorkName         work.Name
	WorkRoot         string
	ManifestPath     string
	Mode             Mode
	OpenProgram      string
	Repositories     []RepositoryPlan
	NoRemoteMutation bool
}

type RepositoryPlan struct {
	ID              string
	SourcePath      string
	GitCommonDir    string
	Remote          string
	FetchedAt       *time.Time
	BaseRef         string
	BaseOID         string
	TargetBranchRef string
	Destination     string
	IncludeInGoWork bool
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
