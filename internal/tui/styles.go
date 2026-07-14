package tui

import (
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			MarginBottom(1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("63")).
			Bold(true)

	descStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Italic(true)

	hintStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			MarginTop(1)

	okStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("42"))

	errStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("196"))

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 2)
)

func Title(text string) string {
	return titleStyle.Render(text)
}

func Box(content string) string {
	return boxStyle.Render(content)
}
