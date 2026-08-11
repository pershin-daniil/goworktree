package tui

// The application in this file is deliberately the only long-lived Bubble Tea
// program used by the interactive entry point.  Command mode stays outside of
// it, which keeps it usable from scripts and prevents command output from
// corrupting the alternate screen.

import (
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/pershin-daniil/goworktree/internal/config"
	"github.com/pershin-daniil/goworktree/internal/project"
)

// AppActions is the narrow boundary between the TUI and command execution.
// Execute must not write to the terminal: its output is rendered as a result.
type AppActions struct {
	Version string
	Execute func([]string) (string, error)
}

type appScreen int

const (
	appDashboard appScreen = iota
	appProjectActions
	appSettings
	appRepositories
	appPickRepos
	appPickBranchRepo
	appPickDefaultProgram
	appPrograms
	appProgramActions
	appProgramForm
	appInput
	appSetup
	appConfirm
	appRunning
	appResult
)

type appItem struct{ id, title, desc string }

func (i appItem) FilterValue() string { return i.title + " " + i.desc }
func (i appItem) Title() string       { return i.title }
func (i appItem) Description() string { return i.desc }

// repoPickerDelegate renders the current multi-selection state. The standard
// list delegate has no notion of checked items, which made Space appear to do
// nothing in the repository picker.
type repoPickerDelegate struct {
	list.DefaultDelegate
	selected map[string]bool
}

func (d repoPickerDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	if it, ok := item.(appItem); ok {
		mark := "○ "
		if d.selected[it.id] {
			mark = "✓ "
		}
		it.title = mark + it.title
		item = it
	}
	d.DefaultDelegate.Render(w, m, index, item)
}

type appDoneMsg struct {
	output string
	err    error
}

type appModel struct {
	actions       AppActions
	screen        appScreen
	list          list.Model
	projects      []project.Entry
	cfg           *config.Config
	configErr     error
	selected      string
	programID     string
	pendingAction string
	pendingArgs   []string
	confirmScreen appScreen
	input         textinput.Model
	setupInputs   []textinput.Model
	setupFocus    int
	selectedRepos map[string]bool
	message       string
	output        string
	err           error
	log           []string
	quitting      bool
}

// RunApp starts the one and only interactive program.
func RunApp(actions AppActions) error {
	m := newAppModel(actions)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func newAppModel(actions AppActions) appModel {
	m := appModel{actions: actions, screen: appDashboard, selectedRepos: map[string]bool{}}
	m.reload()
	if m.cfg == nil && !config.Default().Exists() {
		m.cfg = config.Default()
		m.openSetup()
	} else if m.cfg == nil {
		m.err = m.configErr
		m.message = "could not load existing configuration"
		m.screen = appResult
	} else {
		m.setDashboard()
	}
	return m
}

func (m *appModel) openSetup() {
	values := []string{m.cfg.ReposRoot, m.cfg.ProjectsRoot, m.cfg.DefaultBranch}
	m.setupInputs = make([]textinput.Model, len(values))
	for i, value := range values {
		input := textinput.New()
		input.SetValue(value)
		input.Width = 56
		input.Prompt = "› "
		if i == 0 {
			input.Focus()
		}
		m.setupInputs[i] = input
	}
	m.setupFocus = 0
	m.screen = appSetup
}

func (m *appModel) reload() {
	m.cfg, m.configErr = config.Load()
	m.projects = nil
	if m.cfg != nil {
		m.projects, _ = project.List(m.cfg)
	}
}

func (m *appModel) setList(title string, items []list.Item) {
	d := list.NewDefaultDelegate()
	d.ShowDescription = true
	m.list = list.New(items, d, 78, 24)
	m.list.Title = title
	m.list.SetShowStatusBar(false)
	m.list.SetFilteringEnabled(true)
	m.list.DisableQuitKeybindings()
	m.list.Styles.Title = titleStyle
	applyVimListKeys(&m.list)
}

func (m *appModel) setDashboard() {
	items := []list.Item{appItem{"new", "New project", "Create or resume a worktree group"}}
	for _, p := range m.projects {
		desc := fmt.Sprintf("%d repositories", p.Repos)
		if p.Manifest != nil && p.Manifest.ReadyCount() < len(p.Manifest.Repos) {
			desc = fmt.Sprintf("%d/%d ready", p.Manifest.ReadyCount(), len(p.Manifest.Repos))
		}
		items = append(items, appItem{"project:" + p.Name, p.Name, desc})
	}
	items = append(items,
		appItem{"repos", "Repositories", "Scan configured repository root"},
		appItem{"settings", "Settings", "View and edit configuration values"},
		appItem{"doctor", "Doctor", "Check git, editors, and config"},
		appItem{"quit", "Quit", "Exit goworktree"},
	)
	m.setList("goworktree · projects", items)
	m.screen = appDashboard
}

func (m *appModel) setProjectActions() {
	items := []list.Item{
		appItem{"add", "Add repositories", "Add configured repositories"},
		appItem{"drop", "Drop repositories", "Remove worktrees from this project"},
		appItem{"sync", "Sync project", "Fetch and rebase project branches"},
		appItem{"branch", "Adopt branch", "Save a worktree's current branch"},
	}
	if m.cfg != nil {
		for _, id := range m.cfg.EnabledPrograms() {
			p, _ := m.cfg.Program(id)
			items = append(items, appItem{"open:" + id, "Open " + p.Name, "Open project folder in " + p.Name})
		}
	}
	items = append(items,
		appItem{"remove", "Remove project", "Delete worktrees and local branches"},
		appItem{"back", "Back", "Return to projects"},
	)
	m.setList("project · "+m.selected, items)
	m.screen = appProjectActions
}

func (m *appModel) setSettings() {
	items := []list.Item{appItem{"back", "Back", "Return to projects"}}
	if m.cfg != nil {
		items = append(items,
			appItem{"open_with", "Open with", fmt.Sprintf("%d configured programs", len(m.cfg.OpenWith))},
			appItem{"default_program", "Default program", programLabel(m.cfg, m.cfg.DefaultProgram)},
			appItem{"repos_root", "Repositories root", m.cfg.ReposRoot},
			appItem{"projects_root", "Projects root", m.cfg.ProjectsRoot},
			appItem{"default_branch", "Default branch", m.cfg.DefaultBranch},
			appItem{"scan_depth", "Scan depth", fmt.Sprintf("%d", m.cfg.ScanDepth)},
		)
	}
	m.setList("settings", items)
	m.screen = appSettings
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled · enter to disable"
	}
	return "disabled · enter to enable"
}

func programLabel(cfg *config.Config, program string) string {
	if program == "" {
		return "none enabled"
	}
	p, ok := cfg.Program(program)
	if !ok {
		return program
	}
	return p.Name
}

func (m *appModel) setDefaultProgramPicker() {
	items := make([]list.Item, 0, len(m.cfg.OpenWith))
	for _, id := range m.cfg.EnabledPrograms() {
		p, _ := m.cfg.Program(id)
		items = append(items, appItem{id, p.Name, p.Path})
	}
	m.setList("Default program", items)
	m.screen = appPickDefaultProgram
}

func (m *appModel) setPrograms() {
	items := []list.Item{appItem{"add", "Add program", "Add an executable to Open with"}, appItem{"default", "Choose default", "Select the default enabled program"}, appItem{"back", "Back", "Return to settings"}}
	for _, id := range m.cfg.ProgramIDs() {
		p, _ := m.cfg.Program(id)
		state := "enabled"
		if !p.Enabled {
			state = "disabled"
		}
		if m.cfg.DefaultProgram == id {
			state += " · default"
		}
		command := strings.TrimSpace(p.Path + " " + strings.Join(p.Args, " "))
		items = append(items, appItem{"program:" + id, p.Name, id + " · " + command + " · " + state})
	}
	m.setList("Open with", items)
	m.screen = appPrograms
}

func (m *appModel) setProgramActions() {
	p, _ := m.cfg.Program(m.programID)
	items := []list.Item{
		appItem{"edit", "Edit", p.Name + " · " + strings.TrimSpace(p.Path+" "+strings.Join(p.Args, " "))},
		appItem{"toggle", "Toggle enabled", enabledLabel(p.Enabled)},
		appItem{"default", "Set default", "Use this program for open and conflicts"},
		appItem{"delete", "Delete", "Remove this program"},
		appItem{"back", "Back", "Return to Open with"},
	}
	m.setList("Open with · "+p.Name, items)
	m.screen = appProgramActions
}

func (m *appModel) setRepositories() {
	items := []list.Item{appItem{"scan", "Scan repositories", "Discover Git repositories under repositories root"}, appItem{"back", "Back", "Return to projects"}}
	if m.cfg != nil {
		ids := m.cfg.RepoNames()
		sort.Strings(ids)
		for _, id := range ids {
			repo := m.cfg.Repos[id]
			items = append(items, appItem{"repo:" + id, m.cfg.DisplayName(id), repo.Path + " · " + m.cfg.RepoBranch(id)})
		}
	}
	m.setList("repositories", items)
	m.screen = appRepositories
}

func (m appModel) Init() tea.Cmd {
	if m.screen == appSetup {
		return textinput.Blink
	}
	return nil
}

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, max(8, msg.Height-3))
		return m, nil
	case appDoneMsg:
		m.output, m.err = strings.TrimSpace(msg.output), msg.err
		if m.output != "" {
			m.log = append(m.log, strings.Split(m.output, "\n")...)
		}
		m.reload()
		m.screen = appResult
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.screen == appRunning {
			return m, nil
		}
		if m.screen == appResult {
			if msg.String() == "c" && m.err != nil {
				if err := clipboard.WriteAll(m.diagnostic()); err != nil {
					m.message = "could not copy report: " + err.Error()
				} else {
					m.message = "diagnostic report copied"
				}
				return m, nil
			}
			if msg.String() == "enter" && m.err != nil && len(m.pendingArgs) > 0 && m.pendingArgs[0] == "sync" && strings.Contains(m.output, "conflict") {
				m.start(m.pendingArgs...)
				return m, m.command()
			}
			if msg.String() == "enter" || msg.String() == "esc" || msg.String() == "q" {
				m.setDashboard()
				return m, nil
			}
		}
		if m.screen == appInput {
			if msg.String() == "esc" {
				m.setDashboard()
				return m, nil
			}
			if msg.String() == "enter" {
				value := strings.TrimSpace(m.input.Value())
				if value == "" {
					m.message = "a value is required"
					return m, nil
				}
				if m.pendingAction == "new" {
					m.selected = value
					m.prepareRepoPicker("start")
				} else {
					m.start(append(m.pendingArgs, value)...)
					return m, m.command()
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		if m.screen == appProgramForm {
			switch msg.String() {
			case "esc":
				m.setPrograms()
				return m, nil
			case "tab", "down":
				m.setupInputs[m.setupFocus].Blur()
				m.setupFocus = (m.setupFocus + 1) % len(m.setupInputs)
				m.setupInputs[m.setupFocus].Focus()
				return m, textinput.Blink
			case "shift+tab", "up":
				m.setupInputs[m.setupFocus].Blur()
				m.setupFocus = (m.setupFocus + len(m.setupInputs) - 1) % len(m.setupInputs)
				m.setupInputs[m.setupFocus].Focus()
				return m, textinput.Blink
			case "enter":
				if m.setupFocus < len(m.setupInputs)-1 {
					m.setupInputs[m.setupFocus].Blur()
					m.setupFocus++
					m.setupInputs[m.setupFocus].Focus()
					return m, textinput.Blink
				}
				var args []string
				if m.pendingAction == "program-edit" {
					args = []string{"programs", "update", m.programID, "--name", strings.TrimSpace(m.setupInputs[0].Value()), "--path", strings.TrimSpace(m.setupInputs[1].Value()), "--args", strings.TrimSpace(m.setupInputs[2].Value())}
				} else {
					args = []string{"programs", "add", strings.TrimSpace(m.setupInputs[0].Value()), "--name", strings.TrimSpace(m.setupInputs[1].Value()), "--path", strings.TrimSpace(m.setupInputs[2].Value()), "--args", strings.TrimSpace(m.setupInputs[3].Value())}
				}
				m.start(args...)
				return m, m.command()
			}
			var cmd tea.Cmd
			m.setupInputs[m.setupFocus], cmd = m.setupInputs[m.setupFocus].Update(msg)
			return m, cmd
		}
		if m.screen == appSetup {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "tab", "down":
				m.setupInputs[m.setupFocus].Blur()
				m.setupFocus = (m.setupFocus + 1) % len(m.setupInputs)
				m.setupInputs[m.setupFocus].Focus()
				return m, textinput.Blink
			case "shift+tab", "up":
				m.setupInputs[m.setupFocus].Blur()
				m.setupFocus = (m.setupFocus + len(m.setupInputs) - 1) % len(m.setupInputs)
				m.setupInputs[m.setupFocus].Focus()
				return m, textinput.Blink
			case "enter":
				if m.setupFocus < len(m.setupInputs)-1 {
					m.setupInputs[m.setupFocus].Blur()
					m.setupFocus++
					m.setupInputs[m.setupFocus].Focus()
					return m, textinput.Blink
				}
				m.cfg.ReposRoot = strings.TrimSpace(m.setupInputs[0].Value())
				m.cfg.ProjectsRoot = strings.TrimSpace(m.setupInputs[1].Value())
				m.cfg.DefaultBranch = strings.TrimSpace(m.setupInputs[2].Value())
				if err := m.cfg.Save(); err != nil {
					m.err = err
					m.message = "setup failed"
					m.screen = appResult
					return m, nil
				}
				m.start("repos", "scan")
				return m, m.command()
			}
			var cmd tea.Cmd
			m.setupInputs[m.setupFocus], cmd = m.setupInputs[m.setupFocus].Update(msg)
			return m, cmd
		}
		if m.screen == appConfirm {
			switch msg.String() {
			case "y", "Y", "enter":
				m.start(m.pendingArgs...)
				return m, m.command()
			case "n", "N", "esc", "q":
				if m.confirmScreen == appPrograms {
					m.setPrograms()
				} else {
					m.setProjectActions()
				}
				return m, nil
			}
		}
		if m.screen == appPickRepos {
			if m.list.FilterState() != list.Filtering {
				switch msg.String() {
				case "esc", "q":
					m.setProjectActions()
					return m, nil
				case " ":
					if it, ok := m.list.SelectedItem().(appItem); ok {
						m.selectedRepos[it.id] = !m.selectedRepos[it.id]
					}
					return m, nil
				case "enter":
					ids := selectedIDs(m.selectedRepos)
					if len(ids) == 0 {
						m.message = "select at least one repository"
						return m, nil
					}
					if m.pendingAction == "drop" {
						m.message = "Drop selected repositories? Their worktrees will be removed."
						m.pendingArgs = []string{"drop", m.selected, "--repos", strings.Join(ids, ","), "--yes"}
						m.screen = appConfirm
						return m, nil
					}
					m.start(m.pendingAction, m.selected, "--repos", strings.Join(ids, ","))
					return m, m.command()
				}
			}
		}
		if m.screen == appPickBranchRepo && m.list.FilterState() != list.Filtering {
			if msg.String() == "esc" || msg.String() == "q" {
				m.setProjectActions()
				return m, nil
			}
			if msg.String() == "enter" {
				if it, ok := m.list.SelectedItem().(appItem); ok {
					m.start("branch", m.selected, it.id)
					return m, m.command()
				}
				return m, nil
			}
		}
		if m.screen == appPickDefaultProgram && m.list.FilterState() != list.Filtering {
			if msg.String() == "esc" || msg.String() == "q" {
				m.setSettings()
				return m, nil
			}
			if msg.String() == "enter" {
				if it, ok := m.list.SelectedItem().(appItem); ok {
					m.cfg.DefaultProgram = it.id
					if err := m.cfg.Save(); err != nil {
						m.err, m.message, m.screen = err, "could not save default program", appResult
						return m, nil
					}
					m.setSettings()
				}
				return m, nil
			}
		}
		if (m.screen == appDashboard || m.screen == appProjectActions || m.screen == appSettings || m.screen == appRepositories || m.screen == appPrograms || m.screen == appProgramActions) && m.list.FilterState() != list.Filtering {
			if msg.String() == "esc" && m.screen == appProjectActions {
				m.setDashboard()
				return m, nil
			}
			if msg.String() == "esc" && m.screen == appSettings {
				m.setDashboard()
				return m, nil
			}
			if msg.String() == "esc" && m.screen == appRepositories {
				m.setDashboard()
				return m, nil
			}
			if msg.String() == "esc" && m.screen == appPrograms {
				m.setSettings()
				return m, nil
			}
			if msg.String() == "esc" && m.screen == appProgramActions {
				m.setPrograms()
				return m, nil
			}
			if msg.String() == "q" && m.screen == appDashboard {
				return m, tea.Quit
			}
			if msg.String() == "enter" {
				return m, m.activate()
			}
		}
	}
	var cmd tea.Cmd
	if m.screen != appInput && m.screen != appConfirm && m.screen != appResult {
		m.list, cmd = m.list.Update(msg)
	}
	return m, cmd
}

func selectedIDs(selected map[string]bool) []string {
	var ids []string
	for id, on := range selected {
		if on {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (m *appModel) activate() tea.Cmd {
	it, ok := m.list.SelectedItem().(appItem)
	if !ok {
		return nil
	}
	if m.screen == appDashboard {
		switch it.id {
		case "quit":
			m.quitting = true
			return tea.Quit
		case "new":
			m.openInput("new", "New project", "ticket-123", nil)
			return nil
		case "repos":
			m.setRepositories()
			return nil
		case "settings":
			m.setSettings()
			return nil
		case "doctor":
			m.start("doctor")
			return m.command()
		default:
			if strings.HasPrefix(it.id, "project:") {
				m.selected = strings.TrimPrefix(it.id, "project:")
				m.setProjectActions()
			}
			return nil
		}
	}
	if m.screen == appSettings {
		if it.id == "back" {
			m.setDashboard()
			return nil
		}
		if it.id == "default_program" {
			if len(m.cfg.EnabledPrograms()) == 0 {
				m.err, m.message, m.screen = fmt.Errorf("enable a program before choosing a default"), "no programs enabled", appResult
				return nil
			}
			m.setDefaultProgramPicker()
			return nil
		}
		if it.id == "open_with" {
			m.setPrograms()
			return nil
		}
		m.openInput("setting", "Set "+it.title, it.desc, []string{"config", "set", it.id})
		return nil
	}
	if m.screen == appPrograms {
		switch {
		case it.id == "back":
			m.setSettings()
		case it.id == "add":
			m.openProgramForm(false)
		case it.id == "default":
			m.setDefaultProgramPicker()
		case strings.HasPrefix(it.id, "program:"):
			m.programID = strings.TrimPrefix(it.id, "program:")
			m.setProgramActions()
		}
		return nil
	}
	if m.screen == appProgramActions {
		switch it.id {
		case "back":
			m.setPrograms()
		case "edit":
			m.openProgramForm(true)
		case "toggle":
			p, _ := m.cfg.Program(m.programID)
			value := "false"
			if !p.Enabled {
				value = "true"
			}
			m.start("programs", "update", m.programID, "--enabled="+value)
			return m.command()
		case "default":
			m.start("programs", "default", m.programID)
			return m.command()
		case "delete":
			m.message = "Delete program " + m.programID + "?"
			m.pendingArgs = []string{"programs", "delete", m.programID}
			m.confirmScreen = appPrograms
			m.screen = appConfirm
		}
		return nil
	}
	if m.screen == appRepositories {
		if it.id == "back" {
			m.setDashboard()
			return nil
		}
		if it.id == "scan" {
			m.start("repos", "scan")
			return m.command()
		}
		if strings.HasPrefix(it.id, "repo:") {
			id := strings.TrimPrefix(it.id, "repo:")
			m.openInput("repo", "Set default branch for "+it.title, m.cfg.RepoBranch(id), []string{"repos", "set", id, "--branch"})
		}
		return nil
	}
	switch it.id {
	case "back":
		m.setDashboard()
	case "add", "drop":
		m.prepareRepoPicker(it.id)
	case "sync":
		m.start("sync", m.selected)
		return m.command()
	case "branch":
		m.prepareBranchPicker()
	default:
		if strings.HasPrefix(it.id, "open:") {
			m.start("programs", "open", strings.TrimPrefix(it.id, "open:"), m.selected)
			return m.command()
		}
		switch it.id {
		case "remove":
			m.message = "Remove project and all local project branches?"
			m.pendingArgs = []string{"remove", m.selected, "-D", "--yes"}
			m.confirmScreen = appProjectActions
			m.screen = appConfirm
		}
		return nil
	}
	return nil
}

func (m *appModel) openProgramForm(edit bool) {
	values := []string{"", "", "", ""}
	if edit {
		p, _ := m.cfg.Program(m.programID)
		values = []string{p.Name, p.Path, strings.Join(p.Args, " ")}
		m.pendingAction = "program-edit"
	} else {
		m.pendingAction = "program-add"
	}
	m.setupInputs = make([]textinput.Model, len(values))
	for i, value := range values {
		input := textinput.New()
		input.SetValue(value)
		input.Width = 56
		input.Prompt = "› "
		if i == 0 {
			input.Focus()
		}
		m.setupInputs[i] = input
	}
	m.setupFocus = 0
	m.screen = appProgramForm
}

func (m *appModel) openInput(action, title, placeholder string, args []string) {
	m.pendingAction, m.pendingArgs = action, args
	m.message = title
	m.input = textinput.New()
	m.input.Placeholder = placeholder
	m.input.Focus()
	m.input.Width = 48
	m.screen = appInput
}

func (m *appModel) prepareRepoPicker(action string) {
	m.pendingAction, m.selectedRepos = action, map[string]bool{}
	if m.cfg == nil {
		m.message = "configuration not found"
		m.screen = appResult
		return
	}
	inProject := map[string]bool{}
	if action == "add" || action == "drop" {
		for _, p := range m.projects {
			if p.Name == m.selected && p.Manifest != nil {
				for _, r := range p.Manifest.Repos {
					inProject[r.ID] = true
				}
			}
		}
	}
	var ids []string
	for _, id := range m.cfg.RepoNames() {
		if (action == "add" && !inProject[id]) || (action == "drop" && inProject[id]) || action == "start" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	items := make([]list.Item, 0, len(ids))
	for _, id := range ids {
		items = append(items, appItem{id, m.cfg.DisplayName(id), m.cfg.RepoBranch(id)})
	}
	m.setList(map[string]string{"start": "Select repositories", "add": "Add repositories", "drop": "Drop repositories"}[action], items)
	m.list.SetDelegate(repoPickerDelegate{DefaultDelegate: list.NewDefaultDelegate(), selected: m.selectedRepos})
	m.screen = appPickRepos
}

func (m *appModel) prepareBranchPicker() {
	var items []list.Item
	for _, p := range m.projects {
		if p.Name == m.selected {
			for _, w := range p.Worktrees {
				items = append(items, appItem{w.Name, w.Name, w.Branch})
			}
		}
	}
	m.setList("Adopt current branch", items)
	m.screen = appPickBranchRepo
}

func (m *appModel) start(args ...string) {
	m.pendingArgs = args
	m.log = nil
	m.output = ""
	m.err = nil
	m.message = "running " + strings.Join(args, " ")
	m.screen = appRunning
}

func (m appModel) command() tea.Cmd {
	args := append([]string(nil), m.pendingArgs...)
	return func() tea.Msg { out, err := m.actions.Execute(args); return appDoneMsg{out, err} }
}

func (m appModel) View() string {
	if m.quitting {
		return ""
	}
	switch m.screen {
	case appInput:
		return Box(Title(m.message) + "\n\n" + m.input.View() + "\n\n" + hintStyle.Render("enter save  esc cancel"))
	case appSetup:
		labels := []string{"Repositories root", "Projects root", "Default branch"}
		var body strings.Builder
		body.WriteString(Title("goworktree setup"))
		body.WriteString("\n\nConfigure paths and scan repositories.\n\n")
		for i, label := range labels {
			body.WriteString(labelStyle.Render(label))
			body.WriteString("\n")
			body.WriteString(m.setupInputs[i].View())
			body.WriteString("\n\n")
		}
		body.WriteString(hintStyle.Render("tab navigate  enter next/save  ctrl+c quit"))
		return Box(body.String())
	case appProgramForm:
		labels := []string{"ID", "Name", "Executable path", "Arguments (space-separated)"}
		if m.pendingAction == "program-edit" {
			labels = []string{"Name", "Executable path", "Arguments (space-separated)"}
		}
		var body strings.Builder
		body.WriteString(Title("Open with · program"))
		body.WriteString("\n\n")
		for i, label := range labels {
			body.WriteString(labelStyle.Render(label))
			body.WriteString("\n")
			body.WriteString(m.setupInputs[i].View())
			body.WriteString("\n\n")
		}
		body.WriteString(hintStyle.Render("tab navigate  enter next/save  esc cancel"))
		return Box(body.String())
	case appConfirm:
		return Box(Title("confirm") + "\n\n" + m.message + "\n\n" + hintStyle.Render("y/enter confirm  n/esc cancel"))
	case appRunning:
		return Box(Title("working") + "\n\n" + m.message + "\n\n" + hintStyle.Render("operation is running…"))
	case appResult:
		status := okStyle.Render("✓ completed")
		if m.err != nil {
			status = errStyle.Render("✗ failed: " + m.err.Error())
		}
		body := Title("operation result") + "\n\n" + status
		if m.output != "" {
			body += "\n\n" + m.output
		}
		if m.message != "" && m.err != nil {
			body += "\n\n" + hintStyle.Render(m.message)
		}
		if m.err != nil {
			hint := "c copy diagnostic report  enter/esc dashboard"
			if len(m.pendingArgs) > 0 && m.pendingArgs[0] == "sync" && strings.Contains(m.output, "conflict") {
				hint = "resolve in the opened program, then enter retry  esc dashboard  c copy diagnostic report"
			}
			body += "\n\n" + hintStyle.Render(hint)
		} else {
			body += "\n\n" + hintStyle.Render("enter/esc dashboard")
		}
		return Box(body)
	default:
		hint := "j/k move  enter select  / filter  q quit"
		if m.screen == appPickRepos {
			hint = "j/k move  space toggle  enter confirm  esc cancel"
		}
		return m.list.View() + "\n" + hintStyle.Render(hint)
	}
}

func (m appModel) diagnostic() string {
	return fmt.Sprintf("goworktree diagnostic report\nreport_version: 1\napp_version: %s\nos: %s/%s\ntimestamp: %s\naction: %s\nproject: %s\nstatus: failed\nerror: %v\n\noperation_log:\n%s\n", m.actions.Version, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339), strings.Join(m.pendingArgs, " "), m.selected, m.err, strings.Join(m.log, "\n"))
}
