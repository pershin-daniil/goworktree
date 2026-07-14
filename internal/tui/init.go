package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pershin-daniil/goworktree/internal/config"
	"github.com/pershin-daniil/goworktree/internal/git"
)

type initPhase int

const (
	phaseConfirm initPhase = iota
	phaseForm
	phaseScan
	phaseDone
)

type field struct {
	label string
	desc  string
	input textinput.Model
}

type initModel struct {
	phase      initPhase
	fields     []field
	focus      int
	spinner    spinner.Model
	cfg        *config.Config
	repoCount  int
	configPath string
	err        error
	quitting   bool
}

type scanDoneMsg struct {
	repos []git.ScannedRepo
	err   error
}

func RunInit() error {
	cfg := config.Default()
	if cfg.Exists() {
		if existing, err := config.Load(); err == nil {
			cfg = existing
		}
	}

	m := newInitModel(cfg, cfg.Exists())
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return err
	}
	if m, ok := final.(initModel); ok && m.err != nil {
		return m.err
	}
	if m, ok := final.(initModel); ok && m.quitting && m.phase == phaseConfirm {
		fmt.Println("cancelled")
	}
	return nil
}

func newInitModel(cfg *config.Config, needsConfirm bool) initModel {
	phase := phaseForm
	if needsConfirm {
		phase = phaseConfirm
	}

	defs := []struct{ label, desc, value string }{
		{"Cursor executable", "Path to Cursor or command from PATH", cfg.CursorPath},
		{"GoLand executable", "Path to GoLand or command from PATH", cfg.GolandPath},
		{"Repositories root", "Directory with your git repositories", cfg.ReposRoot},
		{"Projects root", "Where worktree projects are created", cfg.ProjectsRoot},
		{"Default branch", "Fallback branch for repositories", cfg.DefaultBranch},
	}

	fields := make([]field, len(defs))
	for i, d := range defs {
		ti := textinput.New()
		ti.Placeholder = d.value
		ti.SetValue(d.value)
		ti.CharLimit = 512
		ti.Width = 50
		ti.Prompt = "› "
		if i == 0 && phase == phaseForm {
			ti.Focus()
		}
		fields[i] = field{label: d.label, desc: d.desc, input: ti}
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return initModel{
		phase:   phase,
		fields:  fields,
		cfg:     cfg,
		spinner: sp,
	}
}

func (m initModel) Init() tea.Cmd {
	if m.phase == phaseScan {
		return tea.Batch(m.spinner.Tick, scanRepos(m.cfg.ReposRoot, m.cfg.ScanDepth))
	}
	return textinput.Blink
}

func scanRepos(root string, depth int) tea.Cmd {
	return func() tea.Msg {
		repos, err := git.ScanRepos(root, depth)
		return scanDoneMsg{repos: repos, err: err}
	}
}

func (m initModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch m.phase {
		case phaseConfirm:
			switch msg.String() {
			case "ctrl+c", "q", "n", "esc":
				m.quitting = true
				return m, tea.Quit
			case "y", "enter":
				m.phase = phaseForm
				m.fields[0].input.Focus()
				return m, textinput.Blink
			}

		case phaseForm:
			switch msg.String() {
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			case "tab", "down":
				m.fields[m.focus].input.Blur()
				m.focus = (m.focus + 1) % len(m.fields)
				m.fields[m.focus].input.Focus()
				return m, textinput.Blink
			case "shift+tab", "up":
				m.fields[m.focus].input.Blur()
				m.focus = (m.focus + len(m.fields) - 1) % len(m.fields)
				m.fields[m.focus].input.Focus()
				return m, textinput.Blink
			case "enter":
				if m.focus < len(m.fields)-1 {
					m.fields[m.focus].input.Blur()
					m.focus++
					m.fields[m.focus].input.Focus()
					return m, textinput.Blink
				}
				m.applyForm()
				m.phase = phaseScan
				return m, tea.Batch(m.spinner.Tick, scanRepos(m.cfg.ReposRoot, m.cfg.ScanDepth))
			}

		case phaseDone:
			if msg.String() == "ctrl+c" || msg.String() == "q" || msg.String() == "enter" {
				return m, tea.Quit
			}
		}

	case scanDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.phase = phaseDone
			return m, nil
		}
		m.cfg.Repos = map[string]config.Repo{}
		for _, r := range msg.repos {
			id := config.RepoIDFromPath(m.cfg.ReposRoot, r.Path)
			branch, err := git.DefaultBranch(r.Path)
			if err != nil {
				branch = m.cfg.DefaultBranch
			}
			m.cfg.Repos[id] = config.Repo{
				Path:          r.Path,
				DefaultBranch: branch,
				Alias:         r.Alias,
			}
		}
		m.repoCount = len(msg.repos)
		if err := m.cfg.Save(); err != nil {
			m.err = err
		} else {
			m.configPath, _ = config.Path()
		}
		m.phase = phaseDone
		return m, nil

	case spinner.TickMsg:
		if m.phase == phaseScan {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	}

	if m.phase == phaseForm {
		var cmd tea.Cmd
		m.fields[m.focus].input, cmd = m.fields[m.focus].input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *initModel) applyForm() {
	keys := []func(string){
		func(v string) { m.cfg.CursorPath = v },
		func(v string) { m.cfg.GolandPath = v },
		func(v string) { m.cfg.ReposRoot = v },
		func(v string) { m.cfg.ProjectsRoot = v },
		func(v string) { m.cfg.DefaultBranch = v },
	}
	for i, f := range m.fields {
		v := strings.TrimSpace(f.input.Value())
		if v == "" {
			v = f.input.Placeholder
		}
		keys[i](v)
	}
	if m.cfg.DefaultBranch == "" {
		m.cfg.DefaultBranch = "main"
	}
	if m.cfg.ScanDepth <= 0 {
		m.cfg.ScanDepth = config.DefaultScanDepth
	}
	if m.cfg.Repos == nil {
		m.cfg.Repos = map[string]config.Repo{}
	}
}

func (m initModel) View() string {
	var b strings.Builder

	switch m.phase {
	case phaseConfirm:
		b.WriteString(Box(Title("goworktree") + "\n\n" +
			"Config already exists.\nOverwrite with new settings?\n\n" +
			hintStyle.Render("y/enter = yes   n/q/esc = cancel")))

	case phaseForm:
		b.WriteString(Title("goworktree setup"))
		b.WriteString("\n")
		b.WriteString(subtitleStyle.Render("Configure paths and defaults"))
		b.WriteString("\n\n")

		for i, f := range m.fields {
			style := labelStyle
			if i == m.focus {
				style = style.Underline(true)
			}
			b.WriteString(style.Render(f.label))
			b.WriteString("\n")
			b.WriteString(descStyle.Render(f.desc))
			b.WriteString("\n")
			b.WriteString(f.input.View())
			b.WriteString("\n\n")
		}
		b.WriteString(hintStyle.Render("tab/shift+tab = navigate   enter = next/save   ctrl+c = quit"))

	case phaseScan:
		b.WriteString(Box(Title("scanning repositories") + "\n\n" + m.spinner.View()))

	case phaseDone:
		if m.err != nil {
			b.WriteString(Box(Title("setup failed") + "\n\n" + errStyle.Render(m.err.Error())))
		} else {
			content := fmt.Sprintf(
				"%s\n\n%s\n%s\n\n%s",
				Title("setup complete"),
				okStyle.Render(fmt.Sprintf("✓ found %d repositories", m.repoCount)),
				lipgloss.NewStyle().Render("config: "+m.configPath),
				hintStyle.Render("next: goworktree start <project-name>"),
			)
			b.WriteString(Box(content))
			b.WriteString("\n")
			b.WriteString(hintStyle.Render("enter/q = close"))
		}
	}

	return b.String()
}
