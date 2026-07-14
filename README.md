# goworktree

Personal CLI for **multi-repo git worktree project groups** — pick repos, create a shared branch worktree folder, open it in Cursor or GoLand.

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
goworktree                      # interactive home menu (vim keys)
goworktree start EVOVPC-2855    # create / resume project group
goworktree list
goworktree cursor               # pick project → open in Cursor
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
4. **Manifest** — `.goworktree.json` in the project folder. Re-run `start` to **resume** pending/failed repos.
5. **`add` / `drop`** — change the repo set mid-task. Legacy folders without a manifest are auto-migrated. `drop -D` deletes only **local** project branches.
6. **`remove -D`** — delete the whole group; `-D` deletes local project branches (never remotes).

## Commands

| Command | Description |
|---------|-------------|
| *(no args)* | Interactive home menu |
| `init` | First-time setup + repo scan |
| `start [name] [--open]` | Create or resume a project |
| `add [project]` | Add repos to a project |
| `drop [project] [-D]` | Drop repos (`-D` = delete local branches) |
| `list` / `ls` | List project groups |
| `remove` / `rm` `[name] [-D]` | Delete a project group |
| `cursor` / `goland` / `open` | Open project in editor |
| `doctor` | Check git, editors, config |
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

After `list`, `doctor`, or `config` from the home menu: **press Enter** to return to the menu.

## Config sketch

```json
{
  "cursor_path": "cursor",
  "goland_path": "goland",
  "repos_root": "/Users/you/Projects",
  "projects_root": "/Users/you/Projects/worktrees",
  "default_branch": "main",
  "scan_depth": 3,
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

- `repos_root`, `projects_root`, `default_branch`, `scan_depth`
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

- One git branch → one worktree (project branch is created with `-b` from the base).
- Editor subprocess stdout/stderr is discarded so Node/IDE noise does not corrupt the TUI.
- No shell completions yet; no multi-panel dashboard.

## License

Personal / use as you like. Not an official product.
