# Source repository management

Status: implemented MVP 0.2

Parent specification: [WORKFLOWS.md](WORKFLOWS.md).

This document describes the implemented MVP behavior and its safety
boundaries.

## Context

`goworktree` currently uses configured source repositories to create and sync
Works. Online New Work and Sync Work fetch remote refs, but neither workflow is
responsible for maintaining the checkout or local default branch in the source
repository itself.

As a result:

- all configured repositories appear in one catalog without user-defined
  grouping;
- selecting repositories becomes harder when the catalog serves several
  teams or areas;
- a source repository's local default branch may remain behind its fetched
  remote-tracking branch for a long time;
- users must leave `goworktree` to inspect or update source repositories as a
  batch.

## Goals

1. Keep one catalog containing all configured source repositories.
2. Let users assign repositories to groups and filter the catalog by group.
3. Provide complete interactive TUI flows for inspection, grouping, fetch, and
   safe default-branch updates.
4. Provide equivalent deterministic CLI workflows.
5. Make batch behavior explainable and preserve a result for every repository.
6. Never discard local changes, rewrite divergent branches, or change remote
   branches.

## Non-goals

- Groups do not own repositories or define filesystem placement.
- Groups do not change repository IDs or move repository directories.
- Groups do not dynamically determine the contents of an existing Work.
- Updating a source repository does not rebase or otherwise modify a Work.
- The workflow does not pull with merge, force-reset, clean files, push, or
  delete refs.
- The first version does not continuously poll remotes in the background.
- Cloning and relocating source repositories are not part of this version.

## Product model

### Repository catalog

There is one canonical catalog containing every configured source repository.
The repository remains the primary item in both the TUI and CLI. Groups are
metadata used to filter and select catalog items; they are not a parent level
in a repository tree.

`repos_root` is the discovery and onboarding boundary: scanning and manual
path configuration accept repositories only under that directory. It is not
a catalog filter. Existing configured repositories remain visible and usable
when their paths are outside the current `repos_root`; an invalid or missing
path is reported on that repository without preventing the rest of the
catalog from loading.

### Groups

A group is a user-defined label such as `team-a`, `team-b`, or `platform`.

- A repository may belong to zero, one, or several groups.
- `All` is a virtual filter containing every configured repository.
- `Ungrouped` is a virtual filter containing repositories with no groups.
- A group is visible only while at least one repository uses it.
- Group membership is deduplicated and stored in stable lexical order.
- Renaming a group updates its label on every affected repository in one
  atomic configuration write.
- Deleting a group removes only the label. It never deletes repositories,
  directories, branches, or Works.

The proposed initial persistence adds an optional `groups` array to each
repository entry:

```json
{
  "repos": {
    "api": {
      "path": "/Users/you/Projects/api",
      "default_branch": "main",
      "groups": ["team-a", "platform"]
    },
    "web": {
      "path": "/Users/you/Projects/web",
      "default_branch": "main",
      "groups": ["team-a"]
    }
  }
}
```

This is backward-compatible with existing configuration: a missing `groups`
field means that the repository is ungrouped. A separate top-level group
registry is unnecessary until groups need their own settings or metadata.

Initial group identifiers should use the same conservative form as other
configuration identifiers: ASCII letters, digits, `.`, `_`, and `-`, beginning
with a letter or digit. Names are case-sensitive and stored without automatic
normalization.

## TUI information architecture

The dashboard has two top-level modes:

```text
[ Works ]  [ Repositories ]
```

`Works` preserves the current Work-oriented dashboard. `Repositories` shows
one list containing all configured source repositories. It does not show a
separate list or section for each group.

Example repository catalog:

```text
[ Works ]  [ Repositories ]

Repositories                         8 of 24
Group: team-a                         2 selected

  [x] api          main     known behind 3  team-a, platform
  [ ] web          main     known current   team-a
  [x] worker       develop  diverged     team-a
  [ ] legacy       master   remote ?     team-a

Tab switch  j/k move  Space select  / search  f group  : actions
```

The group filter opens a picker rather than navigating to a group detail
screen:

```text
Filter by group

  All
  team-a
  team-b
  platform
  Ungrouped
```

Text search and the group filter compose: search applies only to repositories
allowed by the active group filter. The header always shows the active filter,
visible count, total catalog count, and selection count.

Selection may span groups. Changing the filter does not silently clear hidden
selected repositories. When hidden selections exist, the header and every
mutation plan state their count explicitly, for example `5 selected, 2 hidden
by filter`.

### Repository catalog keys

Outside text input:

| Key | Action |
| --- | --- |
| `Tab` / `Shift+Tab` | Switch between Works and Repositories |
| `j` / `k` | Move down / up |
| `l` / `Enter` | Open repository detail |
| `Space` | Toggle repository selection |
| `a` | Select all visible repositories, or clear them when all are selected |
| `/` | Search the current filtered catalog |
| `f` | Select a group filter |
| `:` | Open actions for selected repositories, or the focused repository when nothing is selected |
| `r` | Reinspect local state without network access |
| `?` | Show context help |

Repository detail shows configured identity, path, groups, remote, default
branch, checked-out branch, working-tree facts, active Git operations, local
default OID, and locally known remote default OID. It must distinguish locally
known remote state from state fetched during the current operation.

### Repository actions

The scoped action palette contains:

- **Fetch** — fetch the configured remote and report the new locally known
  remote state; do not update the local default branch.
- **Update default branch** — fetch, build an exact plan, and fast-forward only
  eligible local default branches.
- **Edit groups** — add or remove group labels for the current selection.
- **Open repository** — open one focused source repository with a configured
  program.
- **Refresh** — reinspect local state without fetch.
- **Scan repositories** — rescan the configured root, add newly discovered
  primary clones, and preserve group metadata while migrating legacy IDs.
- **Manage groups** — add or remove labels for the current scope. Rename and
  catalog-wide deletion are also available through the CLI in the MVP.

The action scope is always explicit. If repositories are selected, actions
apply to the complete selection, including selections hidden by the current
filter. If nothing is selected, actions apply only to the focused repository.
An update of every repository requires an explicit `Select all` or `Update all`
action and must never be inferred merely from the `All` filter.

The New Work repository picker should reuse the same group filter and text
search behavior. The resulting New Work request still contains an exact list
of repository IDs. Later group membership changes do not alter that Work.

## Repository state and remote freshness

Dashboard refresh is local-only. Before a fetch, the UI may compare refs that
already exist locally but must label the result as locally known state. It must
not imply that the remote server currently has the same OID.

Suggested labels:

| Label | Meaning |
| --- | --- |
| `remote ?` | Remote/default ref is absent or could not be inspected locally |
| `known current` | Local default equals the locally known remote ref |
| `known behind 3` | Local default is behind the locally known remote ref |
| `fetched current` | Equality was established after a successful fetch in the current operation |
| `fetched behind 3` | Behind count was established after a successful fetch in the current operation |
| `ahead 2` | Local default has commits not in the fetched remote ref |
| `diverged` | Both refs contain unique commits |

A persisted last-successful-fetch timestamp may be shown as history. It does
not turn old remote-tracking refs into authoritative remote state.

## Inspect Source Repositories

Inspection is read-only and does not fetch.

Per repository it collects at least:

- configured ID, display name, path, groups, remote, and default preference;
- canonical source path and Git common-directory identity;
- source checkout HEAD ref and OID, including detached state;
- all worktree registrations relevant to the local default branch;
- staged, unstaged, untracked, and conflicted files in a checkout that may be
  updated;
- active Git operations;
- local default full ref and OID;
- locally known remote default full ref and OID;
- ahead, behind, equal, or diverged relationship when both OIDs are known.

`unknown` remains a first-class result. One repository inspection failure does
not hide independently inspected repositories.

## Fetch Source Repositories

Fetch accepts an exact non-empty repository-ID set and uses the configured
remote for each repository.

1. Resolve and lock repositories in stable canonical-identity order.
2. Reinspect identity and configured remote.
3. Run fetch independently for every eligible repository.
4. Resolve the fetched remote default ref to an immutable OID.
5. Record and display one result per repository.

Fetch does not modify the source checkout, local default branch, Work branches,
or remote branches. A failure in one repository does not stop independent
repositories. Independent fetch and post-fetch inspection steps run with a
bounded concurrency of four repositories; result order remains stable by
repository ID.

## Update Source Default Branches

Update is an online-only batch workflow for an exact repository-ID set. It
first fetches each configured remote, then builds a plan against immutable
fetched target OIDs.

### Safe update rules

The only allowed branch transition is:

```text
refs/heads/<configured-default>: old OID → fetched descendant OID
```

The transition uses compare-and-swap semantics: execution proceeds only if the
local ref still equals the planned old OID and the fetched target is still
available. No merge commit, rebase, force update, reset, checkout switch, push,
or remote-branch mutation is allowed.

| Observed state after fetch | Planned result |
| --- | --- |
| Local default equals fetched target | `unchanged` |
| Local default is behind and checked out in the source checkout, which is clean and has no active operation | fast-forward the checked-out branch |
| Local default is behind and is not checked out in any worktree | fast-forward the ref directly with old-OID verification |
| Local default is checked out in another worktree | `skipped`: show the exact worktree path |
| Checkout to update has staged, unstaged, untracked, or conflicted files | `skipped`: local changes |
| Repository has an active Git operation | `skipped`: active operation |
| Local default is ahead of fetched target | `unchanged-ahead`: do not push or rewrite |
| Local default and fetched target diverged | `skipped-diverged` |
| Local default ref is missing | `skipped-missing-local-default` |
| Remote/default ref is missing or ambiguous | `failed` with a precise next action |
| Repository identity, old OID, or target OID changed after planning | `state-conflict`; invalidate that repository plan |

Updating a non-checked-out default ref directly is proposed because it keeps
the branch ready for its next checkout without changing the user's current
checkout. This behavior remains an explicit product decision to confirm before
implementation.

### Plan and confirmation

The TUI plan shows, per repository:

- source path and canonical repository identity;
- configured remote and default branch;
- fetched full ref and immutable target OID;
- local default full ref and current OID;
- relation and exact planned action;
- worktree path whose files would change, if any;
- skip or block reason;
- statement that no remote branch or Work will be modified.

The plan summary separates `update`, `unchanged`, `skipped`, and `failed`
counts. A focused confirmation such as `Update 6 repositories` authorizes only
the exact eligible entries in the displayed plan. Fetches performed while
building the plan are reported even when the user does not confirm branch
updates.

### Execution and recovery

- Acquire repository locks in stable canonical-identity order.
- Reinspect every mutation target after locks are held.
- Execute and verify repositories independently.
- Record the intended old and target OIDs before each branch mutation.
- Verify the resulting ref and, for a checked-out branch, the checkout HEAD and
  working-tree state.
- Preserve one result for every requested repository.
- Continue independent repositories after a known failure.

The MVP revalidates exact old and target OIDs for every mutation and reconciles
an uncertain command result by reading the resulting ref. Re-running Update
rebuilds a plan from observed refs, so completed fast-forwards become
`unchanged`. Durable cross-process operation history and checkpoints remain a
follow-up; the workflow never repeats a branch update without old-OID
verification.

## CLI surface

Exact flags remain open, but the CLI must expose the same typed workflows
without implicit pickers. Proposed commands:

```text
goworktree repos list [--group <group>]
goworktree repos status [--repos <ids> | --group <group> | --all]
goworktree repos fetch  [--repos <ids> | --group <group> | --all]
goworktree repos update [--repos <ids> | --group <group> | --all]
goworktree repos open <id> [--program <id>]

goworktree repos group add <group> --repos <ids>
goworktree repos group remove <group> --repos <ids>
goworktree repos group rename <old> <new>
goworktree repos group delete <group>
```

Mutating or networked batch commands require one explicit scope. Omitting
`--repos`, `--group`, and `--all` is an input error rather than an implicit
operation over the entire catalog.

`start --group <group>` may be added as selection shorthand, but the resolved
request and Work manifest must store exact repository IDs. `--group` and
`--repos` combination semantics should be decided before exposing this flag.

## Interaction with existing workflows

- **New Work:** online planning already fetches selected repositories and may
  continue using immutable remote base OIDs. It does not need the source local
  default branch to be current.
- **Sync Work:** continues to fetch and rebase Work branches. It does not update
  source local default branches.
- **Inspect Works:** remains local-only and separate from source repository
  inspection.
- **Remove Work:** never removes source repositories or group membership.
- **Repository scan:** retains group membership for repositories whose stable
  configured identity is preserved. Missing repositories are reported rather
  than silently deleting their configuration and labels.

Source Repository Update and Sync Work intentionally remain separate actions:
the first maintains local default branches in source repositories; the second
rebases Work branches onto fetched bases.

## Safety invariants

1. Source update never runs `reset --hard`, `clean`, force checkout, or a
   force-ref update.
2. Staged, unstaged, untracked, and conflicted files are never discarded.
3. Divergent local default branches are never rewritten automatically.
4. Remote branches are never mutation targets.
5. Existing Work branches and Work worktrees are never mutation targets.
6. Every local ref update verifies the exact old OID immediately before the
   mutation.
7. A branch checked out in an unexpected worktree is not updated.
8. A failed or unknown inspection result never becomes an eligible update.
9. Filtering controls visibility and selection only; it never authorizes a
   mutation by itself.
10. Batch success is not reported when any requested repository is skipped,
    failed, interrupted, or left in an unknown state.

## Implemented decisions and follow-ups

1. **Multiple groups:** implemented; one repository may belong to several
   groups.
2. **Persistence:** implemented on repository entries without a top-level group
   registry.
3. **Dashboard navigation:** implemented as Works and Repositories modes, with
   groups only as a filter inside the repository catalog.
4. **Unchecked-out defaults:** implemented with compare-and-swap when the
   branch is not checked out anywhere.
5. **Batch confirmation:** implemented in the TUI; explicit CLI invocation
   authorizes the displayed plan.
6. **New Work selection:** group filtering is implemented in the TUI;
   `start --group` remains a follow-up.
7. **Operation history:** durable update records and last-successful-fetch
   timestamps remain a follow-up.
