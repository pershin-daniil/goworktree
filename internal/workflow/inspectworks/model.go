package inspectworks

import (
	"time"

	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
)

type Request struct {
	WorksRoot   string
	ControlRoot string
}

type ProblemCode string

const (
	ProblemWorksRootUnreadable       ProblemCode = "works-root-unreadable"
	ProblemEntryNameInvalid          ProblemCode = "work-entry-name-invalid"
	ProblemOperationDirectoryUnread  ProblemCode = "operation-directory-unreadable"
	ProblemOperationEntryUnsafe      ProblemCode = "operation-entry-unsafe"
	ProblemOperationRecordInvalid    ProblemCode = "operation-record-invalid"
	ProblemOperationOutsideWorksRoot ProblemCode = "operation-outside-works-root"
	ProblemWorkInspectionFailed      ProblemCode = "work-inspection-failed"
)

type Problem struct {
	Code    ProblemCode
	Name    string
	Path    string
	Message string
}

type Snapshot struct {
	InspectedAt time.Time
	WorksRoot   string
	ControlRoot string
	Works       []Work
	Problems    []Problem
}

type Work struct {
	Name          string
	RootPath      string
	FromDirectory bool
	FromOperation bool
	Snapshot      *inspectwork.Snapshot
	Problems      []Problem
}

func (w Work) ProblemCount() int {
	count := len(w.Problems)
	if w.Snapshot != nil {
		count += len(w.Snapshot.Problems)
	}
	return count
}
