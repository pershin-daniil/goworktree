package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type WorkingTreeStatus struct {
	Staged          int
	Unstaged        int
	Untracked       int
	Ignored         int
	Conflicted      int
	DirtySubmodules int
}

func (s WorkingTreeStatus) Dirty() bool {
	return s.Staged > 0 || s.Unstaged > 0 || s.Untracked > 0 || s.Conflicted > 0 || s.DirtySubmodules > 0
}

type ActiveOperation string

const (
	OperationRebase     ActiveOperation = "rebase"
	OperationMerge      ActiveOperation = "merge"
	OperationCherryPick ActiveOperation = "cherry-pick"
	OperationRevert     ActiveOperation = "revert"
	OperationBisect     ActiveOperation = "bisect"
	OperationSequencer  ActiveOperation = "sequencer"
)

// WorkingTreeStatusContext reads porcelain v2 state without refreshing the
// index or contacting a remote.
func WorkingTreeStatusContext(ctx context.Context, repo string) (WorkingTreeStatus, error) {
	out, err := runContext(ctx, repo,
		"--no-optional-locks", "status", "--porcelain=v2", "-z",
		"--untracked-files=all", "--ignored=matching")
	if err != nil {
		return WorkingTreeStatus{}, err
	}
	return parsePorcelainV2Z(out)
}

func parsePorcelainV2Z(output string) (WorkingTreeStatus, error) {
	var status WorkingTreeStatus
	records := strings.Split(output, "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if record == "" || strings.HasPrefix(record, "# ") {
			continue
		}
		switch record[0] {
		case '1', '2':
			parts := strings.SplitN(record, " ", 4)
			if len(parts) < 3 || len(parts[1]) != 2 {
				return WorkingTreeStatus{}, fmt.Errorf("invalid porcelain v2 record %q", record)
			}
			countXY(&status, parts[1])
			countDirtySubmodule(&status, parts[2])
			if record[0] == '2' {
				// Renames and copies have a second NUL-delimited original path.
				i++
				if i >= len(records) || records[i] == "" {
					return WorkingTreeStatus{}, fmt.Errorf("rename record has no original path")
				}
			}
		case 'u':
			parts := strings.SplitN(record, " ", 4)
			if len(parts) < 3 || len(parts[1]) != 2 {
				return WorkingTreeStatus{}, fmt.Errorf("invalid unmerged porcelain v2 record %q", record)
			}
			status.Conflicted++
			countDirtySubmodule(&status, parts[2])
		case '?':
			if !strings.HasPrefix(record, "? ") {
				return WorkingTreeStatus{}, fmt.Errorf("invalid untracked porcelain v2 record %q", record)
			}
			status.Untracked++
		case '!':
			if !strings.HasPrefix(record, "! ") {
				return WorkingTreeStatus{}, fmt.Errorf("invalid ignored porcelain v2 record %q", record)
			}
			status.Ignored++
		default:
			return WorkingTreeStatus{}, fmt.Errorf("unknown porcelain v2 record %q", record)
		}
	}
	return status, nil
}

func countXY(status *WorkingTreeStatus, xy string) {
	if xy[0] != '.' {
		status.Staged++
	}
	if xy[1] != '.' {
		status.Unstaged++
	}
}

func countDirtySubmodule(status *WorkingTreeStatus, sub string) {
	if len(sub) == 4 && sub[0] == 'S' && sub[1:] != "..." {
		status.DirtySubmodules++
	}
}

// ActiveOperationsContext inspects worktree-specific Git control paths. It
// reports all markers found because damaged repositories can contain more than
// one unfinished operation.
func ActiveOperationsContext(ctx context.Context, repo string) ([]ActiveOperation, error) {
	type marker struct {
		path string
		kind ActiveOperation
		dir  bool
	}
	markers := []marker{
		{path: "rebase-merge", kind: OperationRebase, dir: true},
		{path: "rebase-apply", kind: OperationRebase, dir: true},
		{path: "MERGE_HEAD", kind: OperationMerge},
		{path: "CHERRY_PICK_HEAD", kind: OperationCherryPick},
		{path: "REVERT_HEAD", kind: OperationRevert},
		{path: "BISECT_LOG", kind: OperationBisect},
		{path: "sequencer", kind: OperationSequencer, dir: true},
	}
	args := []string{"rev-parse", "--path-format=absolute"}
	for _, item := range markers {
		args = append(args, "--git-path", item.path)
	}
	out, err := runContext(ctx, repo, args...)
	if err != nil {
		return nil, err
	}
	paths := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(paths) != len(markers) {
		return nil, fmt.Errorf("Git returned %d operation paths, expected %d", len(paths), len(markers))
	}

	seen := make(map[ActiveOperation]struct{}, len(markers))
	var operations []ActiveOperation
	for index, item := range markers {
		path := paths[index]
		if !filepath.IsAbs(path) {
			path = filepath.Join(repo, path)
		}
		path = filepath.Clean(path)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect Git operation marker %s: %w", path, err)
		}
		if (item.dir && !info.IsDir()) || (!item.dir && !info.Mode().IsRegular()) {
			return nil, fmt.Errorf("Git operation marker has unexpected type: %s", path)
		}
		if _, exists := seen[item.kind]; exists {
			continue
		}
		seen[item.kind] = struct{}{}
		operations = append(operations, item.kind)
	}
	return operations, nil
}
