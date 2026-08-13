# goworktree workflows

Status: draft 0.4 — decisions from workflow debate round 1

This document defines intended product behavior. It does not describe the
current implementation. Code, CLI output, and TUI behavior must converge on
this specification.

## Product model

The primary product entity is a **Work**.

A Work represents one unit of development, normally a ticket or task, and
contains:

- one directory under the configured works root;
- one or more Git worktrees from configured repositories;
- one newly created local branch named after the Work in every included
  repository;
- a manifest containing the intended composition of the Work;
- a generated `go.work` file when the included repositories contain Go
  modules;
- optional harness files introduced by future versions.

The product lifecycle is:

```text
New Work
  → Open and use Work
  → Modify Work
      → Add repository
      → Remove repository
      → Rename Work
      → Change Work settings or harness
  → Sync Work
  → Repair or continue Work when necessary
  → Remove Work
```

The user decides when work is finished. The application does not assign a
`ready` or `done` status.

### Terminology

| Term | Meaning |
| --- | --- |
| Work | The user-facing development unit managed by `goworktree` |
| Source repository | A configured primary Git repository from which a worktree is created |
| Worktree | A Git worktree directory included in a Work |
| Work branch | The local branch created specifically for one repository in one Work |
| Harness | Work-level files that connect or configure included repositories |
| Manifest | Desired composition and identity of a Work |
| Inspection | Read-only observation of Git, filesystem, manifest, and recovery state |
| Operation record | Durable information needed to reconcile or continue an interrupted mutation |

The current configuration key may remain `projects_root` for compatibility.
Product language and new interfaces use **Work**, not **Project**. Renaming
persisted configuration is a migration decision, not part of this draft.

## Product goal

Behavior must be deterministic and explainable:

- the same inspected state and input produce the same plan;
- a valid action has explicit verified postconditions;
- an invalid or unsafe action returns a precise error and valid next action;
- expected input, Git, filesystem, and process failures never cause a panic;
- a crash or interruption leaves enough information to inspect what happened;
- a repeated operation does not blindly repeat Git commands and does not make
  the state worse.

External commands and filesystems can fail. The requirement is not “nothing
fails”; it is that known failures produce known, recoverable states.

## Product decisions

The following are fixed for the first architectural refactor.

1. There is no Import workflow. Existing unrelated branches or worktrees are
   not silently adopted.
2. The user supplies one Work name. New Work uses that same value as the local
   branch name in every selected source repository.
3. A branch collision blocks New Work before any mutation. The application
   does not reuse the branch, derive another name, or invent an automatic
   suffix. Choosing a valid unused Work name is the user's responsibility.
4. Resume applies only to a Work already identified by its manifest and
   operation record. Resume is not Import.
5. Sync performs fetch and rebase and is designed to preserve the user's
   current staged, unstaged, and untracked changes automatically.
6. Sync processes the whole batch. A repository-level conflict does not stop
   independent repositories from being processed.
7. After batch Sync, the user handles the collected repository errors and
   conflicts. Running Sync again performs inspection and continues only valid
   steps; it is not a blind second rebase.
8. Remove Work is intentional complete removal. After an explicit plan and
   confirmation, it may delete managed local work branches even when they
   contain unique local commits.
9. Remote repository branches are never pushed, rewritten, or deleted by
   New, Modify, Sync, Repair, or Remove Work.
10. The initial harness is `go.work`. `AGENTS.md`, `Taskfile.yml`, and other
    generated harness files are possible future extensions, not current
    requirements.
11. The minimum supported interactive terminal size is `80×24` for the first
    refactor.
12. New Work fetches the configured remote before mutation and creates branches
    from resolved remote base commit OIDs. An explicit offline mode may instead
    use locally known refs; there is no silent offline fallback.
13. Work names use ASCII letters, digits, `.`, `_`, and `-`, begin with a letter
    or digit, contain no path separator or whitespace, and must also pass Git
    branch-name validation.
14. Initial `go.work` discovery includes only a `go.mod` at the root of each
    selected worktree. Nested modules are not added automatically.
15. Partial New Work state is preserved for Resume. It is not automatically
    rolled back.
16. Requested automatic opening happens only after the Work itself is fully
    verified. Open Work remains available as a separate action.
17. New Work stops on the first repository creation failure. Verified partial
    state is preserved and continued through Resume; later repositories remain
    pending.

## Safety invariants

1. Uncommitted user changes are never silently discarded.
2. Existing user stashes are never addressed by mutable positions such as
   `stash@{0}`, modified, or deleted by the application.
3. Remote repository branches are never mutation targets.
4. A mutation acts only on repositories, paths, refs, and commit identities
   present in its confirmed plan.
5. Every filesystem mutation target must be proven to belong to the expected
   Work and remain inside the configured works root.
6. Local branch deletion uses a full local ref and verifies its commit identity
   immediately before deletion.
7. A normal Remove Repository action does not imply branch deletion. Branch
   deletion is an explicit part of the plan.
8. Remove Work may delete unique local commits because complete deletion is its
   declared purpose. The plan must show the affected branch, tip commit, and
   count of commits not reachable from the selected comparison refs.
9. Success is returned only after inspecting the resulting Git and filesystem
   state.
10. Partial batch results preserve the outcome of every repository: completed,
    unchanged, failed, conflicted, or not started.
11. An interrupted operation is reconciled from observed state. A persisted
    status is not accepted as proof that a Git command succeeded or failed.
12. Locks prevent conflicting `goworktree` instances from mutating the same
    Work or source repository concurrently. Locks cannot prevent manual Git or
    IDE changes, so state is checked again immediately before mutation.
13. An unexpected panic is caught only at the process boundary. The process
    exits unsuccessfully, preserves recovery information, and does not continue
    mutation.

## Sources of truth

The application keeps separate kinds of information:

- The manifest stores intent: Work identity, expected worktrees, source
  repository identities, work branches, base branches, and harness settings.
- Git and the filesystem provide observed facts: paths, registrations,
  repository identities, current branches, local changes, refs, commit IDs,
  and active Git operations.
- The operation record stores the plan, phases, immutable identifiers, and
  checkpoints required for reconciliation after interruption.

Observed state wins when it conflicts with a stale status. The application
does not repair the conflict by guessing user intent.

### Version-1 New Work persistence

New Work derives an opaque Work ID from the canonical works root and exact Work
name. Its schema-versioned manifest lives inside the Work; its external
operation record lives under
`<control-root>/operations/new-work/<work-id>.json`. The two records share the
same Work and operation IDs.

Persistent JSON files are published atomically after file sync and followed by
parent-directory sync. Initial creation is exclusive. Existing symlinks and
non-regular metadata files are not followed or replaced. A completed New Work
record is retained in phase `created` until a separate retention/archive policy
is defined.

## User-visible state

There is no aggregate `ready`, `current`, or `done` status.

A normal Work is shown without a status badge. The application displays facts
or problems only when useful, for example:

```text
EVOVPC-3048
  4 repositories
  2 repositories have local changes
  Last sync: 2026-08-11 17:42
```

Exceptional states are concrete:

- Work creation is incomplete;
- rebase is paused in `billing`;
- previous operation was interrupted;
- expected worktree directory is missing;
- worktree registration exists but its directory is missing;
- branch name does not match the manifest;
- Work metadata or repository identity is ambiguous.

Before Sync performs fetch, the application must not claim that a branch is up
to date with the remote. “Last sync” is historical information, not a current
remote status.

## Internal observed state

The UI does not need to expose a status taxonomy, but workflows must inspect at
least:

- path existence and path type;
- canonical source repository and Git common-directory identity;
- Git worktree registration;
- HEAD ref, detached state, and commit OID;
- staged, unstaged, untracked, ignored, and conflicted files;
- active rebase, merge, cherry-pick, or other Git operation;
- expected local branch ref and its commit OID;
- resolved base commit OID after fetch;
- operation-record phase and last verified checkpoint;
- whether the requested action is safe in the observed state.

`unknown` is a valid internal result. A failed inspection never becomes a
healthy default. Unsafe mutations are disabled until the required fact is
known.

## Interfaces

The TUI is the discoverability interface. A user must be able to complete normal
workflows without memorizing commands.

The CLI is a complete deterministic interface for automation, testing,
diagnostics, and advanced use.

Both call the same typed application workflows. The TUI must not launch a
second CLI process and infer state from human-readable output.

Git terminology is acceptable because the user is a developer. Errors must
still state the exact problem and a valid next action:

```text
api: worktree registration exists, but directory is missing
Next: prune the stale registration and resume New Work
```

## TUI contract

### Information architecture

```text
Setup / Empty state
        ↓
Works home snapshot
        ↓
Work detail
        ↓
Repository detail

Any context → scoped action palette
Mutation → Inputs → Plan → Progress → Verified result / Recovery
```

- Home is a snapshot and navigation surface, not a continuously authoritative
  remote-status monitor.
- Relevant unavailable actions remain visible and explain why they are
  disabled. Irrelevant actions may be hidden.
- Refresh preserves selected Work, repository, scroll position, and active
  operation context.
- Slow inspection and Git operations do not block input handling.
- Every mutation shows the current repository, atomic step, batch progress,
  timeout information, and whether cancellation is currently possible.
- Conflicts use an `Action required` screen with Open, Reinspect, Continue, and
  Abort actions when allowed by observed Git state.

### Keyboard contract

Outside text input:

| Key | Action |
| --- | --- |
| `j` / `k` | Move down / up |
| `h` | Go back |
| `l` / `Enter` | Open or select |
| `g` / `G` | First / last item |
| `Ctrl+u` / `Ctrl+d` | Half-page up / down |
| `/` | Search |
| `n` / `N` | Next / previous search result |
| `:` | Open scoped action palette |
| `r` | Refresh or reinspect |
| `?` | Context help |
| `Esc` | Close overlay or go back before mutation begins |
| `q` | Quit from navigation state |
| `Ctrl+C` | Request cancellation during an operation; quit outside an operation |

Text fields use normal terminal text input. The application does not implement
Vim insert and normal modes.

During mutation, `q` and `Esc` never silently stop a process. Cancellation is a
separate explicit request whose effect depends on the current safe boundary.

### Layout

- `80×24` is the minimum supported size for the first refactor.
- At `80×24`, all workflows remain usable with vertical scrolling.
- Larger terminals may use two-column Work and repository detail views.
- Below `80×24`, the TUI shows the required and current size instead of a
  partially broken layout.
- Resize never loses the active operation.
- Plans and destructive targets can always be viewed in full.

## Operation model

Every mutation follows:

```text
inspect
→ build plan
→ confirm when required
→ acquire Work and repository locks
→ reinspect and validate confirmed plan
→ record step intent
→ execute one atomic step
→ inspect its effect
→ checkpoint the observed result
→ verify final postconditions
```

A confirmed plan is tied to the inspected repository identities, paths, refs,
and commit OIDs. If reinspection changes a mutation target or action, the old
confirmation is invalid and the updated plan must be confirmed.

The operation record is a write-ahead recovery mechanism, not a cross-system
transaction. Git, filesystem, manifest, and operation record cannot be changed
atomically together. Every mutable step therefore needs reconciliation rules
for interruption before, during, and after its Git or filesystem command.

Operation phases visible to the TUI are:

```text
planning
awaiting-confirmation
queued
running-step
cancel-requested
paused-for-user
verifying
succeeded
partial
failed-known-state
interrupted
```

Timeout means that the application stopped waiting within the configured
deadline. It does not by itself prove that the child process stopped or that
the mutation rolled back. Post-timeout inspection determines the next action.

## Error contract

Every expected error includes:

- stable code;
- workflow and phase;
- Work and repository identity when applicable;
- underlying Git, process, or filesystem cause;
- whether mutation may have occurred;
- observed state after failure when inspection succeeded;
- valid recovery actions.

Initial codes:

| Code | Meaning |
| --- | --- |
| `invalid-input` | Input is missing, invalid, or internally inconsistent |
| `unsafe-path` | Target identity or containment cannot be proven |
| `not-found` | Required Work, repository, ref, path, or program is absent |
| `already-exists` | A new path, branch, ref, or Work collides with existing state |
| `state-conflict` | Observed state contradicts manifest intent or the confirmed plan |
| `git-conflict` | Git paused for manual conflict resolution |
| `restore-conflict` | Pre-sync user changes could not be restored cleanly |
| `locked` | Another operation owns a required Work or repository lock |
| `timeout` | Waiting exceeded a deadline; final process state requires inspection |
| `interrupted` | An operation ended with a recoverable record |
| `external-failure` | Git, an editor, or the filesystem returned a known failure |
| `invalid-state` | Persistent or observed state is corrupt or ambiguous |
| `internal` | An invariant failed or an unexpected panic reached the boundary |

## Workflow specification contract

Each workflow must eventually define:

```text
Inputs
Inspected facts
Eligibility predicate
Plan fields
Required locks
Confirmation policy
State and phase transitions
Mutations and checkpoints
Verified postconditions
Crash reconciliation
Errors
Allowed recovery actions
Repeat and resume behavior
```

The workflows below define product behavior. Each must be expanded into this
full contract before its implementation is considered complete.

## Workflows

### WF-01: Inspect Works

**Goal:** show observed local state without mutation.

Normative single-Work specification:
[workflows/INSPECT_WORK.md](workflows/INSPECT_WORK.md). Inspect Works composes
that primitive across independently discovered Work names.

1. Load and validate configuration.
2. Enumerate Work directories. An absent or empty works root is a valid empty
   state.
3. Load each manifest and unfinished operation record independently.
4. Inspect expected paths, Git identities, registrations, branches, local
   changes, and active Git operations.
5. Return every Work with concrete facts and problems.

One unreadable Work does not prevent other Works from being displayed. The
snapshot includes its inspection time. Remote state is not refreshed implicitly.

### WF-02: Configure environment and repository catalog

**Goal:** initialize or intentionally update roots, repository metadata, base
preferences, and programs.

This workflow covers:

- initial setup;
- rescan after repositories are added, removed, or moved;
- changing repository alias, source path, base preference, or remote;
- changing works root;
- configuring programs used by Open Work.

Configuration changes show their exact effect before atomic save. Existing
explicit aliases and base preferences are not overwritten by rescan. Moving the
works root requires its own migration plan and is not Repair.

### WF-03: New Work

**Goal:** create a new Work with new local work branches and verified worktrees.

Normative detailed specification: [workflows/NEW_WORK.md](workflows/NEW_WORK.md).

Inputs include Work name, repository selection, optional base overrides, and
optional program to open after success. The Work name is also the local branch
name in every selected repository.

Rules:

1. The Work name must be valid both as one directory component and as a Git
   branch name. It is not silently normalized.
2. The work directory, same-named local work branch, and destination worktree
   path must not already exist in any selected repository.
3. All selected repositories, fetched base commit OIDs, branch names, target
   paths, and repository identities are validated before creating the Work
   directory. Offline behavior must be explicitly requested.
4. A collision in any selected repository blocks the whole New Work plan before
   mutation. The application does not reuse an existing branch or propose a
   generated replacement.
5. After confirmation, desired state and recovery intent are persisted.
6. Each branch and worktree is created, inspected, and checkpointed separately.
7. A repository is added to the generated root `go.work` only when its worktree
   root contains `go.mod`. Nested modules are not discovered automatically.
8. Final success requires all requested worktrees and harness files to match the
   verified plan.

If execution stops after partial creation, the same Work is resumed from its
manifest and operation record. Resume skips verified results and retries only
valid incomplete steps. Partial state is not automatically rolled back.

If automatic opening was requested, it runs only after all Work postconditions
are verified. Failure to launch an external program does not turn a successfully
created Work into an incomplete Work.

### WF-04: Open Work

**Goal:** open the Work root or a selected repository in a configured program.

1. Inspect the selected target path.
2. Resolve and validate the selected or default program.
3. Show the exact path or generated workspace passed to the program.
4. Launch with an argument vector, never a shell command string.
5. Distinguish successful process launch from proof that an external GUI opened
   the target correctly.

Opening is allowed during normal local changes. Unsafe or missing target paths
are blocked.

### WF-05: Modify Work settings and harness

**Goal:** intentionally update Work-level metadata or generated harness files.

The first version supports regeneration of `go.work` after composition changes.
Future versions may add managed `AGENTS.md`, `Taskfile.yml`, environment files,
or other harness components. Generated files must have explicit ownership and
must not overwrite an unmanaged user file silently.

Repair does not substitute for intentional modification.

### WF-06: Add repository to Work

**Goal:** create a new local work branch and worktree for an additional source
repository.

The repository must not already belong to the Work by ID or canonical Git
identity. The target branch and path must not already exist. The operation uses
the same preflight, branch-creation, verification, checkpoint, and `go.work`
regeneration rules as New Work.

### WF-07: Remove repository from Work

**Goal:** remove a selected worktree and update the Work harness.

1. Inspect local changes, active Git operations, registration, branch ref, tip
   OID, and commits unique to the work branch.
2. Block when removal would silently discard uncommitted changes.
3. Plan worktree removal and local branch deletion as separate effects.
4. Branch deletion is opt-in for this workflow.
5. Verify that the planned branch ref still points to the confirmed OID and is
   not checked out by another worktree immediately before deletion.
6. Remove manifest membership only after the requested effects are observed.
7. Regenerate `go.work` without the removed repository.

A partial result remains resumable. No remote branch is changed.

### WF-08: Sync Work

**Goal:** fetch and rebase every included work branch while restoring the user's
working state as closely as Git permits.

Normative detailed specification: [workflows/SYNC_WORK.md](workflows/SYNC_WORK.md).

For each repository:

1. Inspect branch, HEAD, staged, unstaged, untracked, ignored, conflicted, and
   submodule state.
2. Preserve staged, unstaged, and untracked changes using an application-owned
   recovery object identified by immutable commit OID.
3. Do not modify or delete existing user stash entries.
4. Fetch the configured remote and resolve the selected base to a commit OID.
5. Rebase the expected work branch onto that exact base OID.
6. Restore preserved changes, including index state.
7. Inspect and checkpoint the final state.

Ignored files normally remain in place. If they would be overwritten or make
the operation unsafe, that repository is reported as blocked. Dirty submodules
are blocked in the first refactor until a specific preservation policy exists.

Batch behavior:

- repository-level failure or rebase conflict is recorded and processing
  continues with independent repositories;
- an application-wide invariant failure, unavailable control store, invalid
  repository identity, or impossible lock set stops the batch;
- final result lists completed, unchanged, conflicted, restore-conflicted,
  failed, and not-started repositories;
- conflict recovery begins after the independent batch has finished;
- a repeated Sync reinspects all repositories, skips verified completed work,
  continues valid paused operations, and runs only remaining valid steps.

Possible repository outcomes are:

- unchanged after fetch;
- rebased and working state restored;
- rebase conflict requiring resolution;
- preserved-change restore conflict;
- blocked before mutation;
- failed with known observed state;
- interrupted and requiring reconciliation.

### WF-09: Rename Work

**Goal:** intentionally change Work identity rather than treating it as Repair.

The plan must account for:

- Work directory rename;
- manifest identity;
- worktree registrations after moving the parent directory;
- local work branch rename in every repository;
- collision checks for the target Work directory and resulting worktree paths;
- collision checks for the new local branch name in every repository;
- `go.work` path regeneration;
- interruption and rollback between repositories.

Rename Work changes Work identity, root path, and all managed local work branch
names. It does not rename or push remote branches. Before mutation, every target
path and branch name must be available and every repository must have no active
Git operation. Dirty files do not by themselves block a local branch rename.

The Git branch rename is a normal `git branch -m` operation. The complete
multi-repository workflow is not atomic, so each verified rename is checkpointed
and a partial result can be resumed or rolled back according to the operation
record. Rename must not be implemented as a simple directory rename.

### WF-10: Repair or continue Work

**Goal:** reconcile manifest, operation record, Git, and filesystem state after
interruption or damage.

1. Inspect each source independently.
2. Preserve invalid persistent files before replacement.
3. Compare recorded step intent with observed postconditions.
4. Classify each step as completed, safe to resume, safe to undo, or requiring
   user choice.
5. Apply only actions valid for the classification.
6. Reinspect before clearing recovery information.

Repair may prune stale registrations and recreate intended missing worktrees. It
does not import unrelated worktrees, adopt unrelated branches, or delete user
data merely to make the manifest consistent.

### WF-11: Remove Work

**Goal:** intentionally and completely remove a Work that is no longer needed.

Normative detailed specification: [workflows/REMOVE_WORK.md](workflows/REMOVE_WORK.md).

1. Inspect all managed worktrees, registrations, local work branches, local
   changes, active Git operations, harness files, and unknown entries under the
   Work root.
2. Build a complete plan containing:
   - every worktree path;
   - every local work branch and tip OID;
   - count of unique local commits that branch deletion makes unreachable from
     the selected comparison refs;
   - every managed harness file;
   - unknown files or directories that complete Work removal will also delete;
   - an explicit statement that remote branches are unchanged.
3. Require the user to type the exact Work name to confirm complete removal.
4. Record removal recovery information outside the Work directory.
5. Remove and verify worktrees and registrations.
6. Delete confirmed local work branches even when they contain unique commits.
7. Remove the Work directory and remaining confirmed contents.
8. Verify absence from filesystem, worktree registrations, and local work refs.

Remove Work is destructive by definition. Its safety comes from exact scope,
explicit confirmation, identity revalidation, and verified results—not from
refusing to perform the requested deletion.

## Verification strategy

Every workflow requires black-box tests with temporary repositories and local
remotes. Tests assert returned typed results and final Git/filesystem state.

Minimum matrix:

- valid clean workflow;
- invalid input and branch/path collision before mutation;
- branch checked out in another worktree;
- existing destination as correct worktree, ordinary directory, foreign Git
  repository, or stale registration;
- staged, unstaged, untracked, ignored, and conflicted files;
- existing user stashes;
- dirty submodule;
- rebase conflict and preserved-change restore conflict;
- batch Sync with mixed results and repeated Sync;
- timeout and process interruption at every mutable checkpoint;
- corrupt manifest and operation record;
- repeated operation after success and partial failure;
- concurrent Works sharing one source repository;
- path traversal, symlink escape, and identity-change attempts;
- local branch movement between confirmation and deletion;
- Remove Work with unique local commits and unknown files;
- proof that remote branches are unchanged by every destructive workflow;
- TUI transition tests for cancellation, conflict recovery, lock contention,
  refresh, focus preservation, and resize around `80×24`.

Snapshot testing of human-readable output alone is insufficient.

## Fixed harness decisions

For initial New Work generation:

- `go.work` uses the maximum root-module `go` directive, with minimum `1.23`;
- included repositories are ordered by stable repository ID;
- only root `go.mod` files participate;
- generation is independent of the installed host Go version;
- no `go.work` is created when there are no root modules.

## Open decisions

1. Whether `go.work.sum` is managed, preserved, or removed with Work harness.
2. Migration from the current project manifest to the version-1 Work manifest.
3. Retention and archive policy for completed operation records.
4. Cross-platform repository identity and filesystem durability fallbacks where
   hard links or directory sync are unavailable.
5. Cancellation behavior for each class of Git subprocess.
6. Exact Rename Work ordering and rollback semantics for directory, local
   branches, worktree registrations, and interruption.
7. Stable machine-readable result and error schemas beyond New Work version 1.
8. Whether future managed `AGENTS.md` or `Taskfile.yml` files are templates,
   generated artifacts, or user-owned files after creation.
