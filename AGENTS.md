# Repository guide

`goworktree` is a Go 1.23 CLI for managing one ticket/project as a group of Git worktrees across multiple repositories. The root `main` package owns command parsing and orchestration; reusable behavior belongs under `internal/`.

## Layout

- `main.go`: CLI dispatch, aliases, help, and version.
- `commands.go`: command workflows and user-facing output.
- `internal/config`: JSON configuration, repository IDs/aliases, and path defaults.
- `internal/git`: Git subprocesses, repository scanning, branch resolution, and worktree operations.
- `internal/project`: project directories, `.goworktree.json` manifests, migration, and listing.
- `internal/tui`: Bubble Tea menus, pickers, prompts, progress, and shared styles.
- `internal/cursor`, `internal/goland`: editor launch adapters.
- `Taskfile.yml`: canonical development commands.

## Development workflow

- Install pinned repository-local development tools with `task tools:install`.
- The canonical baseline pipeline is the default `task` command. Run it before handing off changes; it executes `tidy`, `fmt`, `lint`, and `test` in that exact order.
- `task` may update Go source formatting and module metadata. Review resulting changes to `go.mod`, `go.sum`, and formatted files before committing.
- Use `task check` when a non-mutating formatting, lint, and test gate is required.
- Run `go vet ./...` for changes that touch command execution, filesystem operations, or concurrency.
- Do not commit the generated root binary `goworktree`.

## AI agent workflow

- The primary agent owns repository discovery, planning, architecture, risk decisions, integration, final review, and final verification.
- Work directly on small or tightly coupled changes. Delegation is useful only when the task contains at least two genuinely independent workstreams or a slow investigation can run independently.
- When delegation is useful, run no more than three subagents in parallel. Do not let subagents create further subagents.
- Prefer `gpt-5.6-terra` with medium reasoning for bounded implementation, tests, documentation, and mechanical changes when that model override is available. Keep architecture, ambiguous work, and final review with the primary model; fall back to the available model rather than blocking on model selection.
- Give every subagent a bounded brief containing the expected result, allowed files or packages, relevant constraints, required validation, and the expected handoff. Assign exclusive file ownership and never allow concurrent edits to the same file.
- Require subagents to report changed files, commands and tests run, failures, assumptions, and remaining risks. Subagents must not commit unless the user explicitly asks for commits.
- After delegated work, the primary agent must inspect the combined diff, resolve inconsistencies, verify that user-facing documentation remains synchronized, and run the repository's required checks. Passing worker tests is not a substitute for final integration verification.

## Design and safety constraints

- Keep the CLI dependency-light and preserve the current stdlib command dispatcher unless a change explicitly requires a parser framework.
- Keep Git execution in `internal/git`; return contextual errors instead of exiting from internal packages.
- Preserve resumability: save manifest state before work starts and update each repository status as work completes or fails.
- `.goworktree.json` is user state. Maintain backward compatibility or add migration behavior when its schema changes.
- Destructive operations must remain local and explicit. `drop -D` and `remove -D` may delete local project branches, but must never delete remote branches.
- Worktree removal must continue to handle both registered worktrees and partially broken/on-disk states, followed by pruning when possible.
- Resolve a base branch in the existing order: configured preference, `origin/HEAD`, common branch names, then `HEAD`.
- Keep editor subprocess output away from the alternate-screen TUI.
- Treat picker cancellation as a normal soft-cancel; do not turn it into an error or add an extra confirmation pause.

## Tests

- Add focused unit tests beside the package being changed.
- Git tests should use temporary repositories and local refs; they must not require network access or mutate a developer's repositories.
- Filesystem/config tests should isolate HOME/config paths and use temporary directories.
- For TUI changes, test model updates and key behavior without requiring an interactive terminal where practical.

## User-facing changes

- Keep `README.md`, `usage()` in `main.go`, and `Taskfile.yml` command help synchronized when commands, flags, requirements, or workflows change.
- Match the existing concise CLI tone and wrap underlying errors with the repository/project context a user needs to act.
