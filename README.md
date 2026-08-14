# goworktree

Personal CLI for managing one **Work** as a group of Git worktrees across
multiple repositories.

Running `goworktree` with no arguments opens the Works dashboard. Its default
inspection reconciles manifests, operation records, filesystem state, local
branches, worktree registrations, working changes, and active Git operations
without mutation or fetch. `n` starts the typed New Work flow; `:` opens actions
for the current context. New Work fetches only while building an online plan and
does not create branches or worktrees until that exact plan is confirmed.

The dashboard can create/resume New Work, open a Work in configured programs,
and run typed Sync Work, Repair Work, or Remove Work. The dashboard and explicit
commands call the same workflow cores.

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
goworktree list
goworktree sync EVOVPC-2855     # plan fetched base OIDs, then rebase Work branches
goworktree archives list         # list metadata retained after Remove Work
goworktree cursor EVOVPC-2855   # open a project in Cursor
```

With [Task](https://taskfile.dev):

```bash
task                  # tidy → fmt → lint → test
task init
task start -- my-ticket
task list
task doctor
```

## How it works

1. **Config** — `~/.config/goworktree/config.json`  
   `repos_root`, `projects_root`, `default_branch`, `scan_depth`, editor paths, and known repos.
2. **Repo IDs** — relative path under `repos_root` (`cloud/vpc/foo` → `cloud-vpc-foo`). Optional `alias` for UI / folder names.
3. **`start <name>` / dashboard New Work** — both use the same typed planner/executor. The online plan fetches configured remotes, resolves immutable base OIDs, then creates branch `<name>` and its worktrees only after the plan succeeds. `--offline` resolves only locally known refs.
4. **Persistence** — `.goworktree.json` stores Work intent; the external New Work operation record stores durable checkpoints. Re-run `start` to resume only verified pending steps.
5. **`add` / `drop`** — change the repo set mid-task. Legacy folders without a manifest are auto-migrated. `drop -D` deletes only **local** project branches.
6. **`remove`** — require a completed New Work record, show exact local deletion targets, and require the case-sensitive Work name. It fingerprints each complete worktree and every remaining Work-root entry, rejects any post-plan change, records intent before each local mutation, removes managed worktrees, compare-and-deletes confirmed local branch OIDs, and removes the confirmed Work root. On success it archives operation metadata under `~/.config/goworktree/archives/removed-work/`; the dashboard no longer shows the Work and the name can be reused. Remote and remote-tracking refs are never deletion targets.
7. **`sync`** — inspect the Work, fetch each configured remote, show the exact resolved base commit, record and revalidate the complete worktree fingerprint, and rebase each eligible Work branch onto the immutable OID. Dirty files are preserved in an application-owned private recovery ref, rather than a user stash entry. The ref is deleted only after a successful restore has an exact durable fingerprint; ambiguous Resume state retains it and refuses destructive cleanup. One repository failure does not stop independent repositories; dirty submodules and unrelated active Git operations are blocked.
8. **`branch`** — adopt the branch currently checked out in one worktree when a repository needs a project-specific branch name.
9. **`repair`** — plans only deterministic repairs: reattach a missing checkout from its exact existing local branch, repair a proven checkout registration, regenerate a missing `go.work`, or reconstruct a missing completed New Work record from a valid manifest. Ambiguous branch, OID, repository-identity, or corrupt-manifest states are reported without mutation.

## Commands

| Command | Description |
|---------|-------------|
| *(no args)* | Inspect Works and run New, Sync, or Open Work actions |
| `init` | First-time setup + repo scan |
| `start <name> --repos id,id [--offline] [--open]` | Same typed New Work core as the dashboard |
| `add <project> --repos id,id` | Add repos to a project |
| `drop <project> --repos id,id [-D] [--yes]` | Drop repos (`-D` = delete local branches) |
| `sync <work>` | Use the same typed Sync Work planner/executor as the dashboard |
| `branch <project> <repo>` | Adopt a repository worktree's current branch in the project manifest |
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
| `repos list` / `scan` / `set …` | Manage scanned repos |
| `version` / `help` | Meta |

## Keys (Works dashboard)

| Key | Action |
|-----|--------|
| `j` / `k` | down / up |
| `h` | back |
| `l` / `Enter` | open |
| `g` / `G` | top / bottom |
| `Ctrl+u` / `Ctrl+d` | page up / down |
| `/` | filter |
| `r` | reinspect local state |
| `n` | New Work from the Works home |
| `:` | scoped action palette |
| `Space` | toggle repository selection during New Work |
| `m` | switch New Work between online/offline planning |
| `Esc` | back |
| `q` | quit |
| `Ctrl+C` | request operation cancellation; quit outside operations |

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
      "alias": "vpc-controller"
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

GitHub Actions runs the same formatting, lint, and test gates on pushes and
pull requests.

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
- `drop` records removal progress per repository. If it stops midway, re-run it with the remaining repository IDs or use `repair`.
- Remove Work archives metadata only; they do not retain deleted worktree files, commits, or uncommitted changes.
- One git branch → one worktree (project branch is created with `-b` from the base).
- Editor subprocess stdout/stderr is discarded so Node/IDE noise does not corrupt the TUI.
- No shell completions yet; no multi-panel dashboard.

## License

Personal / use as you like. Not an official product.
