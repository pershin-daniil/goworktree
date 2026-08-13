package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/removework"
	"github.com/pershin-daniil/goworktree/internal/workflow/syncwork"
)

const (
	actionNewWork = "new-work"
	actionRefresh = "refresh"
	actionResume  = "resume-new-work"
	actionSync    = "sync-work"
	actionRepair  = "repair-work"
	actionRemove  = "remove-work"
	openPrefix    = "open:"
)

func (m workAppModel) isListScreen() bool {
	switch m.screen {
	case workHome, workOverview, workActions, workNewRepositories:
		return true
	default:
		return false
	}
}

func (m workAppModel) canOpenActions() bool {
	switch m.screen {
	case workHome, workOverview, workRepository, workProblem:
		return true
	default:
		return false
	}
}

func (m workAppModel) canRefresh() bool {
	switch m.screen {
	case workHome, workOverview, workRepository, workProblem, workLoadError:
		return true
	default:
		return false
	}
}

func (m *workAppModel) openActionPalette() {
	m.captureSelection()
	m.actionReturn = m.screen
	m.actionNotice = ""
	items := make([]listItem, 0)
	if m.selectedWorkName == "" || m.screen == workHome {
		reason := ""
		switch {
		case m.actions.PlanNewWork == nil || m.actions.CreateNewWork == nil:
			reason = "typed New Work workflow is unavailable"
		case len(m.actions.Repositories) == 0:
			reason = "no configured repositories are available"
		}
		items = append(items, listItem{actionNewWork, "New Work", "Create branches, worktrees, and go.work", reason})
	} else {
		if entry := m.currentWork(); entry != nil && entry.Snapshot != nil && entry.Snapshot.Operation.ResumeSuggested {
			reason := ""
			if m.actions.ResumeNewWork == nil {
				reason = "typed Resume New Work workflow is unavailable"
			}
			items = append(items, listItem{actionResume, "Resume New Work", "Continue the recorded incomplete operation", reason})
		}
		for _, program := range m.actions.Programs {
			title := "Open in " + program.Name
			if program.Default {
				title = "Open Work · " + program.Name
			}
			reason := ""
			if m.actions.OpenWork == nil {
				reason = "Open Work action is unavailable"
			}
			items = append(items, listItem{openPrefix + program.ID, title, "Launch the Work root", reason})
		}
		if len(m.actions.Programs) == 0 {
			items = append(items, listItem{openPrefix, "Open Work", "No enabled programs", "configure an enabled Open with program"})
		}
		syncBlocked := ""
		if m.actions.PlanSyncWork == nil || m.actions.RunSyncWork == nil {
			syncBlocked = "typed Sync Work workflow is unavailable"
		}
		removeBlocked := ""
		if m.actions.PlanRemoveWork == nil || m.actions.RunRemoveWork == nil {
			removeBlocked = "typed Remove Work workflow is unavailable"
		}
		items = append(items,
			listItem{actionSync, "Sync Work", "Fetch and rebase every Work repository", syncBlocked},
			listItem{actionRepair, "Repair Work", "Reconcile manifest, Git, and filesystem state", "typed Repair Work workflow is not implemented"},
			listItem{actionRemove, "Remove Work", "Show a destructive plan and remove the Work", removeBlocked},
		)
	}
	items = append(items, listItem{actionRefresh, "Refresh", "Reinspect Git and filesystem state", ""})

	listItems := make([]list.Item, 0, len(items))
	for _, item := range items {
		listItems = append(listItems, workItem{
			kind: workItemAction, id: item.id, title: item.title,
			desc: actionDescription(item.description, item.blockedReason), blockedReason: item.blockedReason,
		})
	}
	m.setList(listItems)
	m.screen = workActions
}

type listItem struct {
	id            string
	title         string
	description   string
	blockedReason string
}

func actionDescription(description, blockedReason string) string {
	if blockedReason == "" {
		return description
	}
	return description + " · unavailable: " + blockedReason
}

func (m workAppModel) updateActionPalette(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "h", "esc":
		m.restoreActionReturn()
		return m, nil
	case "enter", "l":
		return m.activateAction()
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m workAppModel) activateAction() (tea.Model, tea.Cmd) {
	item, ok := m.list.SelectedItem().(workItem)
	if !ok || item.kind != workItemAction {
		return m, nil
	}
	if item.blockedReason != "" {
		m.actionNotice = "Unavailable: " + item.blockedReason
		return m, nil
	}
	switch {
	case item.id == actionNewWork:
		m.beginNewWork()
		return m, nil
	case item.id == actionRefresh:
		return m.refreshFromAction()
	case item.id == actionResume:
		return m.startResumeNewWork()
	case item.id == actionSync:
		return m.startSyncWorkPlanning()
	case item.id == actionRemove:
		return m.startRemoveWorkPlanning()
	case strings.HasPrefix(item.id, openPrefix):
		return m.startOpenWork(strings.TrimPrefix(item.id, openPrefix))
	default:
		m.actionNotice = "Unavailable: action is not connected"
		return m, nil
	}
}

func (m *workAppModel) restoreActionReturn() {
	switch m.actionReturn {
	case workOverview:
		m.setWork(m.selectedWorkName, m.selectedRepoID)
	case workRepository:
		if m.setWork(m.selectedWorkName, m.selectedRepoID) {
			m.openRepository(m.selectedRepoID)
		}
	case workProblem:
		m.screen = workProblem
	default:
		m.setHome(m.selectedWorkName)
	}
}

func (m workAppModel) refreshFromAction() (tea.Model, tea.Cmd) {
	m.restoreScreen = m.actionReturn
	if m.restoreScreen == workProblem {
		m.restoreScreen = workOverview
	}
	m.screen = workLoading
	m.generation++
	return m, m.loadCommand(m.generation)
}

func (m *workAppModel) beginNewWork() {
	m.input.SetValue("")
	m.input.Focus()
	m.actionNotice = ""
	m.selectedNewRepos = make(map[string]bool)
	m.newWorkMode = newwork.ModeOnline
	m.newWorkPlan = newwork.Plan{}
	m.screen = workNewName
}

func (m workAppModel) updateNewWorkName(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.input.Blur()
		m.setHome(m.selectedWorkName)
		return m, nil
	case "enter":
		if m.input.Value() == "" {
			m.actionNotice = "Work name is required"
			return m, nil
		}
		m.input.Blur()
		m.setNewWorkRepositories(0)
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.actionNotice = ""
	return m, cmd
}

func (m *workAppModel) setNewWorkRepositories(selected int) {
	items := make([]list.Item, 0, len(m.actions.Repositories))
	for index, repository := range m.actions.Repositories {
		mark := "○"
		if m.selectedNewRepos[repository.ID] {
			mark = "✓"
		}
		name := repository.Name
		if name == "" {
			name = repository.ID
		}
		items = append(items, workItem{
			kind: workItemNewRepository, index: index, id: repository.ID,
			title: mark + " " + name, desc: repository.ID + " · " + repository.Path,
		})
	}
	m.setList(items)
	if len(items) > 0 {
		m.list.Select(min(max(0, selected), len(items)-1))
	}
	m.screen = workNewRepositories
}

func (m workAppModel) updateNewWorkRepositories(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "h", "esc":
		m.input.Focus()
		m.screen = workNewName
		return m, nil
	case " ":
		item, ok := m.list.SelectedItem().(workItem)
		if ok && item.kind == workItemNewRepository {
			m.selectedNewRepos[item.id] = !m.selectedNewRepos[item.id]
			m.actionNotice = ""
			selected := m.list.Index()
			m.setNewWorkRepositories(selected)
		}
		return m, nil
	case "m":
		if m.newWorkMode == newwork.ModeOnline {
			m.newWorkMode = newwork.ModeOffline
		} else {
			m.newWorkMode = newwork.ModeOnline
		}
		m.actionNotice = ""
		selected := m.list.Index()
		m.setNewWorkRepositories(selected)
		return m, nil
	case "enter", "l":
		if len(m.selectedRepositoryIDs()) == 0 {
			m.actionNotice = "Select at least one repository with Space"
			return m, nil
		}
		return m.startNewWorkPlanning()
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m workAppModel) selectedRepositoryIDs() []string {
	ids := make([]string, 0, len(m.selectedNewRepos))
	for id, selected := range m.selectedNewRepos {
		if selected {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (m workAppModel) startNewWorkPlanning() (tea.Model, tea.Cmd) {
	if m.actions.PlanNewWork == nil {
		m.setActionResult("New Work unavailable", "Typed New Work planning is unavailable.", workNewRepositories, false)
		return m, nil
	}
	operationCtx, cancel := context.WithCancel(m.ctx)
	m.operationCancel = cancel
	m.operationID++
	generation := m.operationID
	request := newwork.Request{
		Name: m.input.Value(), RepositoryIDs: m.selectedRepositoryIDs(),
		BaseOverrides: map[string]string{}, Mode: m.newWorkMode,
	}
	m.operationKind = "plan-new-work"
	m.operationTitle = "Planning New Work"
	m.operationMessage = planningMessage(m.newWorkMode, len(request.RepositoryIDs))
	m.screen = workOperation
	return m, func() tea.Msg {
		plan, err := m.actions.PlanNewWork(operationCtx, request)
		return newWorkPlannedMsg{generation: generation, plan: plan, err: err}
	}
}

func planningMessage(mode newwork.Mode, repositories int) string {
	if mode == newwork.ModeOnline {
		return fmt.Sprintf("Inspecting %d repositories, fetching configured remotes, and resolving immutable base commits…", repositories)
	}
	return fmt.Sprintf("Inspecting %d repositories and resolving locally known base commits…", repositories)
}

func (m workAppModel) handleNewWorkPlanned(msg newWorkPlannedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || m.operationKind != "plan-new-work" {
		return m, nil
	}
	m.finishOperation()
	if msg.err != nil {
		m.setActionResult("New Work plan failed", msg.err.Error(), workNewRepositories, false)
		return m, nil
	}
	m.newWorkPlan = msg.plan
	m.screen = workNewPlan
	m.setDetail("New Work plan", formatNewWorkPlan(msg.plan))
	return m, nil
}

func (m workAppModel) updateNewWorkPlan(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "c":
		return m.startCreateNewWork()
	case "h", "esc":
		m.setNewWorkRepositories(0)
		return m, nil
	case "q":
		m.cancelLoad()
		m.quitting = true
		return m, tea.Quit
	}
	m.scrollDetail(msg)
	return m, nil
}

func (m workAppModel) startCreateNewWork() (tea.Model, tea.Cmd) {
	if m.actions.CreateNewWork == nil {
		m.setActionResult("New Work unavailable", "Typed New Work execution is unavailable.", workNewPlan, false)
		return m, nil
	}
	operationCtx, cancel := context.WithCancel(m.ctx)
	m.operationCancel = cancel
	m.operationID++
	generation := m.operationID
	plan := m.newWorkPlan
	m.operationKind = "create-new-work"
	m.operationTitle = "Creating Work · " + plan.WorkName.String()
	m.operationMessage = fmt.Sprintf("Creating and verifying %d repository worktrees. Partial verified state is preserved if interrupted…", len(plan.Repositories))
	m.screen = workOperation
	return m, func() tea.Msg {
		result, err := m.actions.CreateNewWork(operationCtx, plan)
		return newWorkCreatedMsg{generation: generation, result: result, err: err}
	}
}

func (m workAppModel) startResumeNewWork() (tea.Model, tea.Cmd) {
	if m.actions.ResumeNewWork == nil {
		m.actionNotice = "Unavailable: typed Resume New Work workflow is unavailable"
		return m, nil
	}
	entry := m.currentWork()
	if entry == nil || entry.Snapshot == nil || !entry.Snapshot.Operation.ResumeSuggested {
		m.actionNotice = "Unavailable: this Work has no resumable New Work operation"
		return m, nil
	}
	operationCtx, cancel := context.WithCancel(m.ctx)
	m.operationCancel = cancel
	m.operationID++
	generation := m.operationID
	path := entry.Snapshot.Operation.Path
	m.operationKind = "resume-new-work"
	m.operationTitle = "Resuming New Work · " + entry.Name
	m.operationMessage = "Reconciling recorded steps and continuing only verified pending work…"
	m.screen = workOperation
	return m, func() tea.Msg {
		result, err := m.actions.ResumeNewWork(operationCtx, path)
		return newWorkCreatedMsg{generation: generation, result: result, err: err}
	}
}

func (m workAppModel) handleNewWorkCreated(msg newWorkCreatedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || (m.operationKind != "create-new-work" && m.operationKind != "resume-new-work") {
		return m, nil
	}
	m.finishOperation()
	workName := m.selectedWorkName
	if m.operationKind == "create-new-work" {
		workName = m.newWorkPlan.WorkName.String()
	}
	m.selectedWorkName = workName
	content := formatNewWorkResult(msg.result)
	title := "Work created"
	if msg.err != nil {
		title = "New Work needs attention"
		content = msg.err.Error() + "\n\n" + content
	}
	returnScreen := workOverview
	if msg.result.WorkRoot == "" && workName == "" {
		returnScreen = workHome
	}
	m.setActionResult(title, content, returnScreen, true)
	return m, nil
}

func (m workAppModel) startOpenWork(programID string) (tea.Model, tea.Cmd) {
	if m.actions.OpenWork == nil {
		m.actionNotice = "Unavailable: Open Work action is unavailable"
		return m, nil
	}
	entry := m.currentWork()
	if entry == nil {
		m.actionNotice = "Unavailable: no Work is selected"
		return m, nil
	}
	programName := programID
	for _, program := range m.actions.Programs {
		if program.ID == programID {
			programName = program.Name
			break
		}
	}
	operationCtx, cancel := context.WithCancel(m.ctx)
	m.operationCancel = cancel
	m.operationID++
	generation := m.operationID
	m.operationKind = "open-work"
	m.operationTitle = "Opening Work · " + programName
	m.operationMessage = entry.RootPath
	m.screen = workOperation
	request := WorkOpenRequest{WorkName: entry.Name, WorkRoot: entry.RootPath, Program: programID}
	return m, func() tea.Msg {
		err := m.actions.OpenWork(operationCtx, request)
		return workOpenedMsg{generation: generation, program: programName, err: err}
	}
}

func (m workAppModel) handleWorkOpened(msg workOpenedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || m.operationKind != "open-work" {
		return m, nil
	}
	m.finishOperation()
	if msg.err != nil {
		m.setActionResult("Open Work failed", msg.err.Error(), workOverview, false)
		return m, nil
	}
	m.setActionResult("Work opened", fmt.Sprintf("%s was started for Work %s.", msg.program, m.selectedWorkName), workOverview, false)
	return m, nil
}

func (m *workAppModel) setActionResult(title, content string, returnScreen workScreen, refresh bool) {
	m.screen = workActionResult
	m.resultReturn = returnScreen
	m.resultRefresh = refresh
	m.setDetail(title, content)
}

func (m *workAppModel) finishOperation() {
	if m.operationCancel != nil {
		m.operationCancel()
		m.operationCancel = nil
	}
}

func (m workAppModel) updateActionResult(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "h", "esc":
		if m.resultRefresh {
			m.restoreScreen = m.resultReturn
			m.screen = workLoading
			m.generation++
			return m, m.loadCommand(m.generation)
		}
		if m.resultReturn == workNewRepositories {
			m.setNewWorkRepositories(0)
		} else if m.resultReturn == workNewPlan {
			m.screen = workNewPlan
		} else if m.resultReturn == workSyncPlan {
			m.screen = workSyncPlan
		} else if m.resultReturn == workRemovePlan {
			m.screen = workRemovePlan
		} else if m.resultReturn == workOverview && m.setWork(m.selectedWorkName, m.selectedRepoID) {
			// setWork restores the Work overview.
		} else {
			m.setHome(m.selectedWorkName)
		}
		return m, nil
	case "q":
		m.cancelLoad()
		m.quitting = true
		return m, tea.Quit
	}
	m.scrollDetail(msg)
	return m, nil
}

func (m workAppModel) startSyncWorkPlanning() (tea.Model, tea.Cmd) {
	if m.actions.PlanSyncWork == nil || m.selectedWorkName == "" {
		m.actionNotice = "Unavailable: typed Sync Work planning is unavailable"
		return m, nil
	}
	operationCtx, cancel := context.WithCancel(m.ctx)
	m.operationCancel = cancel
	m.operationID++
	generation, name := m.operationID, m.selectedWorkName
	m.operationKind = "plan-sync-work"
	m.operationTitle = "Planning Sync Work · " + name
	m.operationMessage = "Fetching configured remotes and resolving exact base commits. No local branch is changed during planning…"
	m.screen = workOperation
	return m, func() tea.Msg {
		plan, err := m.actions.PlanSyncWork(operationCtx, name)
		return syncWorkPlannedMsg{generation: generation, plan: plan, err: err}
	}
}

func (m workAppModel) handleSyncWorkPlanned(msg syncWorkPlannedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || m.operationKind != "plan-sync-work" {
		return m, nil
	}
	m.finishOperation()
	if msg.err != nil {
		m.setActionResult("Sync Work plan failed", msg.err.Error(), workOverview, false)
		return m, nil
	}
	m.syncWorkPlan = msg.plan
	m.screen = workSyncPlan
	m.setDetail("Sync Work plan", formatSyncWorkPlan(msg.plan))
	return m, nil
}

func (m workAppModel) updateSyncWorkPlan(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "s":
		return m.startRunSyncWork()
	case "h", "esc":
		m.setWork(m.selectedWorkName, m.selectedRepoID)
		return m, nil
	case "q":
		m.cancelLoad()
		m.quitting = true
		return m, tea.Quit
	}
	m.scrollDetail(msg)
	return m, nil
}

func (m workAppModel) startRunSyncWork() (tea.Model, tea.Cmd) {
	if m.actions.RunSyncWork == nil {
		m.setActionResult("Sync Work unavailable", "Typed Sync Work execution is unavailable.", workSyncPlan, false)
		return m, nil
	}
	operationCtx, cancel := context.WithCancel(m.ctx)
	m.operationCancel = cancel
	m.operationID++
	generation, plan := m.operationID, m.syncWorkPlan
	m.operationKind = "run-sync-work"
	m.operationTitle = "Syncing Work · " + plan.WorkName
	m.operationMessage = fmt.Sprintf("Revalidating and synchronizing %d repositories. Independent failures do not stop the batch…", len(plan.Repos))
	m.screen = workOperation
	return m, func() tea.Msg {
		result, err := m.actions.RunSyncWork(operationCtx, plan)
		return syncWorkCompletedMsg{generation: generation, result: result, err: err}
	}
}

func (m workAppModel) handleSyncWorkCompleted(msg syncWorkCompletedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || m.operationKind != "run-sync-work" {
		return m, nil
	}
	m.finishOperation()
	title := "Work synchronized"
	content := formatSyncWorkResult(msg.result)
	if msg.err != nil {
		title = "Sync Work failed"
		content = msg.err.Error() + "\n\n" + content
	} else if msg.result.Failed() {
		title = "Sync Work needs attention"
	}
	m.setActionResult(title, content, workOverview, true)
	return m, nil
}

func formatSyncWorkPlan(plan syncwork.Plan) string {
	lines := []string{
		factLine("Work", plan.WorkName), factLine("Repositories", fmt.Sprint(len(plan.Repos))),
		factLine("Remote mutation", "none"), "", "Repository plan",
	}
	for _, repository := range plan.Repos {
		status := string(repository.Relation)
		if repository.BlockedReason != "" {
			status = "blocked: " + repository.BlockedReason
		}
		lines = append(lines, "", repository.ID,
			"  status: "+status,
			"  branch: "+repository.BranchRef+" @ "+syncwork.ShortOID(repository.PreHeadOID),
			"  base: "+repository.BaseRef+" @ "+syncwork.ShortOID(repository.BaseOID),
			fmt.Sprintf("  changes: %d staged, %d unstaged, %d untracked", repository.WorkingTree.Staged, repository.WorkingTree.Unstaged, repository.WorkingTree.Untracked),
		)
	}
	return strings.Join(lines, "\n")
}

func formatSyncWorkResult(result syncwork.Result) string {
	lines := []string{factLine("Work", result.WorkName), factLine("Repositories", fmt.Sprint(len(result.Repositories)))}
	for _, repository := range result.Repositories {
		line := string(repository.Status)
		if repository.Err != nil {
			line += ": " + repository.Err.Error()
		}
		lines = append(lines, "", repository.ID, "  "+line)
	}
	return strings.Join(lines, "\n")
}

func (m workAppModel) startRemoveWorkPlanning() (tea.Model, tea.Cmd) {
	if m.actions.PlanRemoveWork == nil || m.selectedWorkName == "" {
		m.actionNotice = "Unavailable: typed Remove Work planning is unavailable"
		return m, nil
	}
	operationCtx, cancel := context.WithCancel(m.ctx)
	m.operationCancel = cancel
	m.operationID++
	generation, name := m.operationID, m.selectedWorkName
	m.operationKind = "plan-remove-work"
	m.operationTitle = "Planning Remove Work · " + name
	m.operationMessage = "Inspecting exact local worktree, branch, dirty-file, and Work-root deletion targets. No network operation is performed…"
	m.screen = workOperation
	return m, func() tea.Msg {
		plan, err := m.actions.PlanRemoveWork(operationCtx, name)
		return removeWorkPlannedMsg{generation: generation, plan: plan, err: err}
	}
}

func (m workAppModel) handleRemoveWorkPlanned(msg removeWorkPlannedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || m.operationKind != "plan-remove-work" {
		return m, nil
	}
	m.finishOperation()
	if msg.err != nil {
		m.setActionResult("Remove Work plan failed", msg.err.Error(), workOverview, false)
		return m, nil
	}
	m.removeWorkPlan = msg.plan
	m.screen = workRemovePlan
	m.setDetail("Remove Work plan", formatRemoveWorkPlan(msg.plan))
	return m, nil
}

func (m workAppModel) updateRemoveWorkPlan(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "l":
		m.input.SetValue("")
		m.input.Focus()
		m.actionNotice = ""
		m.screen = workRemoveConfirm
		return m, nil
	case "h", "esc":
		m.setWork(m.selectedWorkName, m.selectedRepoID)
		return m, nil
	case "q":
		m.cancelLoad()
		m.quitting = true
		return m, tea.Quit
	}
	m.scrollDetail(msg)
	return m, nil
}

func (m workAppModel) updateRemoveWorkConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.input.Blur()
		m.screen = workRemovePlan
		return m, nil
	case "enter":
		if m.input.Value() != m.removeWorkPlan.WorkName {
			m.actionNotice = fmt.Sprintf("Confirmation must exactly equal %s", m.removeWorkPlan.WorkName)
			return m, nil
		}
		m.input.Blur()
		return m.startRunRemoveWork(m.input.Value())
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.actionNotice = ""
	return m, cmd
}

func (m workAppModel) startRunRemoveWork(confirmation string) (tea.Model, tea.Cmd) {
	if m.actions.RunRemoveWork == nil {
		m.setActionResult("Remove Work unavailable", "Typed Remove Work execution is unavailable.", workRemovePlan, false)
		return m, nil
	}
	operationCtx, cancel := context.WithCancel(m.ctx)
	m.operationCancel = cancel
	m.operationID++
	generation, plan := m.operationID, m.removeWorkPlan
	m.operationKind = "run-remove-work"
	m.operationTitle = "Removing Work · " + plan.WorkName
	m.operationMessage = "Recording removal intent externally, then deleting exact worktrees, local refs, and the confirmed Work root…"
	m.screen = workOperation
	return m, func() tea.Msg {
		result, err := m.actions.RunRemoveWork(operationCtx, plan, confirmation)
		return removeWorkCompletedMsg{generation: generation, result: result, err: err}
	}
}

func (m workAppModel) handleRemoveWorkCompleted(msg removeWorkCompletedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || m.operationKind != "run-remove-work" {
		return m, nil
	}
	m.finishOperation()
	title := "Work removed"
	content := formatRemoveWorkResult(msg.result)
	if msg.err != nil {
		title = "Remove Work needs attention"
		content = msg.err.Error() + "\n\n" + content
	}
	m.selectedWorkName = ""
	m.selectedRepoID = ""
	m.setActionResult(title, content, workHome, true)
	return m, nil
}

func formatRemoveWorkPlan(plan removework.Plan) string {
	lines := []string{
		factLine("Work", plan.WorkName), factLine("Root", plan.WorkRoot),
		factLine("Repositories", fmt.Sprint(len(plan.Repositories))), factLine("Remote mutation", "none"),
		factLine("Recovery record", plan.OperationRecord), "", "This operation is irreversible at product level.",
	}
	for _, repository := range plan.Repositories {
		lines = append(lines, "", repository.ID,
			"  worktree: "+repository.Destination,
			"  delete local ref: "+repository.BranchRef+" @ "+syncwork.ShortOID(repository.BranchOID),
			fmt.Sprintf("  delete changes: %d staged, %d unstaged, %d untracked, %d ignored, %d conflicted", repository.WorkingTree.Staged, repository.WorkingTree.Unstaged, repository.WorkingTree.Untracked, repository.WorkingTree.Ignored, repository.WorkingTree.Conflicted),
		)
	}
	lines = append(lines, "", "Work-root entries")
	for _, entry := range plan.RootEntries {
		lines = append(lines, "  "+entry.Kind+"  "+entry.Name)
	}
	return strings.Join(lines, "\n")
}

func formatRemoveWorkResult(result removework.Result) string {
	lines := []string{factLine("Work", result.WorkName), factLine("Root removed", fmt.Sprint(result.RootRemoved))}
	for _, repository := range result.Repositories {
		line := repository.Status
		if repository.Err != nil {
			line += ": " + repository.Err.Error()
		}
		lines = append(lines, "", repository.ID, "  "+line)
	}
	return strings.Join(lines, "\n")
}

func (m *workAppModel) scrollDetail(msg tea.KeyMsg) {
	switch msg.String() {
	case "j", "down":
		m.viewport.ScrollDown(1)
	case "k", "up":
		m.viewport.ScrollUp(1)
	case "ctrl+d", "pgdown":
		m.viewport.HalfPageDown()
	case "ctrl+u", "pgup":
		m.viewport.HalfPageUp()
	case "g", "home":
		m.viewport.GotoTop()
	case "G", "end":
		m.viewport.GotoBottom()
	}
}

func formatNewWorkPlan(plan newwork.Plan) string {
	lines := []string{
		factLine("Work", plan.WorkName.String()),
		factLine("Mode", string(plan.Mode)),
		factLine("Root", plan.WorkRoot),
		factLine("Repositories", fmt.Sprintf("%d", len(plan.Repositories))),
		factLine("Remote mutation", "none"),
		"",
		"Repository plan",
	}
	for _, repository := range plan.Repositories {
		module := "no root module"
		if repository.IncludeInGoWork {
			module = "included in go.work"
		}
		lines = append(lines,
			"",
			repository.ID,
			"  base: "+repository.BaseRef+" @ "+shortOID(repository.BaseOID),
			"  branch: "+repository.TargetBranchRef,
			"  destination: "+repository.Destination,
			"  harness: "+module,
		)
	}
	return strings.Join(lines, "\n")
}

func formatNewWorkResult(result newwork.ExecutionResult) string {
	root := result.WorkRoot
	if root == "" {
		root = "unknown"
	}
	lines := []string{
		factLine("Status", string(result.Status)),
		factLine("Work root", filepath.Clean(root)),
		factLine("Verified repositories", fmt.Sprintf("%d", len(result.VerifiedRepositories))),
	}
	if result.OperationRecordPath != "" {
		lines = append(lines, factLine("Operation record", result.OperationRecordPath))
	}
	if len(result.VerifiedRepositories) > 0 {
		lines = append(lines, "", "Verified: "+strings.Join(result.VerifiedRepositories, ", "))
	}
	return strings.Join(lines, "\n")
}
