# Remove Work workflow

Status: debate draft 0.1

Parent specification: [../WORKFLOWS.md](../WORKFLOWS.md).

## Goal

Intentionally and completely remove one Work that the user no longer needs.

Remove Work deletes the Work's managed worktrees, managed local work branches,
harness, manifest, and confirmed contents of the Work root. It may intentionally
discard uncommitted files and commits reachable only from those local branches.

It never deletes or rewrites remote repository branches.

## Fixed decisions

1. Remove Work is the terminal lifecycle action. It is not named Finish.
2. The user must inspect a complete removal plan and type the exact Work name.
3. Confirmation authorizes deletion of listed staged, unstaged, untracked, and
   ignored files inside the Work.
4. Confirmation authorizes deletion of managed local work branches even when
   they contain unique local commits.
5. Remote branches and remote-tracking refs are not deletion targets.
6. Branch deletion and path deletion are guarded by identity checks immediately
   before mutation.
7. Removal is resumable and never depends on files inside the Work root for its
   recovery record.
8. No network operation is required.

## Inputs

| Input | Required | Meaning |
| --- | --- | --- |
| Work identity | yes | Existing Work selected for complete removal |
| Typed confirmation | yes before mutation | Exact case-sensitive Work name |

There is no ordinary force flag that bypasses inspection or typed confirmation.
A non-interactive CLI must receive an explicit destructive-confirmation option;
exact flag syntax remains a CLI design decision.

## Scope

Remove Work owns only:

- the exact Work root resolved from configuration and manifest identity;
- manifest-listed worktrees that resolve to the expected source repositories;
- Git worktree registrations for those exact paths;
- manifest-listed local full refs in those source repositories;
- operation and harness files owned by the Work;
- additional filesystem entries under the confirmed Work root.

It does not own:

- source repository directories;
- another Work directory;
- a worktree or ref whose identity changed after confirmation;
- any remote ref;
- any path reached by following a symlink outside the Work root;
- a nested mount or filesystem boundary not explicitly supported by the plan.

## Inspected facts

### Work identity

- configured works-root canonical identity;
- Work root canonical identity and path-component chain;
- manifest and operation-record identity;
- symlinks and filesystem boundaries under the removal root;
- managed harness files;
- unknown top-level filesystem entries;
- nested Git repositories or worktrees not listed by the manifest.

### Per managed repository

- configured repository ID and Git common-directory identity;
- expected worktree path and Git registration;
- expected full local ref `refs/heads/<work-name>`;
- checked-out branch, HEAD OID, and active Git operation;
- staged, unstaged, untracked, ignored, and conflicted paths;
- whether the branch is checked out by another registered worktree;
- target branch tip OID;
- commits reachable from the target branch but not from any other retained local
  branch, remote-tracking ref, or tag;
- corresponding remote-tracking branch, when one exists, for information only.

Reflog reachability is not counted as retained branch reachability. A reflog is
not a product-level backup guarantee.

## Eligibility matrix

| Observed state | Remove Work result | Next action |
| --- | --- | --- |
| Valid Work, all managed identities known | eligible | Show complete plan |
| Dirty managed worktree | eligible, destructive fact shown | Confirm exact Work name or cancel |
| Managed branch has unique local commits | eligible, destructive fact shown | Confirm exact Work name or cancel |
| Managed worktree directory is already missing but registration is known | eligible with cleanup | Remove stale registration and branch |
| Worktree exists but registration is missing | eligible only when source identity is proven | Remove exact confirmed directory and branch |
| Expected branch is checked out in another worktree | blocked: `state-conflict` | Inspect the unexpected worktree |
| Active rebase/merge/cherry-pick in managed worktree | blocked | Finish or abort the Git operation first |
| Branch ref or worktree identity differs from manifest | blocked: `state-conflict` | Repair Work |
| Work root is a symlink or resolves outside works root | blocked: `unsafe-path` | Repair configuration/path state |
| Nested mount or filesystem boundary detected | blocked: `unsafe-path` | Move/unmount it explicitly |
| Manifest invalid and no trustworthy external operation record exists | blocked: `invalid-state` | Repair Work |
| Source repository temporarily missing | partial cleanup may be plannable | Remove filesystem scope and retain branch-cleanup record |
| Unknown ordinary file/directory under Work root | eligible, exact entry shown | Confirm deletion or cancel |
| Unknown nested Git repository/worktree | unresolved policy | See decision 1 |

## Unique-commit calculation

For each managed local branch, the plan records:

- full local ref;
- exact tip OID;
- count and abbreviated list of commits reachable from the target branch but not
  from any retained local branch, remote-tracking ref, or tag in that repository;
- whether a corresponding remote-tracking branch currently exists;
- statement that remote state is not refreshed and no network request occurs.

This calculation is a warning and confirmation fact. Unique commits do not block
Remove Work after exact confirmation.

## Plan contract

The plan contains:

### Work-level targets

- Work name and exact root path;
- manifest and harness files;
- ordinary unknown top-level entries with type and recursive size/count when it
  can be computed safely;
- symlinks as symlinks, including their displayed target, without following them;
- any blocked or unresolved nested repository/mount entry;
- operation-record location outside the Work root;
- explicit statement that the operation is irreversible at product level.

### Per-repository targets

- source repository ID and canonical Git identity;
- worktree path and registration;
- dirty/conflicted file counts;
- local branch full ref and expected tip OID;
- unique-commit count and displayed commit summary;
- informational remote-tracking ref, if present;
- exact planned effects: remove worktree, prune registration, delete local ref.

### Explicit non-targets

- source repository directories;
- remote branches;
- remote-tracking refs;
- local branches not listed in the plan;
- paths outside the Work root.

Any target change after confirmation invalidates confirmation for the remaining
scope.

## Confirmation contract

The TUI requires:

1. review of the complete plan;
2. explicit focus on `Remove Work`;
3. typing the exact case-sensitive Work name;
4. final Enter activation.

Copy/paste is allowed. The purpose is intentional scope confirmation, not a
typing challenge.

Confirmation covers dirty files, ignored files, unknown ordinary entries, and
unique local commits shown in the plan. It does not cover an identity or target
that appears later.

## Required locks

Execution requires:

1. external Work lock keyed by Work identity;
2. repository locks for every known canonical Git common directory;
3. stable repository-lock ordering.

The complete lock set is acquired before the first destructive step. After lock
acquisition, the Work root, worktree registrations, branch refs/OIDs, dirty
inventory, unknown entries, and filesystem boundaries are reinspected.

Manual Git and filesystem changes are not prevented by these locks. Every
destructive step repeats its specific identity guard immediately before mutation.

## Operation phases

```text
planning
awaiting-confirmation
acquiring-locks
recording-removal-intent
removing-worktrees
deleting-local-branches
removing-work-root
verifying
removed
partial
cancel-requested
interrupted
failed-known-state
```

## Mutation order

### 1. Record removal intent externally

Before changing the Work root or any repository:

1. Write an external operation record containing the full confirmed plan,
   manifest snapshot, repository identities, refs, OIDs, paths, and phase state.
2. Durably verify that the record can be read without the Work root.
3. Record every target as pending.

If this fails, no destructive mutation starts.

### 2. Remove managed worktrees

For each repository in stable plan order:

1. Revalidate worktree path, source identity, registration, expected branch,
   active Git operation, and planned filesystem scope.
2. Record step intent.
3. Remove the exact Git worktree, intentionally including confirmed dirty files.
4. If the registered worktree is partially broken, remove only the confirmed
   path without following symlinks and prune its exact stale registration.
5. Verify path absence and registration absence.
6. Checkpoint the result externally.

If a repository target changed, stop destructive processing and return a partial
result requiring a new plan for remaining targets.

### 3. Delete managed local branches

After a repository's worktree and registration are absent:

1. Revalidate the exact full local ref and expected tip OID.
2. Verify the branch is not checked out by any registered worktree.
3. Record branch-deletion intent.
4. Atomically compare-and-delete the local ref only if it still points to the
   confirmed OID.
5. Remove branch-specific local configuration owned by that ref.
6. Verify local-ref absence.
7. Verify remote and remote-tracking refs were unchanged.
8. Checkpoint the result externally.

An OID mismatch never becomes an automatic force deletion. It invalidates the
remaining plan and requires updated confirmation.

### 4. Remove remaining Work-root contents

After managed worktrees are absent:

1. Reinspect remaining top-level entries, symlinks, mounts, and filesystem
   boundaries.
2. Compare them with the confirmed plan.
3. If an unconfirmed entry appeared, stop and request an updated plan.
4. Record root-removal intent.
5. Remove confirmed entries without following symlinks outside the Work root.
6. Remove the Work root itself.
7. Verify root absence.

Once deletion of one confirmed filesystem entry begins, that entry is removed to
completion or until the filesystem returns an error. Cancellation applies only
at the safe boundary before the next entry.

### 5. Final verification

Verify:

- Work root is absent;
- every managed worktree registration is absent;
- every confirmed local work branch is absent;
- source repository paths still exist and retain their original identity;
- remote and remote-tracking refs equal their pre-removal snapshot;
- no pending target remains in the external operation record.

Only then is Remove Work successful.

## Partial result and Resume

Remove Work is not rolled back automatically. Deleted files, worktrees, and refs
remain deleted.

On partial failure, the external operation record lists:

- removed worktrees;
- deleted local refs;
- pending targets;
- changed targets requiring a new plan;
- source repositories unavailable for branch cleanup;
- Work-root deletion state;
- exact recovery actions.

Resume:

1. Loads the external record without trusting the Work root.
2. Reacquires the required lock set.
3. Reinspects all completed and pending targets.
4. Treats verified absence as completed instead of repeating deletion.
5. Continues only pending targets whose identities still match.
6. Requires a new plan and typed confirmation for changed or newly discovered
   targets.

## Cancellation and timeout

- Cancellation before mutation leaves no unfinished removal.
- During removal, `Ctrl+C` records `cancel-requested`.
- Cancellation is honored between repository steps and confirmed top-level
  filesystem entries.
- A running Git or filesystem deletion step is not killed mid-command.
- No new destructive step starts after a safe cancellation boundary.
- A partial result and external recovery record remain.

Git timeout does not imply successful termination or rollback. Process and Git
state are inspected before continuing or returning.

## Crash reconciliation

| Recorded intent and observed state | Classification | Resume action |
| --- | --- | --- |
| Worktree path and registration still match | step not applied | Retry removal after guards |
| Path absent and registration absent | worktree removal completed | Checkpoint and continue |
| Path absent but registration remains | partial worktree removal | Prune exact registration and verify |
| Path exists but identity changed | unsafe external change | Stop and replan |
| Local ref exists at expected OID | branch deletion not applied | Compare-and-delete ref |
| Local ref absent | branch deletion completed | Checkpoint and continue |
| Local ref exists at a different OID | external branch movement | Stop and require new confirmation |
| Work root absent, repository targets complete | root removal completed | Final verification |
| Work root present with confirmed remaining entries | root removal pending/partial | Resume at safe entry boundary |
| New unconfirmed entry exists | plan invalidated | Show updated plan and reconfirm |
| External record unreadable | invalid recovery state | Stop automatic deletion; preserve diagnostics |

## Expected errors

| Error | Meaning | Recovery |
| --- | --- | --- |
| `invalid-input` | Work identity or confirmation is invalid | Select and type exact Work name |
| `not-found` | Work or required source repository is absent | Inspect and reconcile scope |
| `unsafe-path` | Containment, symlink, mount, or identity safety failed | Repair path state manually |
| `state-conflict` | Branch, worktree, registration, or contents changed | Reinspect and build a new plan |
| `locked` | Required Work/repository lock unavailable | Wait and retry |
| `external-failure` | Git or filesystem deletion failed | Inspect partial state and Resume |
| `timeout` | Waiting expired; process result is not yet proven | Inspect before Resume |
| `interrupted` | External record contains unfinished removal | Resume Remove Work |
| `invalid-state` | Manifest or recovery information is ambiguous | Repair; do not continue deletion automatically |
| `internal` | Invariant or unexpected panic failure | Preserve record and diagnostics; inspect manually |

## Acceptance test matrix

### Planning and confirmation

- clean Work with one and multiple repositories;
- dirty staged, unstaged, untracked, ignored, and conflicted paths;
- local branch with zero, one, and many unique commits;
- corresponding remote-tracking branch exists and remains unchanged;
- unknown ordinary files and directories;
- symlink inside Work points outside root;
- nested mount/filesystem boundary;
- branch checked out in another worktree;
- worktree path, registration, branch ref, or OID changes after confirmation;
- incorrect typed Work name performs no mutation.

### Successful removal

- registered clean worktrees;
- registered dirty worktrees after explicit confirmation;
- missing directory with stale registration;
- directory present with missing registration and proven identity;
- unique local branch commits intentionally become unreferenced;
- `go.work`, `go.work.sum`, manifest, and ordinary confirmed files removed;
- source repositories retain identity and contents;
- remote and remote-tracking refs remain byte-for-byte/OID equivalent.

### Partial and interrupted removal

- interruption before and after each worktree removal;
- interruption before and after each branch deletion;
- branch moves between confirmation and compare-and-delete;
- source repository disappears after worktree removal;
- new unknown file appears before Work-root deletion;
- filesystem error during top-level entry removal;
- Work root removed before final checkpoint;
- repeated Resume never repeats verified deletion;
- cancellation between repositories and top-level entries;
- external operation record remains usable without Work root.

## Unresolved decisions for Remove Work

### 1. Unknown nested Git repository or worktree

An ordinary unknown file is deleted after exact confirmation. A nested Git
repository may contain unrelated history and may own a worktree registration in
an unconfigured source repository.

Choose:

- delete it as confirmed Work-root content; or
- block Remove Work until the user moves or explicitly unregisters it.

Recommendation: block. A typed Work name confirms the known Work scope; it
should not silently expand ownership to an unrelated Git repository.

### 2. Completed removal receipt

Choose whether the external control store retains a metadata-only receipt after
successful removal. It would contain Work identity, plan, deleted refs/OIDs,
timestamps, and outcomes, but no file contents or Git-object backup.

Recommendation: retain a removal receipt for diagnostics until explicit control
data cleanup. It does not make deleted data recoverable.
