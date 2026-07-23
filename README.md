# goworktree

Personal CLI for **multi-repo git worktree project groups** — pick repos, create a shared branch worktree folder, open it in Cursor or GoLand.

Running `goworktree` with no arguments opens one persistent dashboard: navigation, forms, confirmations, progress, and results stay inside the same terminal UI. A failed operation exposes `c` to copy a structured diagnostic report; it can include local paths and Git error text.

**MVP status.** Stable enough for daily ticket work. Stack: stdlib CLI, JSON config, Bubble Tea (no external `fzf`).

Repo: [github.com/pershin-daniil/goworktree](https://github.com/pershin-daniil/goworktree)

## Install

```bash
go install github.com/pershin-daniil/goworktree@latest
```

Or from a clone:

```bash
task install   # or: go install .
```

Requires: **Go 1.23+**, **git**, and optionally Cursor / GoLand.

## Quickstart

```bash
goworktree init                 # TUI: paths + scan repos_root
goworktree                      # unified interactive dashboard (vim keys)
goworktree start EVOVPC-2855 --repos api,web # create / resume project group
goworktree list
goworktree sync EVOVPC-2855     # rebase project branches onto origin bases
goworktree cursor EVOVPC-2855   # open a project in Cursor
```

With [Task](https://taskfile.dev):

```bash
task                  # build + dep check
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
7. **`sync`** — fetch `origin` and rebase each project branch onto its configured base. Dirty files are auto-stashed and restored by immutable stash ID; a rebase or restore conflict rolls that repository back. Sync Git operations use a configurable timeout.
8. **`branch`** — adopt the branch currently checked out in one worktree when a repository needs a project-specific branch name.
9. **`repair`** — validates a project, preserves and rebuilds a corrupt manifest when possible, marks missing worktrees for recreation, and prunes stale Git registrations.

## Commands

| Command | Description |
|---------|-------------|
| *(no args)* | Unified interactive dashboard |
| `init` | First-time setup + repo scan |
| `start <name> --repos id,id [--open]` | Create or resume a project |
| `add <project> --repos id,id` | Add repos to a project |
| `drop <project> --repos id,id [-D] [--yes]` | Drop repos (`-D` = delete local branches) |
| `sync <project>` | Fetch and rebase project branches onto their origin base branches |
| `branch <project> <repo>` | Adopt a repository worktree's current branch in the project manifest |
| `list` / `ls` | List project groups |
| `remove` / `rm` `<name> [-D] [--yes]` | Delete a project group |
| `cursor` / `goland` / `open` | Open project in editor |
| `doctor` | Check git, editors, config |
| `repair <project>` | Reconcile a project manifest and worktrees after interruption or corruption |
| `config show` / `edit` / `set <key> <value>` | Configuration |
| `repos list` / `scan` / `set …` | Manage scanned repos |
| `version` / `help` | Meta |

## Keys (home menu & pickers)

| Key | Action |
|-----|--------|
| `j` / `k` | down / up |
| `g` / `G` | top / bottom |
| `/` | filter |
| `Space` | toggle (multi-select) |
| `Enter` | confirm |
| `Esc` / `q` | cancel / quit |
| `Ctrl+C` | hard quit |

The dashboard never drops to a console-only screen or asks for a continuation pause. On failure, press `c` to copy the diagnostic report and `Enter`/`Esc` to return to the dashboard.

## Config sketch

```json
{
  "cursor_path": "cursor",
  "goland_path": "goland",
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

Useful keys for `goworktree config set`:

- `repos_root`, `projects_root`, `default_branch`, `scan_depth`, `command_timeout_seconds`
- `cursor_path`, `goland_path`

## Development

```bash
task build
task test
task vet
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
