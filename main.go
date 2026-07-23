package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/pershin-daniil/goworktree/internal/tui"
)

func exitOnError(err error) {
	if err == nil {
		return
	}
	if tui.IsCancelled(err) {
		fmt.Println("cancelled")
		return
	}
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		exitOnError(tui.RunApp(tui.AppActions{Version: version, Execute: runAppCommand}))
		return
	}

	var err error
	switch os.Args[1] {
	case "init":
		err = runInit()
	case "start":
		err = runStart(os.Args[2:])
	case "add":
		err = runAdd(os.Args[2:])
	case "drop":
		err = runDrop(os.Args[2:])
	case "sync":
		err = runSync(os.Args[2:])
	case "branch":
		err = runBranch(os.Args[2:])
	case "list", "ls":
		err = runList()
	case "remove", "rm":
		err = runRemove(os.Args[2:])
	case "cursor":
		err = runCursor(os.Args[2:])
	case "goland":
		err = runGoland(os.Args[2:])
	case "open":
		err = runOpen(os.Args[2:])
	case "doctor":
		err = runDoctor()
	case "config":
		err = runConfig(os.Args[2:])
	case "repos":
		err = runRepos(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	case "version", "-v", "--version":
		fmt.Println(version)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}

	exitOnError(err)
}

// runAppCommand executes an explicit CLI invocation while the root TUI owns
// the terminal.  Capturing both streams prevents Git diagnostics from drawing
// over Bubble Tea's alternate screen.
func runAppCommand(args []string) (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(path, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runConfig(args []string) error {
	if len(args) == 0 {
		return runConfigShow()
	}
	switch args[0] {
	case "show":
		return runConfigShow()
	case "edit":
		return runConfigEdit()
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("usage: goworktree config set <key> <value>")
		}
		return runConfigSet(args[1], args[2])
	default:
		return fmt.Errorf("unknown config subcommand: %s", args[0])
	}
}

func runRepos(args []string) error {
	if len(args) == 0 {
		return runConfigShow()
	}
	switch args[0] {
	case "list":
		return runConfigShow()
	case "scan":
		return runReposScan()
	case "set":
		if len(args) < 2 {
			return fmt.Errorf("usage: goworktree repos set <id> [--path PATH] [--branch BRANCH]")
		}
		return runReposSet(args[1], flagValue(args, "--path"), flagValue(args, "--branch"))
	default:
		return fmt.Errorf("unknown repos subcommand: %s", args[0])
	}
}

func flagValue(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
		if len(arg) > len(name)+1 && arg[:len(name)+1] == name+"=" {
			return arg[len(name)+1:]
		}
	}
	return ""
}

func usage() {
	fmt.Fprintf(os.Stderr, `goworktree %s — git worktree helper for multi-repo projects

usage:
  goworktree                       interactive home menu
  goworktree init
  goworktree start <name> --repos id,id [--open] create or resume project
  goworktree add <project> --repos id,id add repos to an existing project
  goworktree drop <project> --repos id,id [-D] [--yes] remove repos from a project
  goworktree sync <project>        rebase project branches onto origin bases
  goworktree branch <project> <repo> adopt a worktree's current branch
  goworktree list
  goworktree remove <project> [-D] [--yes] delete whole project group
  goworktree cursor <project>
  goworktree goland <project>
  goworktree open [project]        alias for cursor
  goworktree doctor
  goworktree config [show|edit|set <key> <value>]
  goworktree repos [list|scan|set <id> --path PATH --branch BRANCH]

notes:
  without arguments, goworktree starts the unified interactive dashboard
  explicit commands never open pickers; pass required arguments and --repos
  start/add resume incomplete worktrees via .goworktree.json
  remove -D fully clears project worktrees and LOCAL branches; remotes are untouched
  drop -D deletes only LOCAL branches; remotes are untouched
  add/drop auto-migrate legacy projects (write .goworktree.json from existing worktrees)

`, version)
}
