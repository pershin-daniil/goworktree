package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type askModel struct {
	input    textinput.Model
	title    string
	subtitle string
	value    string
	cancel   bool
}

func AskProjectName() (string, error) {
	m := askModel{
		title:    "new project",
		subtitle: "Name for the worktree project folder",
	}
	m.input = textinput.New()
	m.input.Placeholder = "my-feature"
	m.input.Focus()
	m.input.CharLimit = 64
	m.input.Width = 40
	m.input.Prompt = "› "

	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return "", err
	}
	m = final.(askModel)
	if m.cancel {
		return "", ErrCancelled
	}
	name := strings.TrimSpace(m.value)
	if name == "" {
		name = m.input.Placeholder
	}
	return name, nil
}

func (m askModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m askModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancel = true
			return m, tea.Quit
		case "enter":
			m.value = m.input.Value()
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m askModel) View() string {
	var b strings.Builder
	b.WriteString(Title("goworktree"))
	b.WriteString("\n")
	b.WriteString(subtitleStyle.Render(m.subtitle))
	b.WriteString("\n\n")
	b.WriteString(labelStyle.Render(m.title))
	b.WriteString("\n")
	b.WriteString(m.input.View())
	b.WriteString("\n\n")
	b.WriteString(hintStyle.Render("enter = confirm   esc = cancel"))
	return Box(b.String())
}
