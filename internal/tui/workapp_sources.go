package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/pershin-daniil/goworktree/internal/workflow/sourcerepos"
)

const (
	sourceFilterAll       = ""
	sourceFilterUngrouped = "\x00ungrouped"

	sourceActionFetch       = "source-fetch"
	sourceActionUpdate      = "source-update"
	sourceActionAddGroup    = "source-add-group"
	sourceActionRemoveGroup = "source-remove-group"
	sourceActionRefresh     = "source-refresh"
	sourceActionScan        = "source-scan"
	sourceOpenPrefix        = "source-open:"
	sourceFilterPrefix      = "source-filter:"
)

func (m workAppModel) isSourceScreen() bool {
	switch m.screen {
	case workSourceLoading, workSourceHome, workSourceRepository, workSourceFilter,
		workSourceActions, workSourceUpdatePlan, workSourceGroupInput, workSourceRemoveGroup:
		return true
	default:
		return false
	}
}

func (m workAppModel) beginSourceRepositories() (tea.Model, tea.Cmd) {
	if m.sourceLoaded {
		m.setSourceHome(m.selectedSourceID)
		return m, nil
	}
	return m.beginSourceRefresh(workSourceHome)
}

func (m workAppModel) beginSourceRefresh(returnScreen workScreen) (tea.Model, tea.Cmd) {
	if m.actions.LoadSourceRepos == nil {
		m.setActionResult("Source repositories unavailable", "Source repository inspection is not configured.", returnScreen, false)
		return m, nil
	}
	m.captureSourceSelection()
	m.resultReturn = returnScreen
	m.sourceLoadReturn = returnScreen
	if !m.sourceLoaded {
		m.sourceLoadReturn = workHome
	}
	if m.sourceLoadCancel != nil {
		m.sourceLoadCancel()
	}
	loadCtx, cancel := context.WithCancel(m.ctx)
	m.sourceLoadCancel = cancel
	m.screen = workSourceLoading
	m.generation++
	generation := m.generation
	return m, func() tea.Msg {
		snapshot, err := m.actions.LoadSourceRepos(loadCtx)
		return sourceReposLoadedMsg{generation: generation, snapshot: snapshot, err: err}
	}
}

func (m workAppModel) handleSourceReposLoaded(msg sourceReposLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.generation || m.screen != workSourceLoading {
		return m, nil
	}
	if m.sourceLoadCancel != nil {
		m.sourceLoadCancel()
		m.sourceLoadCancel = nil
	}
	if msg.err != nil {
		m.setActionResult("Source repository inspection failed", msg.err.Error(), m.resultReturn, false)
		return m, nil
	}
	m.sourceSnapshot, m.sourceLoaded = msg.snapshot, true
	m.syncRepositoryOptionsFromSourceSnapshot()
	if m.resultReturn == workSourceRepository && m.openSourceRepository(m.selectedSourceID) {
		return m, nil
	}
	m.setSourceHome(m.selectedSourceID)
	return m, nil
}

func (m *workAppModel) cancelSourceRefresh() {
	if m.sourceLoadCancel != nil {
		m.sourceLoadCancel()
		m.sourceLoadCancel = nil
	}
	m.generation++
	switch m.sourceLoadReturn {
	case workSourceRepository:
		if m.openSourceRepository(m.selectedSourceID) {
			return
		}
		m.setSourceHome(m.selectedSourceID)
	case workSourceHome:
		m.setSourceHome(m.selectedSourceID)
	default:
		m.setHome(m.selectedWorkName)
	}
}

func (m *workAppModel) syncRepositoryOptionsFromSourceSnapshot() {
	options := make([]WorkRepositoryOption, 0, len(m.sourceSnapshot.Repositories))
	valid := make(map[string]struct{}, len(m.sourceSnapshot.Repositories))
	for _, repository := range m.sourceSnapshot.Repositories {
		valid[repository.ID] = struct{}{}
		options = append(options, WorkRepositoryOption{
			ID: repository.ID, Name: repository.Name, Path: repository.Path,
			Groups: append([]string(nil), repository.Groups...),
		})
	}
	for id := range m.selectedSources {
		if _, ok := valid[id]; !ok {
			delete(m.selectedSources, id)
		}
	}
	m.actions.Repositories = options
}

func (m *workAppModel) setSourceHome(selectID string) {
	items := make([]list.Item, 0, len(m.sourceSnapshot.Repositories))
	for index, repository := range m.sourceSnapshot.Repositories {
		if !sourceMatchesFilter(repository.Groups, m.sourceFilter) {
			continue
		}
		mark := "○"
		if m.selectedSources[repository.ID] {
			mark = "✓"
		}
		name := repository.Name
		if name == "" {
			name = repository.ID
		}
		items = append(items, workItem{
			kind: workItemSourceRepository, index: index, id: repository.ID,
			title: mark + " " + name, desc: sourceRepositoryDescription(repository),
		})
	}
	m.setList(items)
	m.screen = workSourceHome
	m.selectListID(selectID, workItemSourceRepository)
}

func sourceMatchesFilter(groups []string, filter string) bool {
	switch filter {
	case sourceFilterAll:
		return true
	case sourceFilterUngrouped:
		return len(groups) == 0
	default:
		return contains(groups, filter)
	}
}

func (m workAppModel) sourceHomeView() string {
	visibleItems := m.list.VisibleItems()
	visible := len(visibleItems)
	selected := len(m.selectedSourceRepositoryIDs())
	visibleIDs := make(map[string]struct{}, visible)
	for _, raw := range visibleItems {
		if item, ok := raw.(workItem); ok && item.kind == workItemSourceRepository {
			visibleIDs[item.id] = struct{}{}
		}
	}
	hidden := 0
	for id, enabled := range m.selectedSources {
		if !enabled {
			continue
		}
		if _, ok := visibleIDs[id]; !ok {
			hidden++
		}
	}
	contextLine := fmt.Sprintf("%d of %d · group: %s · %d selected", visible, len(m.sourceSnapshot.Repositories), sourceFilterLabel(m.sourceFilter), selected)
	if hidden > 0 {
		contextLine += fmt.Sprintf(" (%d hidden by filter)", hidden)
	}
	return m.listView("Works  [ Repositories ]", contextLine, "tab works  j/k move  space select  a all visible  l/enter open  f group  : actions  / search  r refresh  q quit")
}

func sourceFilterLabel(filter string) string {
	switch filter {
	case sourceFilterAll:
		return "All"
	case sourceFilterUngrouped:
		return "Ungrouped"
	default:
		return filter
	}
}

func sourceRepositoryDescription(repository sourcerepos.RepositorySnapshot) string {
	parts := []string{repository.DefaultBranch, sourceRelationText(repository.Relation, !repository.FetchedAt.IsZero())}
	if repository.WorkingKnown && repository.WorkingTree.Dirty() {
		parts = append(parts, "local changes")
	}
	if repository.Problem != "" {
		parts = append(parts, "inspection issue")
	}
	if len(repository.Groups) > 0 {
		parts = append(parts, strings.Join(repository.Groups, ", "))
	} else {
		parts = append(parts, "ungrouped")
	}
	return strings.Join(parts, " · ")
}

func sourceRelationText(relation sourcerepos.Relation, fetched bool) string {
	prefix := "known "
	if fetched {
		prefix = "fetched "
	}
	switch relation {
	case sourcerepos.RelationEqual:
		return prefix + "current"
	case sourcerepos.RelationBehind:
		return prefix + "behind"
	case sourcerepos.RelationAhead:
		return prefix + "ahead"
	case sourcerepos.RelationDiverged:
		return prefix + "diverged"
	case sourcerepos.RelationMissing:
		return "remote ?"
	default:
		return "unknown"
	}
}

func (m workAppModel) updateSourceScreen(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.isListScreen() && m.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	if key == "q" {
		m.cancelLoad()
		m.quitting = true
		return m, tea.Quit
	}
	if m.screen == workSourceLoading {
		if key == "esc" {
			m.cancelSourceRefresh()
		}
		return m, nil
	}
	if key == "tab" || key == "shift+tab" {
		m.captureSourceSelection()
		m.setHome(m.selectedWorkName)
		return m, nil
	}
	switch m.screen {
	case workSourceHome:
		switch key {
		case " ":
			if item, ok := m.list.SelectedItem().(workItem); ok && item.kind == workItemSourceRepository {
				if m.selectedSources[item.id] {
					delete(m.selectedSources, item.id)
				} else {
					m.selectedSources[item.id] = true
				}
				return m, m.refreshSourceSelectionMarkers()
			}
			return m, nil
		case "a":
			return m, m.toggleVisibleSourceSelection()
		case "enter", "l":
			if item, ok := m.list.SelectedItem().(workItem); ok && item.kind == workItemSourceRepository {
				m.openSourceRepository(item.id)
			}
			return m, nil
		case "f":
			m.captureSourceSelection()
			m.openSourceFilter()
			return m, nil
		case ":":
			m.openSourceActions()
			return m, nil
		case "r":
			return m.beginSourceRefresh(workSourceHome)
		}
	case workSourceRepository:
		switch key {
		case "h", "esc":
			m.setSourceHome(m.selectedSourceID)
			return m, nil
		case ":":
			m.openSourceActions()
			return m, nil
		case "r":
			return m.beginSourceRefresh(workSourceRepository)
		}
		m.scrollDetail(msg)
		return m, nil
	case workSourceFilter:
		switch key {
		case "h", "esc":
			m.setSourceHome(m.selectedSourceID)
			return m, nil
		case "enter", "l":
			if item, ok := m.list.SelectedItem().(workItem); ok && item.kind == workItemSourceFilter {
				m.sourceFilter = strings.TrimPrefix(item.id, sourceFilterPrefix)
				m.setSourceHome(m.selectedSourceID)
			}
			return m, nil
		}
	case workSourceActions:
		switch key {
		case "h", "esc":
			m.restoreSourceActionReturn()
			return m, nil
		case "enter", "l":
			return m.activateSourceAction()
		}
	case workSourceRemoveGroup:
		switch key {
		case "h", "esc":
			m.openSourceActions()
			return m, nil
		case "enter", "l":
			if item, ok := m.list.SelectedItem().(workItem); ok && item.kind == workItemSourceGroup {
				return m.startSourceGroupChange("remove", item.id)
			}
			return m, nil
		}
	}
	if m.isListScreen() {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *workAppModel) toggleVisibleSourceSelection() tea.Cmd {
	ids := make([]string, 0, len(m.list.VisibleItems()))
	selectAll := false
	for _, raw := range m.list.VisibleItems() {
		item, ok := raw.(workItem)
		if !ok || item.kind != workItemSourceRepository {
			continue
		}
		ids = append(ids, item.id)
		if !m.selectedSources[item.id] {
			selectAll = true
		}
	}
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if selectAll {
			m.selectedSources[id] = true
		} else {
			delete(m.selectedSources, id)
		}
	}
	return m.refreshSourceSelectionMarkers()
}

func (m *workAppModel) refreshSourceSelectionMarkers() tea.Cmd {
	names := make(map[string]string, len(m.sourceSnapshot.Repositories))
	for _, repository := range m.sourceSnapshot.Repositories {
		name := repository.Name
		if name == "" {
			name = repository.ID
		}
		names[repository.ID] = name
	}
	items := append([]list.Item(nil), m.list.Items()...)
	for index, raw := range items {
		item, ok := raw.(workItem)
		if !ok || item.kind != workItemSourceRepository {
			continue
		}
		mark := "○"
		if m.selectedSources[item.id] {
			mark = "✓"
		}
		item.title = mark + " " + names[item.id]
		items[index] = item
	}
	return m.list.SetItems(items)
}

func (m *workAppModel) captureSourceSelection() {
	if m.screen == workSourceHome {
		if item, ok := m.list.SelectedItem().(workItem); ok && item.kind == workItemSourceRepository {
			m.selectedSourceID = item.id
		}
	}
}

func (m *workAppModel) openSourceFilter() {
	items := []list.Item{workItem{kind: workItemSourceFilter, id: sourceFilterPrefix + sourceFilterAll, title: "All", desc: fmt.Sprintf("%d repositories", len(m.sourceSnapshot.Repositories))}}
	for _, group := range m.sourceGroupNames() {
		count := 0
		for _, repository := range m.sourceSnapshot.Repositories {
			if contains(repository.Groups, group) {
				count++
			}
		}
		items = append(items, workItem{kind: workItemSourceFilter, id: sourceFilterPrefix + group, title: group, desc: fmt.Sprintf("%d repositories", count)})
	}
	ungrouped := 0
	for _, repository := range m.sourceSnapshot.Repositories {
		if len(repository.Groups) == 0 {
			ungrouped++
		}
	}
	items = append(items, workItem{kind: workItemSourceFilter, id: sourceFilterPrefix + sourceFilterUngrouped, title: "Ungrouped", desc: fmt.Sprintf("%d repositories", ungrouped)})
	m.setList(items)
	m.screen = workSourceFilter
	m.selectListID(sourceFilterPrefix+m.sourceFilter, workItemSourceFilter)
}

func (m *workAppModel) openNewWorkRepositoryFilter() {
	items := []list.Item{workItem{kind: workItemSourceFilter, id: sourceFilterPrefix + sourceFilterAll, title: "All", desc: fmt.Sprintf("%d repositories", len(m.actions.Repositories))}}
	seen := make(map[string]struct{})
	ungrouped := 0
	for _, repository := range m.actions.Repositories {
		if len(repository.Groups) == 0 {
			ungrouped++
		}
		for _, group := range repository.Groups {
			seen[group] = struct{}{}
		}
	}
	groups := make([]string, 0, len(seen))
	for group := range seen {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		count := 0
		for _, repository := range m.actions.Repositories {
			if contains(repository.Groups, group) {
				count++
			}
		}
		items = append(items, workItem{kind: workItemSourceFilter, id: sourceFilterPrefix + group, title: group, desc: fmt.Sprintf("%d repositories", count)})
	}
	items = append(items, workItem{kind: workItemSourceFilter, id: sourceFilterPrefix + sourceFilterUngrouped, title: "Ungrouped", desc: fmt.Sprintf("%d repositories", ungrouped)})
	m.setList(items)
	m.screen = workNewRepositoryFilter
	m.selectListID(sourceFilterPrefix+m.newWorkFilter, workItemSourceFilter)
}

func (m workAppModel) updateNewWorkRepositoryFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "h", "esc":
		m.setNewWorkRepositories(0)
		return m, nil
	case "enter", "l":
		if item, ok := m.list.SelectedItem().(workItem); ok && item.kind == workItemSourceFilter {
			m.newWorkFilter = strings.TrimPrefix(item.id, sourceFilterPrefix)
			m.setNewWorkRepositories(0)
		}
		return m, nil
	case "q":
		m.cancelLoad()
		m.quitting = true
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m workAppModel) sourceGroupNames() []string {
	seen := make(map[string]struct{})
	for _, repository := range m.sourceSnapshot.Repositories {
		for _, group := range repository.Groups {
			seen[group] = struct{}{}
		}
	}
	groups := make([]string, 0, len(seen))
	for group := range seen {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return groups
}

func (m *workAppModel) openSourceActions() {
	m.captureSourceSelection()
	m.sourceActionIDs = m.sourceActionScope()
	m.actionReturn = m.screen
	noScope := len(m.sourceActionIDs) == 0
	items := []list.Item{
		sourceActionItem(sourceActionFetch, "Fetch", "Update configured remote-tracking refs", noScope || m.actions.FetchSourceRepos == nil),
		sourceActionItem(sourceActionUpdate, "Update default branch", "Fetch, plan, and fast-forward eligible local defaults", noScope || m.actions.PlanSourceUpdate == nil || m.actions.RunSourceUpdate == nil),
		sourceActionItem(sourceActionAddGroup, "Add group", "Add one group label to the repository scope", noScope || m.actions.AddSourceGroup == nil),
		sourceActionItem(sourceActionRemoveGroup, "Remove group", "Remove one existing group label from the repository scope", m.actions.RemoveSourceGroup == nil || len(m.sourceScopeGroups()) == 0),
	}
	if len(m.sourceActionIDs) == 1 {
		for _, program := range m.actions.Programs {
			title := "Open in " + program.Name
			if program.Default {
				title = "Open repository · " + program.Name
			}
			items = append(items, sourceActionItem(sourceOpenPrefix+program.ID, title, "Launch the source repository", m.actions.OpenSourceRepo == nil))
		}
	}
	items = append(items, sourceActionItem(sourceActionRefresh, "Refresh", "Reinspect local state without fetching", m.actions.LoadSourceRepos == nil))
	items = append(items, sourceActionItem(sourceActionScan, "Scan repositories", "Discover primary clones under repos_root", m.actions.ScanSourceRepos == nil))
	m.setList(items)
	m.screen = workSourceActions
}

func sourceActionItem(id, title, description string, blocked bool) list.Item {
	reason := ""
	if blocked {
		reason = "action is unavailable"
	}
	return workItem{kind: workItemAction, id: id, title: title, desc: actionDescription(description, reason), blockedReason: reason}
}

func (m workAppModel) sourceActionScope() []string {
	ids := m.selectedSourceRepositoryIDs()
	if len(ids) > 0 {
		return ids
	}
	if m.selectedSourceID != "" {
		return []string{m.selectedSourceID}
	}
	if item, ok := m.list.SelectedItem().(workItem); ok && item.kind == workItemSourceRepository {
		return []string{item.id}
	}
	return nil
}

func (m workAppModel) selectedSourceRepositoryIDs() []string {
	ids := make([]string, 0, len(m.selectedSources))
	for id, enabled := range m.selectedSources {
		if enabled {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (m workAppModel) sourceScopeGroups() []string {
	seen := make(map[string]struct{})
	for _, id := range m.sourceActionIDs {
		if repository, ok := m.sourceRepository(id); ok {
			for _, group := range repository.Groups {
				seen[group] = struct{}{}
			}
		}
	}
	groups := make([]string, 0, len(seen))
	for group := range seen {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return groups
}

func (m workAppModel) activateSourceAction() (tea.Model, tea.Cmd) {
	item, ok := m.list.SelectedItem().(workItem)
	if !ok || item.kind != workItemAction {
		return m, nil
	}
	if item.blockedReason != "" {
		m.actionNotice = "Unavailable: " + item.blockedReason
		return m, nil
	}
	switch {
	case item.id == sourceActionFetch:
		return m.startSourceFetch()
	case item.id == sourceActionUpdate:
		return m.startSourceUpdatePlanning()
	case item.id == sourceActionAddGroup:
		m.input.SetValue("")
		m.input.Placeholder = "team-a"
		m.input.Focus()
		m.screen = workSourceGroupInput
		return m, nil
	case item.id == sourceActionRemoveGroup:
		m.openSourceRemoveGroup()
		return m, nil
	case item.id == sourceActionRefresh:
		return m.beginSourceRefresh(m.actionReturn)
	case item.id == sourceActionScan:
		return m.startSourceScan()
	case strings.HasPrefix(item.id, sourceOpenPrefix):
		return m.startOpenSourceRepository(strings.TrimPrefix(item.id, sourceOpenPrefix))
	default:
		return m, nil
	}
}

func (m workAppModel) startSourceScan() (tea.Model, tea.Cmd) {
	if m.actions.ScanSourceRepos == nil {
		return m, nil
	}
	operationCtx, generation := m.beginOperation("scan-source-repositories", "Scanning source repositories", "Scanning repos_root and preserving existing repository metadata…")
	return m, func() tea.Msg {
		summary, err := m.actions.ScanSourceRepos(operationCtx)
		return sourceReposScannedMsg{generation: generation, summary: summary, err: err}
	}
}

func (m workAppModel) handleSourceReposScanned(msg sourceReposScannedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || m.operationKind != "scan-source-repositories" {
		return m, nil
	}
	m.finishOperation()
	if msg.err != nil {
		m.setActionResult("Repository scan failed", msg.err.Error(), workSourceHome, false)
		return m, nil
	}
	m.setActionResult("Repository scan complete", msg.summary, workSourceHome, true)
	return m, nil
}

func (m *workAppModel) restoreSourceActionReturn() {
	if m.actionReturn == workSourceRepository && m.openSourceRepository(m.selectedSourceID) {
		return
	}
	m.setSourceHome(m.selectedSourceID)
}

func (m *workAppModel) openSourceRemoveGroup() {
	groups := m.sourceScopeGroups()
	items := make([]list.Item, 0, len(groups))
	for _, group := range groups {
		items = append(items, workItem{kind: workItemSourceGroup, id: group, title: group, desc: "Remove from the complete repository scope"})
	}
	m.setList(items)
	m.screen = workSourceRemoveGroup
}

func (m workAppModel) updateSourceGroupInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.input.Blur()
		m.openSourceActions()
		return m, nil
	case "enter":
		group := strings.TrimSpace(m.input.Value())
		if group == "" {
			m.actionNotice = "Group name is required"
			return m, nil
		}
		m.input.Blur()
		return m.startSourceGroupChange("add", group)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.actionNotice = ""
	return m, cmd
}

func (m workAppModel) sourceGroupInputView() string {
	message := fmt.Sprintf("Add a group to %d repositories.", len(m.sourceActionIDs))
	if m.actionNotice != "" {
		message = m.actionNotice
	}
	return Title("Add repository group") + "\n" + subtitleStyle.Render(message) + "\n\n" + m.input.View() + "\n\n" + hintStyle.Render("enter add  esc cancel")
}

func (m workAppModel) startSourceGroupChange(action, group string) (tea.Model, tea.Cmd) {
	if (action == "add" && m.actions.AddSourceGroup == nil) || (action == "remove" && m.actions.RemoveSourceGroup == nil) {
		return m, nil
	}
	ids := append([]string(nil), m.sourceActionIDs...)
	operationCtx, generation := m.beginOperation("source-group-"+action, strings.ToUpper(action[:1])+action[1:]+" repository group", fmt.Sprintf("Updating group %s for %d repositories…", group, len(ids)))
	return m, func() tea.Msg {
		var err error
		if action == "add" {
			err = m.actions.AddSourceGroup(operationCtx, ids, group)
		} else {
			err = m.actions.RemoveSourceGroup(operationCtx, ids, group)
		}
		return sourceGroupChangedMsg{generation: generation, group: group, action: action, err: err}
	}
}

func (m workAppModel) handleSourceGroupChanged(msg sourceGroupChangedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || m.operationKind != "source-group-"+msg.action {
		return m, nil
	}
	m.finishOperation()
	if msg.err != nil {
		m.setActionResult("Repository group update failed", msg.err.Error(), workSourceHome, false)
		return m, nil
	}
	verb := "added"
	if msg.action == "remove" {
		verb = "removed"
	}
	m.setActionResult("Repository groups updated", fmt.Sprintf("Group %s was %s for %d repositories.", msg.group, verb, len(m.sourceActionIDs)), workSourceHome, true)
	return m, nil
}

func (m workAppModel) startSourceFetch() (tea.Model, tea.Cmd) {
	if m.actions.FetchSourceRepos == nil || len(m.sourceActionIDs) == 0 {
		return m, nil
	}
	ids := append([]string(nil), m.sourceActionIDs...)
	operationCtx, generation := m.beginOperation("fetch-source-repositories", "Fetching source repositories", fmt.Sprintf("Fetching configured remotes concurrently for %d repositories…", len(ids)))
	return m, func() tea.Msg {
		result, err := m.actions.FetchSourceRepos(operationCtx, ids)
		return sourceReposFetchedMsg{generation: generation, result: result, err: err}
	}
}

func (m workAppModel) handleSourceReposFetched(msg sourceReposFetchedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || m.operationKind != "fetch-source-repositories" {
		return m, nil
	}
	m.finishOperation()
	title := "Source repositories fetched"
	content := formatSourceResult(msg.result)
	if msg.err != nil {
		title = "Source Repository Fetch failed"
		content = msg.err.Error() + "\n\n" + content
	} else if msg.result.NeedsAttention() {
		title = "Source Repository Fetch needs attention"
	}
	m.setActionResult(title, content, m.actionReturn, true)
	return m, nil
}

func (m workAppModel) startSourceUpdatePlanning() (tea.Model, tea.Cmd) {
	if m.actions.PlanSourceUpdate == nil || len(m.sourceActionIDs) == 0 {
		return m, nil
	}
	ids := append([]string(nil), m.sourceActionIDs...)
	operationCtx, generation := m.beginOperation("plan-source-update", "Planning source repository update", fmt.Sprintf("Fetching concurrently and planning exact default-branch updates for %d repositories…", len(ids)))
	return m, func() tea.Msg {
		plan, err := m.actions.PlanSourceUpdate(operationCtx, ids)
		return sourceUpdatePlannedMsg{generation: generation, plan: plan, err: err}
	}
}

func (m workAppModel) handleSourceUpdatePlanned(msg sourceUpdatePlannedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || m.operationKind != "plan-source-update" {
		return m, nil
	}
	m.finishOperation()
	if msg.err != nil {
		m.setActionResult("Source repository update plan failed", msg.err.Error(), m.actionReturn, true)
		return m, nil
	}
	m.sourcePlan = msg.plan
	m.screen = workSourceUpdatePlan
	m.setDetail("Source repository update plan", formatSourcePlan(msg.plan))
	return m, nil
}

func (m workAppModel) updateSourceUpdatePlan(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "u":
		return m.startRunSourceUpdate()
	case "h", "esc":
		m.restoreSourceActionReturn()
		return m, nil
	case "q":
		m.cancelLoad()
		m.quitting = true
		return m, tea.Quit
	}
	m.scrollDetail(msg)
	return m, nil
}

func (m workAppModel) startRunSourceUpdate() (tea.Model, tea.Cmd) {
	if m.actions.RunSourceUpdate == nil {
		return m, nil
	}
	plan := m.sourcePlan
	operationCtx, generation := m.beginOperation("run-source-update", "Updating source repositories", fmt.Sprintf("Revalidating and updating %d repository plans…", len(plan.Repositories)))
	return m, func() tea.Msg {
		result, err := m.actions.RunSourceUpdate(operationCtx, plan)
		return sourceUpdateCompletedMsg{generation: generation, result: result, err: err}
	}
}

func (m workAppModel) handleSourceUpdateCompleted(msg sourceUpdateCompletedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || m.operationKind != "run-source-update" {
		return m, nil
	}
	m.finishOperation()
	title := "Source repositories updated"
	content := formatSourceResult(msg.result)
	if msg.err != nil {
		title = "Source repository update failed"
		content = msg.err.Error() + "\n\n" + content
	} else if msg.result.NeedsAttention() {
		title = "Source repositories need attention"
	}
	m.setActionResult(title, content, m.actionReturn, true)
	return m, nil
}

func (m workAppModel) startOpenSourceRepository(programID string) (tea.Model, tea.Cmd) {
	if m.actions.OpenSourceRepo == nil || len(m.sourceActionIDs) != 1 {
		return m, nil
	}
	programName := programID
	for _, program := range m.actions.Programs {
		if program.ID == programID {
			programName = program.Name
			break
		}
	}
	repositoryID := m.sourceActionIDs[0]
	operationCtx, generation := m.beginOperation("open-source-repository", "Opening source repository · "+programName, repositoryID)
	return m, func() tea.Msg {
		err := m.actions.OpenSourceRepo(operationCtx, repositoryID, programID)
		return sourceRepoOpenedMsg{generation: generation, program: programName, err: err}
	}
}

func (m workAppModel) handleSourceRepoOpened(msg sourceRepoOpenedMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.operationID || m.operationKind != "open-source-repository" {
		return m, nil
	}
	m.finishOperation()
	if msg.err != nil {
		m.setActionResult("Open source repository failed", msg.err.Error(), m.actionReturn, false)
		return m, nil
	}
	m.setActionResult("Source repository opened", msg.program+" was started for "+m.sourceActionIDs[0]+".", m.actionReturn, false)
	return m, nil
}

func (m *workAppModel) openSourceRepository(id string) bool {
	repository, ok := m.sourceRepository(id)
	if !ok {
		return false
	}
	m.selectedSourceID = id
	m.screen = workSourceRepository
	m.setDetail("Source repository · "+id, formatSourceRepository(repository))
	return true
}

func (m workAppModel) sourceRepository(id string) (sourcerepos.RepositorySnapshot, bool) {
	for _, repository := range m.sourceSnapshot.Repositories {
		if repository.ID == id {
			return repository, true
		}
	}
	return sourcerepos.RepositorySnapshot{}, false
}

func formatSourceRepository(repository sourcerepos.RepositorySnapshot) string {
	lines := []string{
		factLine("Path", repository.Path),
		factLine("Groups", emptyValue(strings.Join(repository.Groups, ", "), "none")),
		factLine("Remote", repository.Remote),
		factLine("Default branch", repository.DefaultBranch),
		factLine("Observed branch", knownValue(repository.CheckoutKnown, repository.Checkout.FullRef)),
		factLine("Working tree", workingTreeKnownValue(repository.WorkingKnown, repository.WorkingTree)),
		factLine("Git operation", operationsValue(repository.OperationsKnown, repository.Operations)),
		factLine("Local default", sourceRefValue(repository.LocalKnown, repository.LocalRef, repository.LocalOID)),
		factLine("Remote default", sourceRefValue(repository.RemoteKnown, repository.RemoteRef, repository.RemoteOID)),
		factLine("Relation", sourceRelationText(repository.Relation, !repository.FetchedAt.IsZero())),
	}
	if repository.Problem != "" {
		lines = append(lines, "", "Problem", "  "+repository.Problem)
	}
	return strings.Join(lines, "\n")
}

func workingTreeKnownValue(known bool, status interface{ Dirty() bool }) string {
	if !known {
		return "unknown"
	}
	if status.Dirty() {
		return "local changes"
	}
	return "clean"
}

func sourceRefValue(known bool, ref, oid string) string {
	if !known {
		return "missing or unknown"
	}
	return ref + " @ " + shortOID(oid)
}

func emptyValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func formatSourcePlan(plan sourcerepos.Plan) string {
	updates, unchanged, skipped, failed := 0, 0, 0, 0
	for _, repository := range plan.Repositories {
		switch repository.Action {
		case sourcerepos.ActionUpdateCheckout, sourcerepos.ActionUpdateRef:
			updates++
		case sourcerepos.ActionUnchanged:
			unchanged++
		case sourcerepos.ActionFailed:
			failed++
		default:
			skipped++
		}
	}
	lines := []string{
		factLine("Repositories", fmt.Sprint(len(plan.Repositories))),
		factLine("Scope", strings.Join(sourcePlanIDs(plan), ", ")),
		factLine("Updates", fmt.Sprint(updates)), factLine("Unchanged", fmt.Sprint(unchanged)),
		factLine("Skipped", fmt.Sprint(skipped)), factLine("Failed", fmt.Sprint(failed)),
		factLine("Remote mutation", "none"), "", "Repository plan",
	}
	for _, repository := range plan.Repositories {
		detail := string(repository.Action)
		if repository.Reason != "" {
			detail += ": " + repository.Reason
		}
		lines = append(lines, "", repository.ID,
			"  action: "+detail,
			"  source: "+repository.Path,
			"  identity: "+emptyValue(repository.GitCommonDir, "unknown"),
			"  configured: "+repository.Remote+" / "+repository.DefaultBranch,
			"  relation: "+sourceRelationText(repository.Relation, true),
			"  local: "+sourceRefValue(repository.LocalOID != "", repository.LocalRef, repository.LocalOID),
			"  fetched: "+sourceRefValue(repository.TargetOID != "", repository.RemoteRef, repository.TargetOID),
		)
		if repository.CheckoutPath != "" {
			lines = append(lines, "  checkout: "+repository.CheckoutPath)
		}
	}
	return strings.Join(lines, "\n")
}

func sourcePlanIDs(plan sourcerepos.Plan) []string {
	ids := make([]string, 0, len(plan.Repositories))
	for _, repository := range plan.Repositories {
		ids = append(ids, repository.ID)
	}
	return ids
}

func formatSourceResult(result sourcerepos.Result) string {
	lines := []string{factLine("Repositories", fmt.Sprint(len(result.Repositories)))}
	for _, repository := range result.Repositories {
		line := string(repository.Status)
		if repository.Err != nil {
			line += ": " + repository.Err.Error()
		} else if !repository.Snapshot.FetchedAt.IsZero() {
			line += " · " + sourceRelationText(repository.Snapshot.Relation, true)
		}
		lines = append(lines, "", repository.ID, "  "+line)
	}
	return strings.Join(lines, "\n")
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
