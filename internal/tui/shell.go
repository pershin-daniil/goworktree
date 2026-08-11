package tui

import (
	"bufio"
	"fmt"
	"os"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// ShellActions wires package-main command runners into the home menu.
// Use nil for optional actions you do not expose.
type ShellActions struct {
	Start  func() error
	Add    func() error
	Drop   func() error
	Sync   func() error
	Branch func() error
	List   func() error
	Cursor func() error
	Goland func() error
	Remove func() error
	Doctor func() error
	Config func() error
}

type shellItem struct {
	id    string
	title string
	desc  string
}

func (i shellItem) FilterValue() string { return i.title + " " + i.desc }
func (i shellItem) Title() string       { return i.title }
func (i shellItem) Description() string { return i.desc }

type shellModel struct {
	list     list.Model
	actions  ShellActions
	choice   string
	quitting bool
	err      error
}

func newShellModel(actions ShellActions) shellModel {
	items := []list.Item{
		shellItem{"start", "Start project", "Create or resume a worktree group"},
		shellItem{"add", "Add repos", "Add repositories to an existing project"},
		shellItem{"drop", "Drop repos", "Remove repositories from a project"},
		shellItem{"sync", "Sync project", "Rebase project branches onto their origin bases"},
		shellItem{"branch", "Project branches", "Adopt a repository worktree's current branch"},
		shellItem{"list", "List projects", "Show project groups"},
		shellItem{"cursor", "Open Cursor", "Open a project in Cursor"},
		shellItem{"goland", "Open GoLand", "Open a project in GoLand"},
		shellItem{"remove", "Remove project", "Fully delete project, worktrees, and local branches"},
		shellItem{"doctor", "Doctor", "Check git, editors, and config"},
		shellItem{"config", "Config show", "Print current configuration"},
		shellItem{"quit", "Quit", "Exit goworktree"},
	}

	d := list.NewDefaultDelegate()
	d.ShowDescription = true
	// Height grows on WindowSizeMsg; start tall enough for title+~4 rows.
	l := list.New(items, d, 72, 24)
	l.Title = "goworktree"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.DisableQuitKeybindings()
	l.Styles.Title = titleStyle

	applyVimListKeys(&l)
	return shellModel{list: l, actions: actions}
}

func (m shellModel) Init() tea.Cmd { return nil }

func (m shellModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h := msg.Height - 2
		if h < 10 {
			h = 10
		}
		m.list.SetSize(msg.Width, h)
		return m, nil
	case tea.KeyMsg:
		// ctrl+c always quits; esc/q while filtering are left to the list filter UI.
		if msg.String() == "ctrl+c" {
			m.choice = "quit"
			m.quitting = true
			return m, tea.Quit
		}
		if m.list.FilterState() == list.Filtering {
			break
		}
		switch msg.String() {
		case "q", "esc":
			m.choice = "quit"
			m.quitting = true
			return m, tea.Quit
		case "enter":
			if it, ok := m.list.SelectedItem().(shellItem); ok {
				m.choice = it.id
				m.quitting = true
				return m, tea.Quit
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m shellModel) View() string {
	if m.quitting {
		return ""
	}
	return m.list.View() + "\n" + hintStyle.Render("j/k move  enter select  / filter  q quit")
}

func runAction(id string, a ShellActions) error {
	switch id {
	case "quit", "":
		return nil
	case "start":
		if a.Start == nil {
			return fmt.Errorf("start not available")
		}
		return a.Start()
	case "add":
		if a.Add == nil {
			return fmt.Errorf("add not available")
		}
		return a.Add()
	case "drop":
		if a.Drop == nil {
			return fmt.Errorf("drop not available")
		}
		return a.Drop()
	case "sync":
		if a.Sync == nil {
			return fmt.Errorf("sync not available")
		}
		return a.Sync()
	case "branch":
		if a.Branch == nil {
			return fmt.Errorf("branch not available")
		}
		return a.Branch()
	case "list":
		if a.List == nil {
			return fmt.Errorf("list not available")
		}
		return a.List()
	case "cursor":
		if a.Cursor == nil {
			return fmt.Errorf("cursor not available")
		}
		return a.Cursor()
	case "goland":
		if a.Goland == nil {
			return fmt.Errorf("goland not available")
		}
		return a.Goland()
	case "remove":
		if a.Remove == nil {
			return fmt.Errorf("remove not available")
		}
		return a.Remove()
	case "doctor":
		if a.Doctor == nil {
			return fmt.Errorf("doctor not available")
		}
		return a.Doctor()
	case "config":
		if a.Config == nil {
			return fmt.Errorf("config not available")
		}
		return a.Config()
	default:
		return fmt.Errorf("unknown action %q", id)
	}
}

// shellNeedsPause: these dump to the main screen; pause before the alt-screen menu.
func shellNeedsPause(id string) bool {
	switch id {
	case "sync", "branch", "list", "doctor", "config":
		return true
	default:
		return false
	}
}

// waitContinue pauses on the main screen before remounting the alt-screen menu.
// Only use after commands that printed readable output (list/doctor/config).
// Do not use after soft-cancel: esc already dismissed the picker; an extra enter
// feels like a double confirm, and any buffered '\n' would skip the pause.
func waitContinue() {
	fmt.Print(hintStyle.Render("\npress enter to continue…"))
	_, _ = bufio.NewReader(os.Stdin).ReadBytes('\n')
	fmt.Println()
}

// RunShell shows the home menu and dispatches the chosen action.
// After a non-quit action completes successfully, the menu is shown again.
// Cancelled pickers return to the menu immediately (no "cancelled" + enter pause).
// Other errors end the shell.
func RunShell(actions ShellActions) error {
	for {
		m := newShellModel(actions)
		final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
		if err != nil {
			return err
		}
		sm := final.(shellModel)
		if sm.choice == "quit" || sm.choice == "" {
			return nil
		}
		if err := runAction(sm.choice, actions); err != nil {
			if IsCancelled(err) {
				// Remount menu; printing here is wasted (next WithAltScreen hides it)
				// and waiting for enter is a soft-cancel UX bug.
				continue
			}
			return err
		}
		if shellNeedsPause(sm.choice) {
			waitContinue()
		} else {
			fmt.Println()
		}
	}
}
