# goworktree

Personal CLI for managing one **Work** as a group of Git worktrees across
multiple repositories.

Running `goworktree` with no arguments opens the dashboard. `Tab` switches
between Works and the source repository catalog. Default inspection is local
and does not fetch. `n` starts the typed New Work flow; `:` opens actions for
the current context. New Work fetches only while building an online plan and
does not create branches or worktrees until that exact plan is confirmed.

The dashboard can create/resume New Work, manage source repository groups,
open Works or source repositories in configured programs, safely fetch or
fast-forward source default branches, add or remove repositories in the
selected Work, and run typed Sync Work, Repair Work, or Remove Work. The
dashboard and explicit commands call the same workflow cores.

Repo: [github.com/pershin-daniil/goworktree](https://github.com/pershin-daniil/goworktree)

## Install

```bash
go install github.com/pershin-daniil/goworktree@latest
```

Or from a clone:

```bash
task install   # or: go install .
```

Requires: **Go 1.23+**, **git**, and optionally any program that can open a folder.

## Quickstart

```bash
goworktree init                 # TUI: paths + scan repos_root
goworktree                      # Works dashboard, New Work, and scoped actions
goworktree start EVOVPC-2855 --repos api,web # create / resume typed New Work
goworktree add EVOVPC-2855 --repos worker     # extend the active context
goworktree drop EVOVPC-2855 --repos web --yes # remove a clean worktree, keep its branch
goworktree list
goworktree sync EVOVPC-2855     # plan fetched base OIDs, then rebase Work branches
goworktree repos status --group team-a
goworktree repos update --group team-a # fetch + safe default-branch fast-forward
goworktree repos open api       # open the primary source clone
goworktree archives list         # list metadata retained after Remove Work
goworktree cursor EVOVPC-2855   # open a project in Cursor
```

With [Task](https://taskfile.dev):

```bash
task                  # tidy → fmt → lint → test
task init
task start -- my-ticket --repos api,web
task list
task doctor
```

## How it works

1. **Config** — `~/.config/goworktree/config.json`  
   `repos_root`, `projects_root`, `default_branch`, `scan_depth`, editor paths, and known repos.
2. **Repo IDs** — relative path under `repos_root` (`cloud/vpc/foo` → `cloud-vpc-foo`). Optional `alias` for UI / folder names.
3. **Repository groups** — labels stored on repository entries. One repository may belong to several groups. The dashboard and New Work picker always show one catalog filtered by `All`, a group, or `Ungrouped`.
4. **Source update** — fetches configured remotes, then fast-forwards only eligible local default branches to exact fetched OIDs. Dirty, divergent, ambiguous, or unexpectedly checked-out branches are skipped; remote branches and Works are untouched.
5. **`start <name>` / dashboard New Work** — both use the same typed planner/executor. The online plan fetches configured remotes, resolves immutable base OIDs, then creates branch `<name>` and its worktrees only after the plan succeeds. `--offline` resolves only locally known refs.
6. **Persistence** — `.goworktree.json` schema 2 stores active and retained inactive repository intent. External New Work and Change Work records store durable checkpoints; interrupted repository changes are reconciled before continuing.
7. **`add` / `drop`** — change the repository set mid-task through the same typed workflow in CLI and dashboard. Add is online by default and supports `--offline`. Drop requires clean worktrees, keeps local Work branches by default, and `-D` explicitly deletes only exact confirmed **local** branches. A retained branch can be attached again later. A CLI retry must match the original repository set, online/offline mode, and branch-deletion flag; otherwise it stops without continuing the old operation. The dashboard also offers Resume for the recorded operation.
8. **`remove`** — require a completed New Work record, show exact local deletion targets, and require the case-sensitive Work name. The plan includes branches retained by earlier `drop` operations, listed separately without a worktree deletion. Retained branches must still have their recorded OID and must not be checked out elsewhere; already-missing refs are accepted. It fingerprints each complete worktree and every remaining Work-root entry, records intent before each local mutation, compare-and-deletes confirmed local branch OIDs, and removes the confirmed Work root. On success it archives operation metadata, including Change Work, under `~/.config/goworktree/archives/removed-work/` and clears active records so the name can be reused. Unrelated existing local branches may still prevent creation. Remote and remote-tracking refs are never deletion targets.
9. **`sync`** — inspect the Work, fetch each configured remote, show the exact resolved base commit, record and revalidate the complete worktree fingerprint, and rebase each eligible Work branch onto the immutable OID. Dirty files are preserved in an application-owned private recovery ref, rather than a user stash entry. The ref is deleted only after a successful restore has an exact durable fingerprint; ambiguous Resume state retains it and refuses destructive cleanup. One repository failure does not stop independent repositories; dirty submodules and unrelated active Git operations are blocked.
10. **`branch`** — adopt the local branch currently checked out in one worktree when a repository needs a project-specific branch name. Typed Works record this through Change Work with identity/OID checks and resumable manifest publication. Files and Git refs are unchanged; Sync, Drop, and Remove subsequently use the adopted branch. The previous branch is left untouched. Detached HEAD and active Git operations are refused. Legacy manifests remain supported by the compatibility adapter.
11. **`repair`** — plans only deterministic repairs: reattach a missing checkout from its exact existing local branch, repair a proven checkout registration, regenerate a missing `go.work`, or reconstruct a missing completed New Work record from a valid manifest. Ambiguous branch, OID, repository-identity, or corrupt-manifest states are reported without mutation.

## Commands

| Command | Description |
|---------|-------------|
| *(no args)* | Dashboard for Works and source repositories |
| `init` | First-time setup + repo scan |
| `start <name> --repos id,id [--offline] [--open]` | Same typed New Work core as the dashboard |
| `add <work> --repos id,id [--offline]` | Add repositories using fetched or explicitly local base OIDs |
| `drop <work> --repos id,id [-D] --yes` | Remove clean worktrees; `-D` also deletes exact local branches |
| `sync <work>` | Use the same typed Sync Work planner/executor as the dashboard |
| `branch <work> <repo>` | Record a worktree's current local branch through resumable Change Work |
| `list` / `ls` | List project groups |
| `remove` / `rm` `<work> --confirm <exact-work-name>` | Typed Remove Work; delete confirmed worktrees, local refs, and root |
| `archives list` | List retained metadata from completed Remove Work operations |
| `archives show <archive-id>` | Verify and display a removed-Work metadata archive |
| `archives delete <archive-id> --confirm <archive-id>` | Delete one verified metadata archive |
| `cursor` / `goland` / `open` | Open project in a compatibility editor / the default program |
| `programs list/add/update/delete/default/search` | Manage programs shown under Open with |
| `doctor` | Check git, editors, config |
| `repair <work>` | Apply only deterministic, revalidated Repair Work actions |
| `config show` / `edit` / `set <key> <value>` | Configuration |
| `repos list [--group GROUP]` | List the repository catalog, optionally filtered by group |
| `repos status [--repos …\|--group …\|--all]` | Inspect locally known source state without fetch |
| `repos fetch (--repos …\|--group …\|--all)` | Fetch configured remotes without changing local defaults |
| `repos update (--repos …\|--group …\|--all)` | Fetch and safely fast-forward eligible local default branches |
| `repos open <id> [--program ID]` | Open a primary source clone in a configured program |
| `repos group list/add/remove/rename/delete` | Manage repository group labels |
| `repos scan` / `set …` | Discover or configure repositories under `repos_root` |
| `version` / `help` | Meta |

Networked repository commands require an explicit scope. Examples:

```bash
goworktree repos group add team-a --repos api,web
goworktree repos fetch --group team-a
goworktree repos update --repos api,web
goworktree repos update --all
```

## Keys (dashboard)

| Key | Action |
|-----|--------|
| `Tab` / `Shift+Tab` | switch Works / Repositories |
| `j` / `k` | down / up |
| `h` | back |
| `l` / `Enter` | open |
| `g` / `G` | top / bottom |
| `Ctrl+u` / `Ctrl+d` | page up / down |
| `/` | filter |
| `?` | contextual dashboard help |
| `f` | filter source repositories or New Work choices by group |
| `r` | reinspect local state |
| `n` | New Work from the Works home |
| `:` | scoped action palette |
| `Space` | toggle repository selection in Repositories, New Work, or repository changes |
| `m` | switch New Work / Add repositories between online and offline planning |
| `d` | keep or explicitly delete local branches in Remove repositories |
| `Esc` | back |
| `q` | quit |
| `Ctrl+C` | request operation cancellation; cancel source inspection; quit elsewhere |

The minimum supported terminal size is `80×24`. `unknown` is displayed when a
fact could not be inspected; it is never collapsed into a healthy default.

## Config sketch

```json
{
  "open_with": {
    "cursor": {"name": "Cursor", "path": "cursor", "enabled": true},
    "goland": {"name": "GoLand", "path": "goland", "enabled": true}
  },
  "default_program": "cursor",
  "conflict_program": "goland",
  "repos_root": "/Users/you/Projects",
  "projects_root": "/Users/you/Projects/worktrees",
  "default_branch": "main",
  "scan_depth": 3,
  "command_timeout_seconds": 300,
  "repos": {
    "cloud-vpc-controller": {
      "path": "/Users/you/Projects/cloud/vpc/controller",
      "default_branch": "develop",
      "alias": "vpc-controller",
      "groups": ["team-a", "platform"]
    }
  }
}
```

Use `goworktree programs` to manage the **Open with** registry:

```bash
goworktree programs list
goworktree programs search code
goworktree programs add vscode --name "VS Code" --path code
goworktree programs update vscode --enabled=false
goworktree programs default vscode
goworktree programs delete vscode
```

`search` lists executable files from `PATH`; `add` and `update` require a runnable executable path or a command resolvable from `PATH`. Programs receive the project folder as their final argument. To open Codex Desktop, use its CLI launcher: `goworktree programs update codex-id-1 --path codex --args app`. A macOS application can also be launched by exact bundle path, for example: `--path open --args "-a /Applications/Codex.app"`.

`conflict_program` selects the program opened automatically at the exact
repository when Sync leaves a rebase conflict. When omitted, it follows
`default_program`. Configure it with `goworktree config set conflict_program
goland`, `goworktree config set conflict_program codex-id-1`, or use `default`
to follow the default program again. The same setting is available as
`Conflict resolver` in the dashboard `:` action palette.

## Development

Install the Task version pinned in `mise.toml`:

```bash
mise install
```

The commands below assume mise is activated in your shell. Otherwise, run
them through `mise exec --`, for example `mise exec -- task`.

Install pinned development tools into the repository-local `.tools/bin`:

```bash
task tools:install
```

`task lint` and `task check` use `.tools/bin/golangci-lint` exclusively. A
global installation from `PATH` is ignored.

The default command is the canonical development pipeline:

```bash
task
```

It runs `tidy → fmt → lint → test` in that order. `tidy` and `fmt` may update
tracked files; review their changes before committing.

Run the equivalent non-mutating formatting, lint, and test gate with:

```bash
task check
```

Individual development commands:

```bash
task build
task test
task vet
task lint
task fmt:check
task fmt
./goworktree doctor
```

Module path: `github.com/pershin-daniil/goworktree`.

## Notes / MVP limits

- Project names and manifest folder names must be single directory components; this prevents operations from escaping `projects_root`.
- Mutating commands acquire a per-project lock. If a process exits unexpectedly, inspect the named `.goworktree.lock` before removing it.
- Interrupted `add` / `drop` retries require the original full repository selection and flags, including `-D` when used. `branch` can also resume its recorded adoption. Use the dashboard's Resume action to continue the recorded operation directly.
- Remove Work archives metadata only; they do not retain deleted worktree files, commits, or uncommitted changes.
- One git branch → one worktree (project branch is created with `-b` from the base).
- Editor subprocess stdout/stderr is discarded so Node/IDE noise does not corrupt the TUI.
- No shell completions yet; no multi-panel dashboard.

## License

Personal / use as you like. Not an official product.
