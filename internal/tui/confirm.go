package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type confirmModel struct {
	message string
	ok      bool
}

// Confirm asks y/n. Cancel (n/esc/q) returns (false, nil); callers that want
// uniform soft-cancel should map false → ErrCancelled (see drop/remove).
func Confirm(message string) (bool, error) {
	m := confirmModel{message: message}
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return false, err
	}
	return final.(confirmModel).ok, nil
}

func (m confirmModel) Init() tea.Cmd { return nil }

func (m confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "y", "Y", "enter":
			m.ok = true
			return m, tea.Quit
		case "n", "N", "esc", "q", "ctrl+c":
			m.ok = false
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m confirmModel) View() string {
	var b strings.Builder
	b.WriteString(Title("confirm"))
	b.WriteString("\n\n")
	b.WriteString(m.message)
	b.WriteString("\n\n")
	b.WriteString(hintStyle.Render("y/enter = yes   n/esc = cancel"))
	return Box(b.String())
}
