# Change Work workflow

Status: implemented 0.1

Parent specification: [../WORKFLOWS.md](../WORKFLOWS.md).

## Goal

Change the active repository set or recorded branch of an existing valid Work
without recreating the Work or silently discarding local state. Dashboard and CLI use the same
typed planner, executor, and external operation record.

## Add repositories

- Accept one or more configured repositories not already active by ID or Git
  common-directory identity.
- Use online planning by default: fetch the configured remote and resolve an
  immutable base OID. Offline mode must be explicit.
- Reject an existing same-named branch unless the Work manifest records that it
  was retained by an earlier Remove repositories operation.
- Reattach a proven retained branch without resetting or rebasing it; otherwise
  create the Work branch and worktree at the confirmed base OID.
- Detect a root `go.mod` at the selected commit and update the owned `go.work`.

## Remove repositories

- Require at least one repository to remain active.
- Require the selected checkout to be clean, free of active Git operations,
  and to match the manifest identity, branch, registration, OID, and confirmed
  worktree fingerprint.
- Remove only the selected worktree by default and retain its local Work
  branch. Local branch deletion is a separate explicit plan effect and uses a
  compare-and-delete against the confirmed full ref and OID.
- Never delete or update remote or remote-tracking branches.
- Retain inactive repository ownership intent and the exact preserved branch
  OID so a branch can be attached again only while that ownership proof still
  matches Git.

## Persistence and Resume

Manifest schema 2 adds a monotonically increasing revision, the latest Change
Work operation ID, and inactive repository intent. Schema 1 remains readable
and migrates atomically on the first successful composition change or branch adoption.

The latest operation is stored at
`<control-root>/operations/change-work/<work-id>.json`. It contains immutable
before/after manifests, exact Git facts, `go.work` precondition bytes, and
per-repository checkpoints. Intent is saved before each Git mutation. The
manifest is published only after repositories and `go.work` match the plan.

An interrupted operation is reconciled from observed Git, filesystem, harness,
and manifest state. It must be resumed before Sync, Repair, Remove Work, or a
new repository-set change can begin.

Explicit CLI retries match the entire recorded request under the operation's
locks: Work name, operation kind, full repository ID set, online/offline mode
for Add, and branch-deletion policy for Drop. A different selection or omitted
`-D` refuses to continue. Repeat the original command or use the dashboard's
Resume action for the recorded operation. Do not retry with only the remaining
repository IDs.

## Adopt a branch

`branch <work> <repo>` records the local branch currently checked out in the
selected repository. Its `adopt-branch` Change Work record preserves the
before/after manifest, exact branch OID, source identity, and checkout path.
The executor revalidates the checkout and its unique Git registration under
the same Work/repository locks before publishing the new manifest revision.
Detached HEAD and active Git operations are rejected. Dirty files are allowed
and are not changed; neither are Git refs or the previous branch.

The adopted branch becomes the target of later Sync, Drop, reattachment, and
Remove operations. Retrying `branch` resumes the recorded adoption and rejects
a checkout that no longer matches that plan.

Remove Work includes branches retained by earlier Drop operations in its exact
local deletion plan. It archives the latest Change Work record before clearing
active state, so a recreated Work can start a new sequence of changes.
