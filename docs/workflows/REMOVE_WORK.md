# Remove Work workflow

Status: debate draft 0.1

Parent specification: [../WORKFLOWS.md](../WORKFLOWS.md).

## Goal

Intentionally and completely remove one Work that the user no longer needs.

Remove Work deletes the Work's managed worktrees, managed local work branches,
harness, manifest, and confirmed contents of the Work root. It may intentionally
discard uncommitted files and commits reachable only from those local branches.

It never deletes or rewrites remote repository branches.

Managed local targets include branches retained by earlier Drop operations.
These appear as retained-branch-only targets in the plan, with no worktree
deletion. Their source identity and recorded OID must still match, and the
branch must not be checked out elsewhere. An already-missing retained ref is
accepted. The exact Work-name confirmation also authorizes these listed refs.

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
9. An unfinished Sync operation or private Sync recovery ref blocks removal.
10. Successful removal archives operation metadata, not deleted Work files.
11. A new removal plan requires a valid New Work operation in terminal
    `created` phase. An unfinished or invalid New Work must be resumed or
    repaired first.
12. Dirty counts and top-level entry lists are display facts. Destructive
    authorization uses exact recursive fingerprints and is revalidated after
    intent is recorded, immediately before mutation.

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
- valid completed New Work operation identity and `created` phase;
- symlinks and filesystem boundaries under the removal root;
- managed harness files;
- unknown top-level filesystem entries;
- exact recursive fingerprint of all non-managed Work-root entries, including
  file contents, directory structure, permissions, and symlink targets;
- nested Git repositories or worktrees not listed by the manifest.

### Per managed repository

- configured repository ID and Git common-directory identity;
- expected worktree path and Git registration;
- expected full local ref `refs/heads/<work-name>`;
- checked-out branch, HEAD OID, and active Git operation;
- staged, unstaged, untracked, ignored, and conflicted paths;
- whether the branch is checked out by another registered worktree;
- target branch tip OID;
- exact fingerprint of the Git index and every tracked, untracked, and ignored
  filesystem entry in the worktree, excluding only its top-level `.git`
  administrative entry;
- corresponding remote-tracking branch, when one exists, for information only.

## Eligibility matrix

| Observed state | Remove Work result | Next action |
| --- | --- | --- |
| Valid Work, all managed identities known | eligible | Show complete plan |
| New Work operation is valid and phase is `created` | eligible | Continue Remove Work planning |
| New Work operation is missing, invalid, or unfinished | blocked: `invalid-state` | Resume or Repair New Work first |
| Dirty managed worktree | eligible, destructive fact shown | Confirm exact Work name or cancel |
| Managed branch may have unique local commits | eligible; warning is not implemented in this P1 | Confirm exact Work name or cancel |
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

## Deferred unique-commit warning

Calculating and displaying commits unique to a deleted local branch is a
separate task. This workflow records and compare-deletes the confirmed branch
OID, but does not claim to provide that warning in the current P1 scope.

## Plan contract

The plan contains:

### Work-level targets

- Work name and exact root path;
- exact recursive fingerprint of remaining non-managed root contents;
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
- exact worktree fingerprint used for destructive authorization;
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

Confirmation covers dirty files, ignored files, and unknown ordinary entries.
It also authorizes deletion of each exact local branch OID in the plan. It does
not cover an identity or target that appears later.

## Required locks

Execution requires:

1. external Work lock keyed by Work identity;
2. repository locks for every known canonical Git common directory;
3. stable repository-lock ordering.

The complete lock set is acquired before the first destructive step. After lock
acquisition, the Work root, worktree registrations, branch refs/OIDs, exact
worktree and root fingerprints, unknown entries, and filesystem boundaries are
reinspected.

Manual Git and filesystem changes are not prevented by these locks. Every
destructive step repeats its specific identity guard immediately before mutation.

## Operation phases

The schema-v3 operation has one common phase (`removing-worktrees`,
`removing-branches`, `removing-root`, or `archiving`). Every worktree, branch,
and Work-root checkpoint independently advances through:

```text
pending → intent → completed
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
   active Git operation, and the exact planned worktree fingerprint.
2. Record step intent.
3. Recompute the exact fingerprint after the durable intent checkpoint. If it
   differs, stop without removing the worktree.
4. Remove the exact Git worktree, intentionally including confirmed dirty files.
5. If the registered worktree is partially broken, remove only the confirmed
   path without following symlinks and prune its exact stale registration.
6. Verify path absence and registration absence.
7. Checkpoint the result externally.

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

1. Recompute the exact recursive fingerprint of all remaining entries,
   symlinks, permissions, and filesystem contents.
2. Compare it with the confirmed plan.
3. If any entry appeared or changed at any depth, stop and request an updated
   plan.
4. Record root-removal intent.
5. Recompute and compare the fingerprint again immediately before deletion.
6. Remove confirmed entries without following symlinks outside the Work root.
7. Remove the Work root itself.
8. Verify root absence.

Once deletion of one confirmed filesystem entry begins, that entry is removed to
completion or until the filesystem returns an error. Cancellation applies only
at the safe boundary before the next entry.

### 5. Publish metadata archive and finalize

Verify:

- Work root is absent;
- every managed worktree registration is absent;
- every confirmed local work branch is absent;
- source repository paths still exist and retain their original identity;
- remote and remote-tracking refs equal their pre-removal snapshot;
- no pending target remains in the external operation record.

Then publish `archive.json`, `new-work.json`, optional terminal
`sync-work.json`, optional `change-work.json`, and `remove-work.json` through a hidden staging directory,
fsync, and atomic rename to:

```text
~/.config/goworktree/archives/removed-work/<work>-<UTC>-<8hex>/
```

After the archive verifies, delete active New Work, terminal Sync, and Change Work records,
then delete the active Remove record last. The archive is metadata only; it is
not a backup of worktree files, uncommitted changes, or commits.

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

Schema-v1 and schema-v2 records do not contain exact fingerprints. Resume may
checkpoint a target that is already proved absent, but it must not delete a
still-present legacy worktree or Work root. The user must inspect and remove
that target manually before Resume can finish the remaining checkpoints.

## Cancellation and timeout

- Cancellation before mutation leaves no unfinished removal.
- During removal, `Ctrl+C` requests cancellation.
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
| Worktree path, registration, and exact fingerprint still match | step not applied | Retry removal after guards |
| Worktree fingerprint differs, even with the same dirty counts | external content change | Stop without deletion and require a new plan |
| Path absent and registration absent | worktree removal completed | Checkpoint and continue |
| Path absent but registration remains | partial worktree removal | Prune exact registration and verify |
| Path exists but identity changed | unsafe external change | Stop and replan |
| Local ref exists at expected OID | branch deletion not applied | Compare-and-delete ref |
| Local ref absent | branch deletion completed | Checkpoint and continue |
| Local ref exists at a different OID | external branch movement | Stop and require new confirmation |
| Work root absent, repository targets complete | root removal completed | Final verification |
| Work root present with the confirmed recursive fingerprint | root removal pending/partial | Resume at safe entry boundary |
| Any root entry changed or appeared at any depth | plan invalidated | Show updated plan and reconfirm |
| Legacy record lacks an exact fingerprint and target still exists | authorization unavailable | Inspect and remove target manually; then Resume |
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
- existing worktree content changes without changing dirty counts;
- nested Work-root content, permissions, or symlink target changes after
  confirmation;
- schema-v1 and schema-v2 records never delete still-present targets without an
  exact fingerprint;
- filesystem error during top-level entry removal;
- Work root removed before final checkpoint;
- repeated Resume never repeats verified deletion;
- cancellation between repositories and top-level entries;
- interruption during archive publication and each active-record cleanup;
- completed removal disappears from the dashboard and the Work name is reusable;
- archive list/show/delete, exact deletion confirmation, corrupt digests,
  symlinks/path escape, salt collision, and `.deleting-*` recovery;
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
