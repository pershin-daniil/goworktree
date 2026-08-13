# Inspect Work workflow

Status: draft 0.1

Parent specification: [../WORKFLOWS.md](../WORKFLOWS.md).

## Goal

Return a read-only snapshot of one named Work by reconciling recorded intent
with current filesystem and local Git facts.

Inspect Work does not decide whether the user's development is complete. It
does not assign `ready`, `done`, `current`, or remote freshness states.

## Inputs

| Input | Required | Meaning |
| --- | --- | --- |
| Works root | yes | Existing canonical parent directory of Works |
| Control root | yes | Location of durable operation records |
| Work name | yes | Exact Work directory and local branch name |

The name is parsed with the same contract as New Work. The Work identity and
all expected metadata paths are derived from the canonical works root and exact
name; they are not accepted from the caller.

## Non-mutation contract

Inspect Work:

- does not fetch or contact a remote;
- does not refresh or write the Git index;
- does not prune worktree registrations;
- does not create, replace, repair, or delete files, refs, or directories;
- does not acquire mutation locks;
- does not treat a previous successful checkpoint as proof of current state.

Because inspection does not lock out manual Git or IDE activity, its result is
a timestamped snapshot rather than a transactionally consistent guarantee.
Every mutation must acquire its locks and inspect its required facts again.

## Intent sources

Inspect Work reads these files independently:

- `<work-root>/.goworktree.json` — current Work composition intent;
- `<control-root>/operations/new-work/<work-id>.json` — durable New Work plan
  and recovery checkpoints.

Each file is classified as `absent`, `valid`, or `invalid`. Invalid JSON,
unsupported schemas, unsafe metadata file types, invalid embedded paths, and
identity mismatches are reported explicitly.

Intent is selected in this order:

1. a valid manifest;
2. a valid matching operation record when no valid manifest is available;
3. no intent when neither source is valid.

When both records are valid, their Work IDs, operation IDs, repository order,
source identities, refs, immutable initial base OIDs, destinations, and harness
membership must agree. A disagreement is a problem; it is not resolved by
choosing whichever file was modified most recently.

An incomplete valid New Work operation produces `resume-new-work` only when a
valid manifest agrees with it, or when the manifest is absent at an absent or
real-directory Work root because publication may not have happened yet. An
invalid or mismatched manifest routes to Repair. This is not Import: only the
exact recorded plan may be resumed.

## Inspected facts

### Work scope

- inspection timestamp;
- Work identity, root, and root path type;
- manifest and operation-record state;
- selected intent source;
- harness path and path type;
- whether the harness still matches the verified New Work operation.

### Per repository

- repository ID and recorded intent;
- canonical source path and Git common-directory identity;
- expected local branch existence and tip OID;
- all worktree registrations for the expected branch;
- registration, if any, at the expected destination;
- destination path type;
- checkout repository identity, full ref, detached state, and HEAD OID;
- staged, unstaged, untracked, ignored, conflicted, and dirty-submodule counts;
- active rebase, merge, cherry-pick, revert, bisect, or sequencer markers.

Branch, registration, checkout, working-tree, and active-operation facts each
carry an explicit known/unknown state. An inspection failure never becomes a
zero-valued healthy result.

## Reconciliation rules

Inspect Work reports concrete contradictions, including:

- Work root, manifest, or operation record missing or unsafe;
- manifest or operation identity not belonging to the requested Work;
- configured source resolving to another Git common directory;
- expected local branch missing;
- expected worktree directory missing or not a Git worktree;
- stale, missing, duplicate, or conflicting worktree registration;
- checkout ref or repository identity not matching intent;
- branch, checkout, and registration disagreeing on HEAD;
- working-tree or active-operation state being unreadable;
- an unfinished Git operation requiring user resolution;
- missing, unexpected, or changed `go.work` harness.

Dirty working state is an observed fact, not by itself a problem. It is used by
later workflows to decide eligibility and preservation strategy.

A repository failure does not stop independent repositories from being
inspected. Repository problems are attached both to that repository and to the
Work-level problem collection. Cancellation of the supplied context stops the
overall inspection; the returned snapshot may include facts collected before
cancellation.

## Recovery actions

Problems expose stable codes and one of these typed next actions:

| Action | Meaning |
| --- | --- |
| `resume-new-work` | Reconcile and continue the exact recorded New Work plan |
| `resolve-git-operation` | Resolve or abort the reported Git operation, then reinspect |
| `repair-work` | Build a separate explicit repair plan from observed facts |
| `none` | No automated recovery action is implied |

Inspect Work never executes the action it recommends.

## Result contract

A successful call means the requested scope was inspected and a snapshot was
produced. It does not mean the Work has no problems.

A top-level error is reserved for invalid request scope, unavailable required
adapters, invalid roots, cancellation, or another failure that prevents the
snapshot identity from being established. Missing or damaged Work state after
identity is established is represented inside the snapshot.

The CLI and TUI consume the same typed snapshot. Human-readable rendering is an
adapter concern and must not be parsed back into workflow state.

## Inspect Works composition

Inspect Works enumerates candidate Work names and calls Inspect Work for each
independently. An unreadable or invalid directory is represented as a concrete
entry problem and does not hide other Works. Enumeration does not adopt an
unrecorded directory as a Work.
