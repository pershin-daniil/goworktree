package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/pershin-daniil/goworktree/internal/workflow/changework"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
)

func (m *workAppModel) beginRepositoryChange(kind changework.Kind) {
	m.changeKind = kind
	m.changeMode = newwork.ModeOnline
	m.changeDeleteBranches = false
	m.changePlan = changework.Plan{}
	m.selectedChangeRepos = make(map[string]bool)
	if kind == changework.KindRemove && m.actionReturn == workRepository && m.selectedRepoID != "" {
		m.selectedChangeRepos[m.selectedRepoID] = true
	}
	m.actionNotice = ""
	m.setRepositoryChangeItems(0)
}

func (m *workAppModel) setRepositoryChangeItems(selected int) {
	active := make(map[string]bool)
	if entry := m.currentWork(); entry != nil && entry.Snapshot != nil {
		for _, repository := range entry.Snapshot.Repositories {
			active[repository.ID] = true
		}
	}
	items := make([]list.Item, 0)
	if m.changeKind == changework.KindAdd {
		for index, repository := range m.actions.Repositories {
			if active[repository.ID] {
				continue
			}
			items = append(items, changeRepositoryItem(repository.ID, repository.Name, repository.Path, index, m.selectedChangeRepos[repository.ID]))
		}
	} else if entry := m.currentWork(); entry != nil && entry.Snapshot != nil {
		for index, repository := range entry.Snapshot.Repositories {
			items = append(items, changeRepositoryItem(repository.ID, repository.ID, repository.Intent.Destination, index, m.selectedChangeRepos[repository.ID]))
		}
	}
	m.setList(items)
	if len(items) > 0 {
		m.list.Select(min(selected, len(items)-1))
	} else {
		m.actionNotice = "No eligible repositories"
	}
	m.screen = workChangeRepositories
}

func changeRepositoryItem(id, name, path string, index int, selected bool) workItem {
	mark := "○"
	if selected {
		mark = "✓"
	}
	if name == "" {
		name = id
	}
	return workItem{
		kind: workItemNewRepository, index: index, id: id,
		title: mark + " " + name, desc: id + " · " + path,
	}
}

func (m workAppModel) updateRepositoryChangeRepositories(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "h", "esc":
		m.restoreActionReturn()
		return m, nil
	case " ":
		item, ok := m.list.SelectedItem().(workItem)
		if !ok {
			return m, nil
		}
		m.selectedChangeRepos[item.id] = !m.selectedChangeRepos[item.id]
		m.actionNotice = ""
		m.setRepositoryChangeItems(m.list.Index())
		return m, nil
	case "m":
		if m.changeKind == changework.KindAdd {
			if m.changeMode == newwork.ModeOnline {
				m.changeMode = newwork.ModeOffline
			} else {
				m.changeMode = newwork.ModeOnline
			}
		}
		return m, nil
	case "d":
		if m.changeKind == changework.KindRemove {
			m.changeDeleteBranches = !m.changeDeleteBranches
		}
		return m, nil
	case "enter", "l":
		if len(m.selectedChangeRepositoryIDs()) == 0 {
			m.actionNotice = "Select at least one repository with Space"
			return m, nil
		}
		return m.startRepositoryChangePlanning()
	case "q":
		m.cancelLoad()
		m.quitting = true
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m workAppModel) startRepositoryChangePlanning() (tea.Model, tea.Cmd) {
	ids := m.selectedChangeRepositoryIDs()
	name := m.selectedWorkName
	kind := m.changeKind
	mode := m.changeMode
	deleteBranches := m.changeDeleteBranches
	message := "Verifying clean worktrees, exact branch OIDs, and removal fingerprints…"
	if kind == changework.KindAdd {
		message = "Resolving exact base commits and validating branch and destination ownership…"
	}
	operationCtx, generation := m.beginOperation("plan-repository-change", "Planning repository change · "+name, message)
	return m, func() tea.Msg {
		var plan changework.Plan
		var err error
		if kind == changework.KindAdd {
			if m.actions.PlanAddRepositories == nil {
				err = fmt.Errorf("typed Add repositories workflow is unavailable")
			} else {
				plan, err = m.actions.PlanAddRepositories(operationCtx, changework.AddRequest{
					WorkName: name, RepositoryIDs: ids, Mode: mode,
				})
			}
		} else if m.actions.PlanRemoveRepositories == nil {
			err = fmt.Errorf("typed Remove repositories workflow is unavailable")
		} else {
			plan, err = m.actions.PlanRemoveRepositories(operationCtx, changework.RemoveRequest{
				WorkName: name, RepositoryIDs: ids, DeleteBranches: deleteBranches,
			})
		}
		return repositoryChangePlannedMsg{generation: generation, plan: plan, err: err}
	}
}

func (m workAppModel) handleRepositoryChangePlanned(msg repositoryChangePlannedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || m.operationKind != "plan-repository-change" {
		return m, nil
	}
	m.finishOperation()
	if msg.err != nil {
		m.setActionResult("Repository change plan failed", msg.err.Error(), workChangeRepositories, false)
		return m, nil
	}
	m.changePlan = msg.plan
	m.screen = workChangePlan
	m.setDetail("Repository change plan", formatRepositoryChangePlan(msg.plan))
	return m, nil
}

func (m workAppModel) updateRepositoryChangePlan(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "a":
		if m.changePlan.Kind == changework.KindRemove && removesLocalBranches(m.changePlan) {
			m.input.SetValue("")
			m.input.Focus()
			m.actionNotice = ""
			m.screen = workChangeConfirm
			return m, nil
		}
		return m.startRunRepositoryChange()
	case "h", "esc":
		m.setRepositoryChangeItems(0)
		return m, nil
	case "q":
		m.cancelLoad()
		m.quitting = true
		return m, tea.Quit
	}
	m.scrollDetail(msg)
	return m, nil
}

func (m workAppModel) updateRepositoryChangeConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.input.Blur()
		m.screen = workChangePlan
		return m, nil
	case "enter":
		if m.input.Value() != m.selectedWorkName {
			m.actionNotice = "Confirmation must exactly equal " + m.selectedWorkName
			return m, nil
		}
		m.input.Blur()
		return m.startRunRepositoryChange()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.actionNotice = ""
	return m, cmd
}

func (m workAppModel) startRunRepositoryChange() (tea.Model, tea.Cmd) {
	if m.actions.RunRepositoryChange == nil {
		m.setActionResult("Repository change unavailable", "Typed repository change execution is unavailable.", workChangePlan, false)
		return m, nil
	}
	plan := m.changePlan
	operationCtx, generation := m.beginOperation("run-repository-change", "Changing repositories · "+plan.WorkName.String(), "Recording intent, applying exact Git mutations, updating go.work, and publishing the new manifest revision…")
	return m, func() tea.Msg {
		result, err := m.actions.RunRepositoryChange(operationCtx, plan)
		return repositoryChangeCompletedMsg{generation: generation, result: result, err: err}
	}
}

func (m workAppModel) startResumeRepositoryChange() (tea.Model, tea.Cmd) {
	entry := m.currentWork()
	if entry == nil || entry.Snapshot == nil || m.actions.ResumeRepositoryChange == nil {
		m.actionNotice = "Unavailable: typed Resume repository change workflow is unavailable"
		return m, nil
	}
	path := entry.Snapshot.ChangeOperation.Path
	operationCtx, generation := m.beginOperation("resume-repository-change", "Resuming repository change · "+m.selectedWorkName, "Reconciling recorded checkpoints with Git and filesystem state before continuing…")
	return m, func() tea.Msg {
		result, err := m.actions.ResumeRepositoryChange(operationCtx, path)
		return repositoryChangeCompletedMsg{generation: generation, result: result, err: err}
	}
}

func (m workAppModel) handleRepositoryChangeCompleted(msg repositoryChangeCompletedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID ||
		(m.operationKind != "run-repository-change" && m.operationKind != "resume-repository-change") {
		return m, nil
	}
	m.finishOperation()
	title := "Work repositories updated"
	content := formatRepositoryChangeResult(msg.result)
	if msg.err != nil {
		title = "Repository change needs attention"
		content = msg.err.Error() + "\n\n" + content
	}
	m.setActionResult(title, content, workOverview, true)
	return m, nil
}

func (m workAppModel) repositoryChangeRepositoriesView() string {
	title := "Add repositories"
	contextLine := fmt.Sprintf("Work · %s · mode %s · %d selected", m.selectedWorkName, m.changeMode, len(m.selectedChangeRepositoryIDs()))
	hint := "space toggle  m mode  enter plan  h/esc back  / search  q quit"
	if m.changeKind == changework.KindRemove {
		title = "Remove repositories"
		branchMode := "keep local branches"
		if m.changeDeleteBranches {
			branchMode = "delete local branches"
		}
		contextLine = fmt.Sprintf("Work · %s · %s · %d selected", m.selectedWorkName, branchMode, len(m.selectedChangeRepositoryIDs()))
		hint = "space toggle  d branch policy  enter plan  h/esc back  / search  q quit"
	}
	if m.actionNotice != "" {
		contextLine += " · " + m.actionNotice
	}
	return m.listView(title, contextLine, hint)
}

func (m workAppModel) repositoryChangeConfirmView() string {
	message := "Type " + m.selectedWorkName + " exactly to delete the listed LOCAL branches. Remotes are untouched."
	if m.actionNotice != "" {
		message = m.actionNotice
	}
	return Title("Remove repositories · confirm branches") + "\n" + subtitleStyle.Render(message) + "\n\n" + m.input.View() + "\n\n" + hintStyle.Render("enter apply  esc cancel")
}

func (m workAppModel) selectedChangeRepositoryIDs() []string {
	ids := make([]string, 0, len(m.selectedChangeRepos))
	for id, selected := range m.selectedChangeRepos {
		if selected {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func removesLocalBranches(plan changework.Plan) bool {
	for _, repository := range plan.Repositories {
		if repository.DeleteBranch {
			return true
		}
	}
	return false
}

func formatRepositoryChangePlan(plan changework.Plan) string {
	lines := []string{
		factLine("Work", plan.WorkName.String()), factLine("Action", string(plan.Kind)),
		factLine("Revision", fmt.Sprintf("%d → %d", plan.Before.Revision, plan.After.Revision)),
		factLine("Repositories", fmt.Sprint(len(plan.Repositories))), factLine("Remote mutation", "none"),
	}
	if plan.Kind == changework.KindAdd {
		lines = append(lines, factLine("Mode", string(plan.Mode)))
	}
	for _, repository := range plan.Repositories {
		action := "create branch and worktree"
		if repository.Reattach {
			action = "reattach retained local branch"
		}
		if plan.Kind == changework.KindRemove {
			action = "remove worktree; keep local branch"
			if repository.DeleteBranch {
				action = "remove worktree; delete exact local branch"
			}
		}
		lines = append(lines, "", repository.ID,
			"  action: "+action,
			"  branch: "+repository.BranchRef+" @ "+shortOID(repository.BranchOID),
			"  destination: "+repository.Destination,
		)
	}
	return strings.Join(lines, "\n")
}

func formatRepositoryChangeResult(result changework.Result) string {
	if result.WorkName == "" {
		return "No repository checkpoint was completed."
	}
	lines := []string{
		factLine("Work", result.WorkName), factLine("Action", string(result.Kind)),
		factLine("Manifest revision", fmt.Sprint(result.Revision)), factLine("Operation record", result.OperationPath),
	}
	for _, repository := range result.Repositories {
		status := repository.Status
		if repository.Err != nil {
			status += ": " + repository.Err.Error()
		}
		lines = append(lines, "", repository.ID, "  "+status)
	}
	return strings.Join(lines, "\n")
}
