# Sync Work workflow

Status: debate draft 0.1

Parent specification: [../WORKFLOWS.md](../WORKFLOWS.md).

## Goal

Update every repository in a Work by fetching its configured remote and
rebasing the local work branch onto an immutable resolved base commit, while
preserving the user's staged, unstaged, and untracked changes.

The successful user experience is:

```text
working state before Sync
→ fetch and rebase
→ working state restored
→ continue working
```

Conflicts are expected outcomes. They are collected across the batch and
resolved after independent repositories finish.

## Fixed decisions

1. Sync means fetch followed by rebase. It does not merge the base branch.
2. Sync processes all independent repositories even when one repository fails
   or pauses on a conflict.
3. Application-wide safety or control-state failure stops the batch.
4. Staged, unstaged, and untracked changes are preserved automatically.
5. Ignored files remain in place. A possible overwrite blocks that repository.
6. Dirty submodules block that repository in the first refactor.
7. Existing user stash entries are preserved.
8. Every rebase uses the exact base commit OID resolved after fetch.
9. A repeated Sync first reconciles an unfinished Sync. It does not blindly
   start another rebase.
10. Remote repository branches are never pushed, rewritten, or deleted.
11. Sync uses standard linear rebase semantics, not `--rebase-merges`.
12. After a manually resolved restore conflict, the recovery ref is deleted
    only after inspection and explicit `Changes restored` confirmation. The
    user may instead keep the backup ref.
13. Cancellation is applied between repository steps. A running Git command is
    allowed to complete or reach its timeout before no further repository starts.

## Inputs

| Input | Required | Meaning |
| --- | --- | --- |
| Work identity | yes | Existing Work to synchronize |
| Repository selection | no | All repositories by default; optional explicit subset |
| Base override | no | One-time base branch override for a selected repository |
| Open program | no | Program used when opening a conflicted worktree |

Base overrides affect the current Sync plan only unless separately saved
through Modify Work.

## Preconditions and inspected facts

### Work-level inspection

- manifest and Work identity are valid;
- no incompatible unfinished Work mutation exists;
- operation records are readable;
- selected repositories belong to the Work;
- Work and repository lock identities are unambiguous.

### Per-repository inspection

- expected source repository and Git common-directory identity;
- expected worktree path and registration;
- checked-out full branch ref and HEAD OID;
- configured remote and base preference;
- staged, unstaged, untracked, ignored, and conflicted files;
- submodule state;
- active rebase, merge, cherry-pick, revert, bisect, or other Git operation;
- application-owned recovery ref from an unfinished Sync;
- recorded Sync phase and last verified checkpoint.

## Eligibility matrix

| Observed state | Repository result before Sync | Next action |
| --- | --- | --- |
| Expected worktree and branch, no active Git operation | eligible | Fetch and classify |
| Expected worktree and branch with ordinary tracked/untracked changes | eligible with preservation | Show preservation in plan |
| Dirty submodule | blocked | Resolve or commit submodule changes |
| Ignored file would be overwritten by planned checkout/rebase | blocked | Move or remove the ignored file |
| Missing or unregistered worktree | blocked: `state-conflict` | Repair Work |
| Wrong branch or detached HEAD | blocked: `state-conflict` | Restore expected branch or Repair Work |
| Unrelated active Git operation | blocked: `state-conflict` | Finish or abort it manually |
| Rebase owned by the current unfinished Sync | recovery required | Open, reinspect, continue, or abort |
| Application recovery ref exists without matching operation record | blocked: `invalid-state` | Repair Work |
| Unmerged index without an owned active Sync | blocked: `state-conflict` | Resolve existing Git conflict |
| Repository identity cannot be proven | application-wide stop | Repair configuration or Work state |

A blocked repository does not prevent independent eligible repositories from
being synchronized unless the problem invalidates application-wide safety.

## Batch preparation

1. Inspect the Work and reconcile any unfinished Sync operation.
2. Determine the complete Work and repository lock set.
3. Acquire the Work lock and repository locks in stable canonical-identity order.
4. Reinspect all mutation targets.
5. Create a Sync operation record before repository mutations.
6. Fetch every eligible repository, collecting fetch failures instead of
   stopping the batch.
7. Resolve each successful base ref to a commit OID and record fetch time.
8. Classify each repository and build the final mutation plan.

The final plan is based on fetched OIDs. A remote branch may advance after fetch;
the confirmed Sync still uses its recorded OID.

## Base relation

After fetch, relation is computed from `HEAD`, `baseOID`, and merge-base:

| Relation | Meaning | Sync action |
| --- | --- | --- |
| `equal` | Work branch HEAD equals base OID | No rebase |
| `contains-base` | Base OID is already an ancestor of work HEAD | No rebase |
| `behind-base` | Work HEAD is an ancestor of base OID | Rebase/fast-forward through Git rebase |
| `diverged` | Both base and Work contain unique commits | Rebase work commits onto base OID |
| `unknown` | Required graph query failed | Block repository |

`contains-base` means synchronized with the fetched base even when the Work has
its own commits. It is not displayed as an evaluative `ready` status.

## Plan contract

The plan contains:

### Work level

- Work identity and path;
- operation ID;
- selected repository count;
- repositories eligible, unchanged, blocked, or failed during fetch;
- statement that repository failures are collected and independent processing
  continues;
- statement that remote branches are unchanged.

### Per repository

- repository identity and worktree path;
- expected work branch and pre-Sync HEAD OID;
- remote, fetched base ref, base OID, and fetch time;
- base relation and planned rebase action;
- counts for staged, unstaged, and untracked paths;
- ignored-file and submodule result;
- whether a preservation object will be created;
- timeout and cancellation boundary.

The TUI shows the plan and a focused `Run Sync` action. No typed-name
confirmation is required. The CLI invocation itself authorizes Sync unless a
plan-only mode was requested.

## Working-state preservation

### Required result

After a successful repository Sync:

- staged paths are staged again;
- unstaged tracked paths are unstaged again;
- untracked files are restored;
- file contents match the pre-Sync state except where the user explicitly
  resolved a rebase or restore conflict;
- existing user stash entries have the same identity and order as before Sync;
- the application recovery ref is deleted only after verification.

### Recovery object

For a dirty repository:

1. Record exact staged, unstaged, untracked, ignored, and submodule inventory.
2. Create a stash-format object including untracked files.
3. Resolve and record its immutable commit OID.
4. Create an application-owned ref under a reserved namespace keyed by operation
   and repository identity.
5. Restore the user's stash list to its pre-Sync identity and order before
   beginning rebase.
6. Verify that tracked and included untracked working state is clean.

The application never relies on `stash@{n}` after creating the recovery object.
The reserved recovery ref keeps the object reachable until restoration is
verified or the user explicitly removes the Work.

If preservation cannot be verified, that repository is not rebased.

## Per-repository phases

```text
queued
fetch-failed
unchanged
preserving
preserved
rebasing
rebase-conflict
restoring
restore-conflict
verifying
succeeded
blocked
failed-known-state
interrupted
```

## Per-repository mutation

For each eligible repository in stable plan order:

1. Revalidate worktree identity, expected branch, pre-Sync HEAD, base object, and
   working-state inventory.
2. If the working state changed after confirmation, invalidate only that
   repository's plan and report `state-conflict`; continue the batch.
3. If dirty, create and verify the application recovery object.
4. Record rebase intent with pre-Sync HEAD and base OID.
5. Rebase the expected work branch onto the exact base OID with controlled
   behavior equivalent to:

   ```text
   git rebase \
     --no-autostash \
     --no-rebase-merges \
     --no-update-refs \
     --no-rerere-autoupdate \
     --verify \
     <baseOID>
   ```

   These controls prevent user Git configuration from introducing a second
   autostash, preserving merge topology, force-updating other local branches,
   or automatically staging a reused conflict resolution. The pre-rebase hook
   remains enabled.
6. Inspect Git operation state and HEAD.
7. If rebase succeeded, restore the recovery object with index state.
8. Inspect conflicts, working state, expected branch, and resulting graph.
9. Checkpoint the repository outcome.

Repositories classified as `equal` or `contains-base` still restore preserved
state if preservation already occurred, but an implementation should avoid
creating a recovery object when no branch mutation is needed.

## Batch behavior

Repository-level outcomes that do not stop later repositories:

- fetch failure;
- preservation failure;
- rebase conflict;
- restore conflict;
- ignored-file collision;
- dirty submodule;
- branch or worktree state conflict;
- timeout with a successfully inspected known state.

Application-wide failures that stop new repository mutations:

- Work manifest or operation record becomes unreadable;
- operation record cannot be durably checkpointed;
- repository identity becomes ambiguous;
- required lock ownership is lost or cannot be established;
- works/control root containment fails;
- process state cannot be inspected after timeout or unexpected termination;
- an internal invariant fails.

When an application-wide failure occurs, already completed outcomes remain
recorded and all not-started repositories are reported explicitly.

## Conflict recovery

After independent batch processing, the result opens an `Action required` list.
The user selects conflicts in any order.

### Rebase conflict

The screen shows:

- repository and worktree;
- conflicted paths;
- completed and remaining batch outcomes;
- preserved working-state object status;
- actions: Open, Reinspect, Continue Rebase, Abort Rebase.

Rules:

1. Returning from an editor or shell always triggers reinspection.
2. Continue is enabled only when Git reports the owned rebase and no unresolved
   index entries remain after the user stages resolutions.
3. Continue may produce another conflict and repeats the same cycle.
4. Successful rebase continuation proceeds to working-state restoration.
5. Abort uses Git's rebase abort, verifies the original pre-Sync HEAD, and then
   restores the preserved working state.
6. The recovery ref remains until working-state restoration is verified.

Aborting one repository does not roll back repositories already synchronized.

### Restore conflict

A restore conflict means rebase finished but application-owned pre-Sync changes
could not be applied cleanly.

The screen shows:

- conflicted paths;
- post-rebase HEAD;
- recovery-object OID/ref;
- actions: Open, Reinspect, Mark Resolved when eligible, Keep Recovery Backup.

The application does not rerun restore blindly. After manual resolution,
`Changes restored` is enabled only when inspection finds no unmerged index
entries. Explicit confirmation records the final inventory and permits deletion
of the private recovery ref. `Keep backup` leaves the ref reachable and reports
its exact name.

## Repeat and Resume

When Sync is invoked with an unfinished Sync record:

1. Do not fetch a new base yet.
2. Acquire the recorded lock set and inspect every repository.
3. Accept verified completed outcomes without repeating them.
4. Route owned active rebases and restore conflicts to recovery.
5. Retry only steps classified as safe from their recorded intent and observed
   state.
6. Complete or explicitly abort the old Sync operation.
7. Only a later fresh Sync may fetch newer base OIDs.

This prevents a repeated Sync from changing the target base while earlier
conflicts are unresolved.

## Cancellation and timeout

Cancellation before mutation exits without an unfinished Sync.

During batch mutation, `Ctrl+C` records `cancel-requested`. A running Git command
is not killed immediately; it is allowed to complete or reach its configured
timeout. No new repository starts after the current step reaches an inspected
safe boundary.

Cancellation produces completed, conflicted, failed, and not-started outcomes.
It does not discard a recovery object or automatically abort an active rebase.

Timeout means only that the waiting deadline expired. The application must
inspect process and Git state before classifying the repository or starting the
next one.

## Verified outcomes

| Outcome | Required observed facts |
| --- | --- |
| `unchanged` | Expected branch contains fetched base OID; original working state remains |
| `succeeded` | Expected branch contains fetched base OID; no active rebase; working state restored; no application recovery ref required |
| `rebase-conflict` | Owned rebase is active; conflict state inspected; recovery ref preserved when needed |
| `restore-conflict` | Rebase complete; index/worktree conflict inspected; recovery ref preserved |
| `aborted` | Pre-Sync HEAD restored; working state restored or recovery remains explicitly reported |
| `blocked` | No branch mutation occurred for the recorded reason |
| `failed-known-state` | Mutation may have occurred, but observed Git state and recovery actions are known |
| `interrupted` | Operation record remains and requires reconciliation |

## Crash reconciliation

| Recorded phase and observed state | Classification | Recovery |
| --- | --- | --- |
| Before preservation; original dirty state present | no mutation | Retry repository |
| Recovery ref exists; worktree clean; HEAD unchanged | preserved | Start recorded rebase |
| Recovery ref exists; owned rebase active | rebase paused/interrupted | Reinspect and continue or abort |
| Recovery ref exists; expected rebased graph observed; no active rebase | rebase succeeded before checkpoint | Start restoration |
| Recovery ref exists; restored inventory verified | restore succeeded before checkpoint | Delete recovery ref and checkpoint |
| No recovery ref was needed; expected rebased graph observed | rebase succeeded | Checkpoint |
| HEAD or branch moved outside recorded transition | external state conflict | Stop and require Repair/manual inspection |
| Recovery ref exists without matching readable operation | invalid state | Preserve ref and require Repair |

## Acceptance test matrix

### Base and graph

- equal, contains-base, behind-base, and diverged relations;
- remote advances before fetch, after fetch, and after confirmation;
- fetch failure in one repository while others succeed;
- configured base override and missing base;
- repeated Sync uses recorded OID before any fresh fetch.

### Working state

- staged only;
- unstaged tracked only;
- mixed staged and unstaged changes to the same path;
- untracked files and directories;
- existing user stash stack before and after Sync;
- ignored-file collision;
- dirty submodule;
- preservation failure before rebase;
- exact staged/unstaged/untracked restoration after rebase.

### Conflicts and batch

- rebase conflict in first, middle, and last repository;
- later repositories run after repository conflict;
- multiple repositories conflict and appear in Action required;
- repeated conflict during rebase continue;
- abort one conflict without rolling back successful repositories;
- restore conflict after successful rebase;
- mixed unchanged, succeeded, blocked, conflicted, failed, and not-started
  outcomes.

### Interruption

- interruption before and after preservation;
- interruption during and after rebase;
- interruption during and after restoration;
- successful Git command before missing checkpoint;
- cancellation between repositories;
- timeout with process running, stopped, conflicted, and successfully completed;
- operation-record write failure stops later repository mutations;
- recovery ref is never deleted before verified restoration;
- remote branches remain unchanged in every case.
