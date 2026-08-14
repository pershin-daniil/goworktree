package git

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type StashEntry struct {
	Selector string `json:"selector"`
	OID      string `json:"oid"`
	Message  string `json:"message"`
}

type RecoveryRef struct {
	Ref string
	OID string
}

func StashEntriesContext(ctx context.Context, repo string) ([]StashEntry, error) {
	out, err := runContext(ctx, repo, "stash", "list", "--format=%gd%x00%H%x00%gs")
	if err != nil {
		return nil, err
	}
	var entries []StashEntry
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\x00", 3)
		if len(fields) != 3 || fields[0] == "" || fields[1] == "" {
			return nil, fmt.Errorf("parse Git stash entry %q", line)
		}
		entries = append(entries, StashEntry{Selector: fields[0], OID: fields[1], Message: fields[2]})
	}
	return entries, nil
}

// PreserveWorktreeContext moves this operation's temporary stash entry to an
// application-owned ref and restores the pre-existing user stash reflog.
// It is idempotent across crashes before or after each Git mutation.
func PreserveWorktreeContext(ctx context.Context, repo, recoveryRef, marker string, baseline []StashEntry) (string, error) {
	if recoveryRef == "" || marker == "" {
		return "", fmt.Errorf("recovery ref and marker are required")
	}
	recoveryOID, recoveryExists, err := RefOIDContext(ctx, repo, recoveryRef)
	if err != nil {
		return "", err
	}
	entries, err := StashEntriesContext(ctx, repo)
	if err != nil {
		return "", err
	}
	owned := markerEntries(entries, marker)
	if len(owned) > 1 {
		return "", fmt.Errorf("multiple stash entries match recovery marker %q", marker)
	}
	if recoveryExists {
		if len(owned) == 1 {
			if owned[0].OID != recoveryOID {
				return "", fmt.Errorf("recovery ref and marked stash point to different objects")
			}
			if err := deleteOwnedStash(ctx, repo, owned[0], marker); err != nil {
				return "", err
			}
		}
		if err := verifyStashBaseline(ctx, repo, baseline); err != nil {
			return "", err
		}
		return recoveryOID, nil
	}
	if len(owned) == 0 {
		if !sameStashes(entries, baseline) {
			return "", fmt.Errorf("user stash stack changed after preservation intent")
		}
		if _, err := runContext(ctx, repo, "stash", "push", "--include-untracked", "--message", marker); err != nil {
			return "", fmt.Errorf("preserve working tree: %w", err)
		}
		entries, err = StashEntriesContext(ctx, repo)
		if err != nil {
			return "", err
		}
		owned = markerEntries(entries, marker)
	}
	if len(owned) != 1 {
		return "", fmt.Errorf("could not identify the unique preservation stash for marker %q", marker)
	}
	recoveryOID = owned[0].OID
	if _, err := runContext(ctx, repo, "update-ref", recoveryRef, recoveryOID, ""); err != nil {
		return "", fmt.Errorf("create recovery ref %s: %w", recoveryRef, err)
	}
	verified, exists, err := RefOIDContext(ctx, repo, recoveryRef)
	if err != nil || !exists || verified != recoveryOID {
		return "", fmt.Errorf("verify recovery ref %s: %w", recoveryRef, err)
	}
	if err := deleteOwnedStash(ctx, repo, owned[0], marker); err != nil {
		return "", err
	}
	if err := verifyStashBaseline(ctx, repo, baseline); err != nil {
		return "", err
	}
	return recoveryOID, nil
}

func ApplyRecoveryContext(ctx context.Context, repo, recoveryOID string) error {
	if strings.TrimSpace(recoveryOID) == "" {
		return fmt.Errorf("recovery object ID is empty")
	}
	if _, err := runContext(ctx, repo, "stash", "apply", "--index", recoveryOID); err != nil {
		return fmt.Errorf("apply recovery object %s: %w", shortRevision(recoveryOID), err)
	}
	return nil
}

func DeleteRecoveryRefContext(ctx context.Context, repo, recoveryRef, expectedOID string) error {
	oid, exists, err := RefOIDContext(ctx, repo, recoveryRef)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if oid != expectedOID {
		return fmt.Errorf("recovery ref %s changed from %s to %s", recoveryRef, shortRevision(expectedOID), shortRevision(oid))
	}
	if _, err := runContext(ctx, repo, "update-ref", "-d", recoveryRef, expectedOID); err != nil {
		return fmt.Errorf("delete recovery ref %s: %w", recoveryRef, err)
	}
	if _, exists, err := RefOIDContext(ctx, repo, recoveryRef); err != nil || exists {
		return fmt.Errorf("verify recovery ref deletion: %w", err)
	}
	return nil
}

func RefOIDContext(ctx context.Context, repo, ref string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", ref)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)), true, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return "", false, fmt.Errorf("git rev-parse --verify %s: %w", ref, contextErr)
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return "", false, nil
	}
	return "", false, fmt.Errorf("git rev-parse --verify %s: %w", ref, err)
}

func ListRecoveryRefsContext(ctx context.Context, repo, prefix string) ([]RecoveryRef, error) {
	out, err := runContext(ctx, repo, "for-each-ref", "--format=%(refname)%00%(objectname)", prefix)
	if err != nil {
		return nil, err
	}
	var refs []RecoveryRef
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\x00", 2)
		if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
			return nil, fmt.Errorf("parse recovery ref %q", line)
		}
		refs = append(refs, RecoveryRef{Ref: fields[0], OID: fields[1]})
	}
	return refs, nil
}

func AbortRebaseAndResetContext(ctx context.Context, repo, expectedBranch, oid string) error {
	operations, err := ActiveOperationsContext(ctx, repo)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		if operation == OperationRebase {
			if _, err := runContext(ctx, repo, "rebase", "--abort"); err != nil {
				return fmt.Errorf("abort rebase: %w", err)
			}
			break
		}
	}
	branch, err := CurrentBranch(repo)
	if err != nil {
		return err
	}
	if expected := strings.TrimPrefix(expectedBranch, "refs/heads/"); expected != "" && branch != expected {
		return fmt.Errorf("current branch %q, expected %q", branch, expected)
	}
	if _, err := runContext(ctx, repo, "reset", "--hard", oid); err != nil {
		return fmt.Errorf("reset branch to %s: %w", shortRevision(oid), err)
	}
	if _, err := runContext(ctx, repo, "clean", "-fd"); err != nil {
		return fmt.Errorf("clean rollback tree: %w", err)
	}
	return nil
}

func deleteOwnedStash(ctx context.Context, repo string, entry StashEntry, marker string) error {
	entries, err := StashEntriesContext(ctx, repo)
	if err != nil {
		return err
	}
	found := false
	for _, current := range entries {
		if current.Selector != entry.Selector {
			continue
		}
		if current.OID != entry.OID || (current.Message != marker && !strings.HasSuffix(current.Message, ": "+marker)) {
			return fmt.Errorf("stash selector %s no longer belongs to this operation", entry.Selector)
		}
		found = true
		break
	}
	if !found {
		return nil
	}
	if _, err := runContext(ctx, repo, "reflog", "delete", "--rewrite", "--updateref", entry.Selector); err != nil {
		return fmt.Errorf("delete preservation stash %s: %w", entry.Selector, err)
	}
	return nil
}

func verifyStashBaseline(ctx context.Context, repo string, baseline []StashEntry) error {
	current, err := StashEntriesContext(ctx, repo)
	if err != nil {
		return err
	}
	if !sameStashes(current, baseline) {
		return fmt.Errorf("user stash stack changed during preservation")
	}
	return nil
}

func sameStashes(left, right []StashEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].OID != right[index].OID || left[index].Message != right[index].Message {
			return false
		}
	}
	return true
}

func markerEntries(entries []StashEntry, marker string) []StashEntry {
	var found []StashEntry
	for _, entry := range entries {
		if entry.Message == marker || strings.HasSuffix(entry.Message, ": "+marker) {
			found = append(found, entry)
		}
	}
	return found
}
