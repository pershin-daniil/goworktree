# goworktree

Personal CLI for managing one **Work** as a group of Git worktrees across
multiple repositories.

Running `goworktree` with no arguments opens the Works dashboard. Its default
inspection reconciles manifests, operation records, filesystem state, local
branches, worktree registrations, working changes, and active Git operations
without mutation or fetch. `n` starts the typed New Work flow; `:` opens actions
for the current context. New Work fetches only while building an online plan and
does not create branches or worktrees until that exact plan is confirmed.

The dashboard can create/resume New Work and open a Work in configured programs.
Sync, Repair, and Remove remain visible but disabled until their typed workflows
replace the legacy explicit commands.

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
goworktree start EVOVPC-2855 --repos api,web # create / resume project group
goworktree list
goworktree sync EVOVPC-2855     # rebase project branches onto origin bases
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
3. **`start <name>`** — for each selected repo, creates branch `<name>` from that repo’s base branch into `projects_root/<name>/`. Base resolution: config → `origin/HEAD` → main/master/develop → HEAD.
4. **Manifest** — `.goworktree.json` in the project folder. It is atomically written after each repository step, so re-run `start` to **resume** pending/failed repos safely.
5. **`add` / `drop`** — change the repo set mid-task. Legacy folders without a manifest are auto-migrated. `drop -D` deletes only **local** project branches.
6. **`remove -D`** — fully delete a project group: its worktrees, local project branches, stale Git worktree registrations, and project folder. Remote branches are never touched.
7. **`sync`** — fetch `origin` and rebase each project branch onto its configured base. A clean-worktree rebase conflict is kept open, and the conflicting worktree opens in the default program. Resolve and stage it there, then press Enter in the dashboard to retry. Dirty files are auto-stashed and restored by immutable stash ID; conflicts involving that restore are rolled back safely. Sync Git operations use a configurable timeout.
8. **`branch`** — adopt the branch currently checked out in one worktree when a repository needs a project-specific branch name.
9. **`repair`** — validates a project, preserves and rebuilds a corrupt manifest when possible, marks missing worktrees for recreation, and prunes stale Git registrations.

## Commands

| Command | Description |
|---------|-------------|
| *(no args)* | Inspect Works, create/resume New Work, and open scoped actions |
| `init` | First-time setup + repo scan |
| `start <name> --repos id,id [--open]` | Create or resume a project |
| `add <project> --repos id,id` | Add repos to a project |
| `drop <project> --repos id,id [-D] [--yes]` | Drop repos (`-D` = delete local branches) |
| `sync <project>` | Fetch and rebase project branches onto their origin base branches |
| `branch <project> <repo>` | Adopt a repository worktree's current branch in the project manifest |
| `list` / `ls` | List project groups |
| `remove` / `rm` `<name> [-D] [--yes]` | Delete a project group |
| `cursor` / `goland` / `open` | Open project in a compatibility editor / the default program |
| `programs list/add/update/delete/default/search` | Manage programs shown under Open with |
| `doctor` | Check git, editors, config |
| `repair <project>` | Reconcile a project manifest and worktrees after interruption or corruption |
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

`search` lists executable files from `PATH`; `add` and `update` require a runnable executable path or a command resolvable from `PATH`. Programs receive the project folder as their final argument. For macOS apps that need LaunchServices, use arguments, for example: `goworktree programs update codex-id-1 --path open --args "-a Codex"`.

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
- One git branch → one worktree (project branch is created with `-b` from the base).
- Editor subprocess stdout/stderr is discarded so Node/IDE noise does not corrupt the TUI.
- No shell completions yet; no multi-panel dashboard.

## License

Personal / use as you like. Not an official product.
