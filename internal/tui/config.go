package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/pershin-daniil/goworktree/internal/config"
	"github.com/pershin-daniil/goworktree/internal/project"
)

func FormatConfig(cfg *config.Config, path string) string {
	var b strings.Builder

	b.WriteString(Title("goworktree config"))
	b.WriteString("\n")
	b.WriteString(subtitleStyle.Render(path))
	b.WriteString("\n\n")

	rows := [][]string{
		{"default_program", programLabel(cfg, cfg.DefaultProgram)},
		{"repos", cfg.ReposRoot},
		{"projects", cfg.ProjectsRoot},
		{"branch", cfg.DefaultBranch},
		{"scan_depth", fmt.Sprintf("%d", cfg.ScanDepth)},
	}
	if len(cfg.OpenWith) > 0 {
		b.WriteString("\n")
		b.WriteString(labelStyle.Render("open with"))
		b.WriteString("\n")
		for _, id := range cfg.ProgramIDs() {
			p, _ := cfg.Program(id)
			state := "disabled"
			if p.Enabled {
				state = "enabled"
			}
			if cfg.DefaultProgram == id {
				state += ", default"
			}
			b.WriteString(fmt.Sprintf("  %s  %s  %s\n", id, p.Name, p.Path+" ("+state+")"))
		}
	}
	for _, row := range rows {
		b.WriteString(fmt.Sprintf("  %s  %s\n",
			labelStyle.Width(12).Render(row[0]),
			row[1],
		))
	}

	names := cfg.RepoNames()
	sort.Strings(names)
	if len(names) > 0 {
		b.WriteString("\n")
		b.WriteString(labelStyle.Render(fmt.Sprintf("repositories (%d)", len(names))))
		b.WriteString("\n")
		for _, id := range names {
			repo := cfg.Repos[id]
			branch := repo.DefaultBranch
			if branch == "" {
				branch = cfg.DefaultBranch
			}
			alias := cfg.DisplayName(id)
			b.WriteString(fmt.Sprintf("  %s\n    %s  %s\n",
				lipgloss.NewStyle().Bold(true).Render(alias),
				lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(repo.Path),
				lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("["+branch+"]"),
			))
		}
	}

	return b.String()
}

func FormatProjects(projects []project.Entry, root string) string {
	var b strings.Builder
	b.WriteString(Title("goworktree projects"))
	b.WriteString("\n")
	b.WriteString(subtitleStyle.Render(root))
	b.WriteString("\n\n")

	for _, p := range projects {
		b.WriteString(labelStyle.Render(p.Name))
		b.WriteString("  ")
		b.WriteString(descStyle.Render(fmt.Sprintf("%d repos", p.Repos)))
		if p.Manifest != nil {
			ready := p.Manifest.ReadyCount()
			if ready < len(p.Manifest.Repos) {
				b.WriteString("  ")
				b.WriteString(errStyle.Render(fmt.Sprintf("(%d/%d ready)", ready, len(p.Manifest.Repos))))
			}
		}
		b.WriteString("\n")
		for _, wt := range p.Worktrees {
			branch := wt.Branch
			if branch == "" {
				branch = "?"
			}
			status := ""
			if wt.Status != "" && wt.Status != project.StatusReady {
				status = " " + errStyle.Render(wt.Status)
			}
			b.WriteString(fmt.Sprintf("  %-30s %s%s\n",
				wt.Name,
				lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("["+branch+"]"),
				status,
			))
		}
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}
