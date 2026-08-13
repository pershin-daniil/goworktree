# New Work workflow

Status: draft 0.1

Parent specification: [../WORKFLOWS.md](../WORKFLOWS.md).

## Goal

Create one new Work containing verified Git worktrees on newly created local
branches, plus the initial `go.work` harness when applicable.

New Work does not import, adopt, or reuse unrelated existing branches,
worktrees, directories, or manifests.

## Inputs

| Input | Required | Meaning |
| --- | --- | --- |
| Work name | yes | Work directory name and local branch name in every selected repository |
| Repository IDs | yes | Non-empty set of configured source repositories |
| Base override | no | Explicit base branch for a selected repository |
| Remote mode | no | `online` by default; explicit `offline` is allowed |
| Open program | no | Program to launch only after verified Work creation |

The TUI collects these inputs. The CLI supplies the same typed request without
opening implicit pickers in non-interactive mode.

## Work-name contract

The same Work name is used as:

- one directory component under the configured works root;
- `refs/heads/<work-name>` in every selected repository.

The name:

- is 1–128 ASCII bytes;
- matches `^[A-Za-z0-9][A-Za-z0-9._-]*$`;
- is not `.` or `..`;
- passes `git check-ref-format --branch` unchanged;
- is not case-equivalent to an existing Work directory on the actual
  filesystem;
- is never trimmed, normalized, case-folded, or suffixed automatically.

An invalid or colliding name returns an error before Work creation. The user
chooses another name.

## Remote and base resolution

### Online mode

Online mode is the default.

1. Inspect every selected source repository.
2. Acquire repository locks in stable canonical-identity order.
3. Fetch the configured remote for every selected repository.
4. Resolve the base ref in the existing order:
   - request override;
   - configured repository preference;
   - remote symbolic HEAD;
   - supported common branch names;
   - repository HEAD fallback.
5. Resolve the selected base ref to an immutable commit OID.
6. Record the remote, ref, OID, and fetch completion time in the plan.
7. Release preparation locks before awaiting user confirmation.

If fetch or base resolution fails for any repository, no Work directory or
work branch is created. Successful fetches in other repositories are harmless
local remote-tracking updates and are reported in the result.

There is no silent fallback to stale local refs.

### Offline mode

Offline mode must be explicitly selected.

- No fetch is attempted.
- Base resolution uses locally available refs in the same preference order.
- The plan labels every base as locally known state and does not claim it is
  current on the remote.
- The resolved commit OID is still immutable input to branch creation.

## Inspected facts

Before a plan is eligible, inspection collects:

### Work scope

- configured works-root canonical identity;
- target Work root path;
- case-sensitive and case-insensitive path collisions;
- existing manifest or operation record for the Work name;
- existing entries at the target root.

### Per repository

- stable configured repository ID;
- canonical source path and Git common-directory identity;
- configured remote and base preference;
- resolved base full ref and commit OID;
- target local full ref `refs/heads/<work-name>`;
- whether that ref already exists and its OID;
- whether that branch is checked out in another worktree;
- destination worktree path;
- filesystem entry and canonical identity at that path;
- Git worktree registration for that path;
- root `go.mod` presence.

Local staged, unstaged, and untracked changes in an existing source checkout do
not block New Work. New Work does not modify that checkout.

## Eligibility matrix

| Observed state | New Work result | Next action |
| --- | --- | --- |
| Work root absent, all refs and destinations absent | eligible | Show plan |
| Valid manifest and operation record identify the same Work | route to Resume | Review resume plan |
| Work root exists without matching Work identity | blocked: `already-exists` | Inspect or remove the conflicting path manually |
| Manifest exists but operation record is missing or inconsistent | blocked: `invalid-state` | Repair Work |
| Target local branch exists but is not owned by this Work operation | blocked: `already-exists` | Choose another Work name |
| Target branch is checked out elsewhere | blocked: `already-exists` | Choose another Work name or remove the other worktree manually |
| Destination is already the exact intended worktree but no matching Work operation owns it | blocked: `state-conflict` | Inspect manually; Import is unsupported |
| Destination is an empty ordinary directory | blocked: `already-exists` | Remove or rename it manually |
| Destination contains files or another repository | blocked: `state-conflict` | Choose another Work name or move the conflicting data |
| Git registration exists but destination is missing | blocked: `state-conflict` | Prune or repair the stale registration, then retry |
| Source repository identity is missing or ambiguous | blocked: `invalid-state` | Fix repository configuration |
| Fetch or base resolution fails | blocked: `external-failure` or `not-found` | Retry online or explicitly select offline mode |
| Two selected IDs resolve to the same Git common directory | blocked: `invalid-input` | Remove the duplicate selection/configuration |

New Work never turns a blocked row into an implicit cleanup or Import action.

## Plan contract

The plan contains:

### Work-level fields

- Work name;
- Work root;
- mode: online or offline;
- selected program, if automatic opening was requested;
- exact manifest and operation-record locations;
- statement that no remote branch will be created, pushed, rewritten, or
  deleted.

### Per-repository fields

- repository ID, source path, and canonical Git identity;
- fetched remote and timestamp, or explicit offline marker;
- base full ref and commit OID;
- new local full ref;
- destination worktree path;
- whether root `go.mod` makes the worktree a `go.work` entry.

The TUI displays the plan and requires the focused `Create Work` action. The CLI
invocation itself authorizes this non-destructive creation unless `--plan` was
requested. Exact CLI flag names are not fixed by this document.

## Required locks and plan validation

Execution requires:

1. one external Work lock keyed by intended Work identity;
2. one repository lock per canonical Git common directory;
3. repository locks acquired in stable identity order.

After locks are acquired, all mutation targets are reinspected.

The confirmed plan remains valid if its exact base OIDs are still available,
even if a remote branch advances after fetch. The plan is invalidated when a
Work path, destination, repository identity, local target ref, or selected base
OID no longer matches.

An invalidated plan returns `state-conflict`; no Work mutation begins.

## Operation phases

```text
preparing-remotes
planning
awaiting-confirmation
acquiring-locks
recording-intent
creating-work-root
creating-repository-worktrees
generating-harness
verifying
created
opening-program
partial
interrupted
failed-known-state
```

## Mutation steps

### 1. Record Work intent

After plan validation:

1. Create the external operation record with a stable operation ID.
2. Record immutable plan inputs and every repository step as pending.
3. Create the Work root.
4. Atomically write the initial manifest.
5. Inspect both files and checkpoint Work-root creation.

No repository mutation begins before recoverable Work intent exists.

### 2. Create each repository worktree

For every selected repository in stable plan order:

1. Record step intent with source identity, base OID, target full ref, and
   destination path.
2. Revalidate branch absence, destination absence, source identity, and base
   object availability.
3. Create the new branch and worktree from the exact base OID.
4. Inspect:
   - destination exists and is a Git worktree;
   - registration points to the destination;
   - Git common-directory identity matches the planned source repository;
   - checked-out full ref equals `refs/heads/<work-name>`;
   - HEAD initially equals the planned base OID.
5. Checkpoint the observed result in the operation record and manifest.

The source checkout's current branch and local changes must remain unchanged.

### 3. Generate `go.work`

After repository worktrees are verified:

1. Include only worktrees whose root contains `go.mod`.
2. Do not scan for nested modules.
3. Use relative paths from the Work root.
4. Write the file atomically.
5. Verify that every generated `use` path resolves to the planned worktree and
   root `go.mod`.

If no selected worktree has a root `go.mod`, no `go.work` is generated and the
result states that explicitly.

The exact `go` directive, entry ordering, and `go.work.sum` ownership remain
defined by the parent document's open decisions.

### 4. Verify Work

Final inspection verifies:

- Work root and manifest identity;
- exact repository membership;
- every worktree identity, registration, branch, and initial HEAD;
- generated harness contents when applicable;
- no unfinished repository creation step.

Only then is New Work successful and its creation operation record cleared or
archived according to the future storage contract.

### 5. Open after creation

If automatic opening was requested, Open Work runs only after successful Work
verification.

An external-program launch failure returns:

- New Work result: successful;
- Open Work result: failed;
- next action: retry Open Work.

It does not make the Work incomplete and does not trigger Resume.

## Partial result policy

Created worktrees, branches, manifest data, and harness data are not
automatically rolled back after failure or cancellation.

New Work stops on the first repository creation failure. It does not start
later pending repositories in the same run. Already verified repositories stay
part of the partial Work and are not repeated by Resume.

The operation record and final inspection identify:

- verified completed repositories;
- pending repositories;
- repository whose step failed or was interrupted;
- any branch, registration, or directory requiring reconciliation;
- whether harness generation remains pending.

The TUI returns to Work detail with `Resume New Work`, `Inspect`, and valid
recovery actions. The CLI returns a partial/interrupted exit status and the same
typed result.

## Resume contract

Resume is allowed only when manifest and operation record identify the original
New Work operation.

Resume:

1. Acquires the same Work and repository lock set.
2. Reinspects every recorded step.
3. Accepts already verified postconditions without repeating Git commands.
4. Reconciles an uncheckpointed command from observed state.
5. Continues only pending or safely recoverable steps.
6. Uses the original branch names, paths, repository identities, and base OIDs.
7. Regenerates and verifies `go.work` after all repository steps succeed.

Changing Work composition, name, or bases during Resume is not allowed. Finish
the original Resume, Repair it, or Remove Work before submitting a different
New Work request.

## Crash reconciliation

| Observed state after an uncheckpointed repository step | Classification | Resume action |
| --- | --- | --- |
| Branch absent, destination absent, no registration | command did not take effect | Run the original create step |
| Planned branch exists at base OID, destination absent, no valid registration | branch created by recorded operation; worktree incomplete | Attach the recorded branch to the planned destination, then verify |
| Exact planned worktree, registration, identity, branch, and initial OID exist | command succeeded | Checkpoint without rerunning Git |
| Destination exists but identity or branch differs | ambiguous conflict | Stop and require Repair/manual inspection |
| Registration exists but directory is missing | incomplete Git state | Offer prune/repair, then resume |
| Planned branch moved to another OID before verification | external state change | Stop with `state-conflict` |
| Work root or manifest identity changed | unsafe external state change | Stop with `invalid-state` |

Recovery never assumes success from process exit text alone.

## Expected errors

| Error | Mutation allowed before detection? | Recovery |
| --- | --- | --- |
| `invalid-input` | no | Correct inputs |
| `already-exists` | no | Choose another Work name or resolve the collision manually |
| `not-found` | no | Correct repository, remote, or base configuration |
| `unsafe-path` | no | Correct works root or Work name |
| `locked` | no | Wait, inspect lock owner, then retry |
| `external-failure` during preparation | remote-tracking refs may have updated; no Work mutation | Retry or explicitly use offline mode |
| `external-failure` during creation | yes, for recorded completed steps | Inspect and Resume |
| `state-conflict` | possibly, only when detected after an interrupted step | Inspect, Repair, or resolve external state |
| `timeout` | unknown until inspection | Reinspect and classify operation state |
| `interrupted` | yes | Resume from operation record |
| `internal` | unknown until inspection | Preserve diagnostics; restart into Inspect/Repair |

## Acceptance test matrix

### Input and preflight

- valid online creation from fetched remote base;
- explicit offline creation from a locally known base;
- online fetch failure with no Work mutation;
- invalid character, whitespace, path separator, invalid Git ref, and excessive
  Work name length;
- duplicate repository ID and duplicate canonical repository identity;
- missing source repository, remote, configured base, and fallback base;
- case-only Work-directory collision on a case-insensitive filesystem;
- branch exists and is free;
- branch exists and is checked out in another worktree;
- destination is empty, non-empty, foreign repository, exact unrelated
  worktree, or stale registration.

### Successful mutation

- one repository;
- multiple repositories with different base refs;
- source checkout dirty before and after creation with identical status;
- worktree branch and initial HEAD verified against planned OID;
- root `go.mod` included in `go.work`;
- nested `go.mod` ignored;
- no root modules produces no `go.work`;
- successful Work plus failed external-program launch.

### Failure injection and Resume

- interruption before Work-root creation;
- interruption after Work root but before manifest checkpoint;
- interruption before and after every repository Git command;
- branch created without completed worktree;
- worktree created before checkpoint;
- repository identity, branch OID, destination, or registration changed between
  plan and execution;
- harness write failure and Resume;
- repeated Resume after verified success performs no additional Git mutation;
- remote base advances after confirmed plan; creation still uses confirmed OID;
- remote branches remain unchanged in every case.
