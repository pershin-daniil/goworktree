package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/project"
)

// CreateTask is one worktree to create or resume.
type CreateTask struct {
	ID         string
	Folder     string
	RepoPath   string
	Branch     string
	BaseBranch string
	Dest       string
}

type progressTickMsg struct {
	index int
	err   error
}

type progressModel struct {
	spinner    spinner.Model
	tasks      []CreateTask
	manifest   *project.Manifest
	projectDir string
	index      int
	err        error
	done       bool
	log        []string
}

// TasksFromManifest builds create tasks for pending/failed repos (or all if onlyPending is false).
func TasksFromManifest(projectDir string, m *project.Manifest, onlyPending bool) []CreateTask {
	tasks := make([]CreateTask, 0, len(m.Repos))
	for _, r := range m.Repos {
		if onlyPending && r.Status != project.StatusPending && r.Status != project.StatusFailed {
			continue
		}
		folder := r.Folder
		if folder == "" {
			folder = r.ID
		}
		tasks = append(tasks, CreateTask{
			ID:         r.ID,
			Folder:     folder,
			RepoPath:   r.Path,
			Branch:     r.Branch,
			BaseBranch: r.Base,
			Dest:       filepath.Join(projectDir, folder),
		})
	}
	return tasks
}

// RunCreate creates/resumes worktrees and updates the manifest.
// If allowProjectCleanup is true and creation fails, offers to wipe the whole project folder.
// When false (e.g. add into existing project), only leaves state for resume.
func RunCreate(projectDir string, m *project.Manifest, tasks []CreateTask, allowProjectCleanup bool) error {
	if len(tasks) == 0 {
		return nil
	}

	pm := progressModel{
		tasks:      tasks,
		manifest:   m,
		projectDir: projectDir,
	}
	pm.spinner = spinner.New()
	pm.spinner.Spinner = spinner.Dot

	p := tea.NewProgram(pm, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return err
	}
	result := final.(progressModel)
	if result.err == nil {
		return nil
	}

	if !allowProjectCleanup {
		return fmt.Errorf("%w (left for resume)", result.err)
	}

	cleanup, cerr := Confirm(fmt.Sprintf(
		"Create failed:\n%s\n\nyes = cleanup project\nno = leave for resume (`goworktree start %s`)",
		result.err.Error(), m.Name,
	))
	if cerr != nil {
		return result.err
	}
	if cleanup {
		_ = project.Remove(project.Entry{
			Name:     m.Name,
			Path:     projectDir,
			Manifest: m,
		}, false)
		return fmt.Errorf("%w (cleaned up)", result.err)
	}
	return fmt.Errorf("%w (left for resume)", result.err)
}

func (m progressModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, runCreateTask(0, m.tasks))
}

func runCreateTask(index int, tasks []CreateTask) tea.Cmd {
	return func() tea.Msg {
		if index >= len(tasks) {
			return progressTickMsg{index: index}
		}
		t := tasks[index]
		if !git.IsRepo(t.RepoPath) {
			return progressTickMsg{index: index, err: fmt.Errorf("%s is not a git repository", t.RepoPath)}
		}
		err := git.AddProjectWorktree(t.RepoPath, t.Dest, t.Branch, t.BaseBranch)
		return progressTickMsg{index: index + 1, err: err}
	}
}

func (m progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.done && (msg.String() == "enter" || msg.String() == "q" || msg.String() == "ctrl+c") {
			return m, tea.Quit
		}

	case progressTickMsg:
		if msg.err != nil {
			failed := m.tasks[m.index]
			m.err = fmt.Errorf("%s: %w", failed.Folder, msg.err)
			if m.manifest != nil {
				m.manifest.SetStatus(failed.ID, project.StatusFailed, msg.err.Error())
				_ = m.manifest.Save(m.projectDir)
			}
			m.done = true
			return m, tea.Quit
		}
		if msg.index > 0 && msg.index <= len(m.tasks) {
			t := m.tasks[msg.index-1]
			m.log = append(m.log, okStyle.Render("✓")+" "+t.Folder+
				lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
					" ← "+t.Branch+" (from "+t.BaseBranch+")",
				))
			if m.manifest != nil {
				m.manifest.SetStatus(t.ID, project.StatusReady, "")
				_ = m.manifest.Save(m.projectDir)
			}
		}
		m.index = msg.index
		if m.index >= len(m.tasks) {
			m.done = true
			return m, nil
		}
		return m, runCreateTask(m.index, m.tasks)

	case spinner.TickMsg:
		if !m.done {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m progressModel) View() string {
	var b strings.Builder
	b.WriteString(Title("creating project"))
	b.WriteString("\n")
	b.WriteString(subtitleStyle.Render(m.projectDir))
	b.WriteString("\n\n")

	for _, line := range m.log {
		b.WriteString(line)
		b.WriteString("\n")
	}

	if m.err != nil {
		b.WriteString("\n")
		b.WriteString(errStyle.Render("✗ " + m.err.Error()))
	} else if !m.done && m.index < len(m.tasks) {
		t := m.tasks[m.index]
		b.WriteString("\n")
		b.WriteString(m.spinner.View())
		b.WriteString(" ")
		b.WriteString(labelStyle.Render(t.Folder))
		b.WriteString(descStyle.Render(" (" + t.Branch + " from " + t.BaseBranch + ")"))
	} else if m.done && m.err == nil {
		b.WriteString("\n")
		b.WriteString(okStyle.Render(fmt.Sprintf("✓ %d worktrees ready", len(m.tasks))))
		b.WriteString("\n\n")
		b.WriteString(hintStyle.Render("enter/q = close"))
	}

	return Box(b.String())
}
