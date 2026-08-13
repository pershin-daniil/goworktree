package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	gitops "github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectworks"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/removework"
	"github.com/pershin-daniil/goworktree/internal/workflow/repairwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/syncwork"
)

const (
	minimumWorkAppWidth  = 80
	minimumWorkAppHeight = 24
	detailViewportChrome = 8
)

type WorkAppActions struct {
	Version        string
	Load           func(context.Context) (inspectworks.Snapshot, error)
	Repositories   []WorkRepositoryOption
	Programs       []WorkProgramOption
	PlanNewWork    func(context.Context, newwork.Request) (newwork.Plan, error)
	CreateNewWork  func(context.Context, newwork.Plan) (newwork.ExecutionResult, error)
	ResumeNewWork  func(context.Context, string) (newwork.ExecutionResult, error)
	OpenWork       func(context.Context, WorkOpenRequest) error
	PlanSyncWork   func(context.Context, string) (syncwork.Plan, error)
	RunSyncWork    func(context.Context, syncwork.Plan) (syncwork.Result, error)
	PlanRemoveWork func(context.Context, string) (removework.Plan, error)
	RunRemoveWork  func(context.Context, removework.Plan, string) (removework.Result, error)
	PlanRepairWork func(context.Context, string) (repairwork.Plan, error)
	RunRepairWork  func(context.Context, repairwork.Plan) (repairwork.Result, error)
}

type WorkRepositoryOption struct {
	ID   string
	Name string
	Path string
}

type WorkProgramOption struct {
	ID      string
	Name    string
	Default bool
}

type WorkOpenRequest struct {
	WorkName string
	WorkRoot string
	Program  string
}

type workScreen int

const (
	workLoading workScreen = iota
	workHome
	workOverview
	workRepository
	workProblem
	workLoadError
	workActions
	workNewName
	workNewRepositories
	workNewPlan
	workSyncPlan
	workRemovePlan
	workRemoveConfirm
	workRepairPlan
	workOperation
	workActionResult
)

type workItemKind int

const (
	workItemEntry workItemKind = iota
	workItemRepository
	workItemProblem
	workItemCollectionProblem
	workItemAction
	workItemNewRepository
)

type workItem struct {
	kind          workItemKind
	index         int
	id            string
	title         string
	desc          string
	blockedReason string
}

func (i workItem) FilterValue() string { return terminalSafe(i.title+" "+i.desc, false) }
func (i workItem) Title() string       { return terminalSafe(i.title, false) }
func (i workItem) Description() string { return terminalSafe(i.desc, false) }

type worksLoadedMsg struct {
	generation uint64
	snapshot   inspectworks.Snapshot
	err        error
}

type newWorkPlannedMsg struct {
	generation uint64
	plan       newwork.Plan
	err        error
}

type newWorkCreatedMsg struct {
	generation uint64
	result     newwork.ExecutionResult
	err        error
}

type workOpenedMsg struct {
	generation uint64
	program    string
	err        error
}

type syncWorkPlannedMsg struct {
	generation uint64
	plan       syncwork.Plan
	err        error
}

type syncWorkCompletedMsg struct {
	generation uint64
	result     syncwork.Result
	err        error
}

type removeWorkPlannedMsg struct {
	generation uint64
	plan       removework.Plan
	err        error
}

type removeWorkCompletedMsg struct {
	generation uint64
	result     removework.Result
	err        error
}

type repairWorkPlannedMsg struct {
	generation uint64
	plan       repairwork.Plan
	err        error
}

type repairWorkCompletedMsg struct {
	generation uint64
	result     repairwork.Result
	err        error
}

type workAppModel struct {
	actions          WorkAppActions
	ctx              context.Context
	cancel           context.CancelFunc
	screen           workScreen
	restoreScreen    workScreen
	snapshot         inspectworks.Snapshot
	list             list.Model
	viewport         viewport.Model
	width            int
	height           int
	generation       uint64
	selectedWorkName string
	selectedRepoID   string
	selectedWork     int
	detailTitle      string
	detailContent    string
	input            textinput.Model
	actionReturn     workScreen
	actionNotice     string
	selectedNewRepos map[string]bool
	newWorkMode      newwork.Mode
	newWorkPlan      newwork.Plan
	syncWorkPlan     syncwork.Plan
	removeWorkPlan   removework.Plan
	repairWorkPlan   repairwork.Plan
	operationCancel  context.CancelFunc
	operationID      uint64
	operationTitle   string
	operationMessage string
	operationKind    string
	resultReturn     workScreen
	resultRefresh    bool
	quitting         bool
}

func RunWorkApp(actions WorkAppActions) error {
	if actions.Load == nil {
		return fmt.Errorf("Work app loader is nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	model := newWorkAppModel(actions, ctx, cancel)
	_, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}

func newWorkAppModel(actions WorkAppActions, ctx context.Context, cancel context.CancelFunc) workAppModel {
	input := textinput.New()
	input.Placeholder = "EVOVPC-1234-short-description"
	input.Prompt = "› "
	input.CharLimit = 128
	input.Width = 56
	model := workAppModel{
		actions: actions, ctx: ctx, cancel: cancel, screen: workLoading,
		restoreScreen: workHome, width: minimumWorkAppWidth, height: minimumWorkAppHeight,
		input: input, selectedNewRepos: make(map[string]bool), newWorkMode: newwork.ModeOnline,
	}
	model.viewport = viewport.New(minimumWorkAppWidth-4, minimumWorkAppHeight-detailViewportChrome)
	model.generation = 1
	return model
}

func (m workAppModel) Init() tea.Cmd {
	return m.loadCommand(m.generation)
}

func (m workAppModel) loadCommand(generation uint64) tea.Cmd {
	return func() tea.Msg {
		snapshot, err := m.actions.Load(m.ctx)
		return worksLoadedMsg{generation: generation, snapshot: snapshot, err: err}
	}
}

func (m workAppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil
	case worksLoadedMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		if msg.err != nil {
			m.screen = workLoadError
			m.setDetail("Inspection failed", formatLoadError(msg.err))
			return m, nil
		}
		m.snapshot = msg.snapshot
		m.restoreAfterLoad()
		return m, nil
	case newWorkPlannedMsg:
		return m.handleNewWorkPlanned(msg)
	case newWorkCreatedMsg:
		return m.handleNewWorkCreated(msg)
	case workOpenedMsg:
		return m.handleWorkOpened(msg)
	case syncWorkPlannedMsg:
		return m.handleSyncWorkPlanned(msg)
	case syncWorkCompletedMsg:
		return m.handleSyncWorkCompleted(msg)
	case removeWorkPlannedMsg:
		return m.handleRemoveWorkPlanned(msg)
	case removeWorkCompletedMsg:
		return m.handleRemoveWorkCompleted(msg)
	case repairWorkPlannedMsg:
		return m.handleRepairWorkPlanned(msg)
	case repairWorkCompletedMsg:
		return m.handleRepairWorkCompleted(msg)
	case tea.KeyMsg:
		key := msg.String()
		if key == "ctrl+c" {
			if m.screen == workOperation {
				if m.operationCancel != nil {
					m.operationCancel()
					m.operationMessage = "Cancellation requested; waiting for the current safe boundary…"
				}
				return m, nil
			}
			m.cancelLoad()
			m.quitting = true
			return m, tea.Quit
		}
		if m.tooSmall() {
			if key == "q" && m.screen != workOperation {
				m.cancelLoad()
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		}
		if m.screen == workLoading {
			if key == "q" {
				m.cancelLoad()
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		}
		if m.screen == workNewName {
			return m.updateNewWorkName(msg)
		}
		if m.screen == workOperation {
			return m, nil
		}
		if m.screen == workNewPlan {
			return m.updateNewWorkPlan(msg)
		}
		if m.screen == workSyncPlan {
			return m.updateSyncWorkPlan(msg)
		}
		if m.screen == workRemovePlan {
			return m.updateRemoveWorkPlan(msg)
		}
		if m.screen == workRemoveConfirm {
			return m.updateRemoveWorkConfirm(msg)
		}
		if m.screen == workRepairPlan {
			return m.updateRepairWorkPlan(msg)
		}
		if m.screen == workActionResult {
			return m.updateActionResult(msg)
		}
		if m.isListScreen() && m.list.FilterState() == list.Filtering {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}
		if key == ":" && m.canOpenActions() {
			m.openActionPalette()
			return m, nil
		}
		if key == "n" && m.screen == workHome {
			m.beginNewWork()
			return m, nil
		}
		if key == "r" && m.canRefresh() {
			return m.beginRefresh()
		}
		if key == "q" {
			m.cancelLoad()
			m.quitting = true
			return m, tea.Quit
		}
		if m.screen == workRepository || m.screen == workProblem || m.screen == workLoadError {
			return m.updateDetail(msg)
		}
		if m.screen == workActions {
			return m.updateActionPalette(msg)
		}
		if m.screen == workNewRepositories {
			return m.updateNewWorkRepositories(msg)
		}
		if m.list.FilterState() != list.Filtering {
			switch key {
			case "h", "esc":
				if m.screen == workOverview {
					m.setHome(m.selectedWorkName)
				}
				return m, nil
			case "enter", "l":
				m.activateListItem()
				return m, nil
			}
		}
	}

	if m.isListScreen() {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m workAppModel) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "h", "esc":
		if m.screen == workRepository || (m.screen == workProblem && m.selectedWorkName != "") {
			m.setWork(m.selectedWorkName, m.selectedRepoID)
		} else {
			m.setHome(m.selectedWorkName)
		}
		return m, nil
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
	return m, nil
}

func (m workAppModel) beginRefresh() (tea.Model, tea.Cmd) {
	m.captureSelection()
	m.restoreScreen = m.screen
	if m.restoreScreen == workProblem || m.restoreScreen == workLoadError {
		m.restoreScreen = workHome
	}
	m.screen = workLoading
	m.generation++
	return m, m.loadCommand(m.generation)
}

func (m *workAppModel) restoreAfterLoad() {
	switch m.restoreScreen {
	case workOverview:
		if m.setWork(m.selectedWorkName, m.selectedRepoID) {
			return
		}
	case workRepository:
		if m.setWork(m.selectedWorkName, m.selectedRepoID) && m.openRepository(m.selectedRepoID) {
			return
		}
	}
	m.setHome(m.selectedWorkName)
}

func (m *workAppModel) captureSelection() {
	if m.screen == workHome {
		if item, ok := m.list.SelectedItem().(workItem); ok && item.kind == workItemEntry {
			m.selectedWorkName = item.id
		}
	}
	if m.screen == workOverview {
		if item, ok := m.list.SelectedItem().(workItem); ok && item.kind == workItemRepository {
			m.selectedRepoID = item.id
		}
	}
}

func (m *workAppModel) setHome(selectName string) {
	items := make([]list.Item, 0, len(m.snapshot.Works)+len(m.snapshot.Problems))
	for index, entry := range m.snapshot.Works {
		items = append(items, workItem{
			kind: workItemEntry, index: index, id: entry.Name,
			title: workEntryTitle(entry), desc: workEntryDescription(entry),
		})
	}
	for index, problem := range m.snapshot.Problems {
		items = append(items, workItem{
			kind: workItemCollectionProblem, index: index,
			title: "Inspection issue · " + string(problem.Code), desc: compactMessage(problem.Message),
		})
	}
	m.setList(items)
	m.screen = workHome
	m.selectedRepoID = ""
	m.selectListID(selectName, workItemEntry)
}

func (m *workAppModel) setWork(name, selectRepo string) bool {
	index := m.findWork(name)
	if index < 0 {
		return false
	}
	m.selectedWork, m.selectedWorkName = index, name
	entry := m.snapshot.Works[index]
	items := make([]list.Item, 0)
	for problemIndex, problem := range entry.Problems {
		items = append(items, workItem{
			kind: workItemProblem, index: problemIndex,
			title: "Problem · " + string(problem.Code), desc: compactMessage(problem.Message),
		})
	}
	if entry.Snapshot != nil {
		for problemIndex, problem := range entry.Snapshot.Problems {
			if problem.RepositoryID != "" {
				continue
			}
			items = append(items, workItem{
				kind: workItemProblem, index: len(entry.Problems) + problemIndex,
				title: "Problem · " + string(problem.Code), desc: problemDescription(problem),
			})
		}
		for repositoryIndex, repository := range entry.Snapshot.Repositories {
			items = append(items, workItem{
				kind: workItemRepository, index: repositoryIndex, id: repository.ID,
				title: repository.ID, desc: repositoryDescription(repository),
			})
		}
	}
	if len(items) == 0 {
		items = append(items, workItem{kind: workItemProblem, index: -1, title: "No inspectable repositories", desc: "Work intent is unavailable"})
	}
	m.setList(items)
	m.screen = workOverview
	m.selectListID(selectRepo, workItemRepository)
	return true
}

func (m *workAppModel) setList(items []list.Item) {
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = true
	m.list = list.New(items, delegate, max(40, m.width), max(8, m.height-8))
	m.list.SetShowTitle(false)
	m.list.SetShowStatusBar(false)
	m.list.SetShowHelp(false)
	m.list.SetFilteringEnabled(true)
	m.list.DisableQuitKeybindings()
	applyVimListKeys(&m.list)
}

func (m *workAppModel) selectListID(id string, kind workItemKind) {
	if id == "" {
		return
	}
	for index, item := range m.list.Items() {
		value, ok := item.(workItem)
		if ok && value.kind == kind && value.id == id {
			m.list.Select(index)
			return
		}
	}
}

func (m *workAppModel) activateListItem() {
	item, ok := m.list.SelectedItem().(workItem)
	if !ok {
		return
	}
	switch m.screen {
	case workHome:
		switch item.kind {
		case workItemEntry:
			m.setWork(item.id, "")
		case workItemCollectionProblem:
			problem := m.snapshot.Problems[item.index]
			m.selectedWorkName = ""
			m.screen = workProblem
			m.setDetail("Inspection issue", formatCollectionProblem(problem))
		}
	case workOverview:
		switch item.kind {
		case workItemRepository:
			m.openRepository(item.id)
		case workItemProblem:
			m.openWorkProblem(item.index)
		}
	}
}

func (m *workAppModel) openRepository(id string) bool {
	entry := m.currentWork()
	if entry == nil || entry.Snapshot == nil {
		return false
	}
	for _, repository := range entry.Snapshot.Repositories {
		if repository.ID != id {
			continue
		}
		m.selectedRepoID = id
		m.screen = workRepository
		m.setDetail("Repository · "+id, formatRepository(repository))
		return true
	}
	return false
}

func (m *workAppModel) openWorkProblem(index int) {
	entry := m.currentWork()
	if entry == nil {
		return
	}
	if index < 0 {
		m.screen = workProblem
		m.setDetail("Work · "+entry.Name, "No repository intent is available.\n\nThe path is visible, but it is not adopted as a managed Work.")
		return
	}
	if index < len(entry.Problems) {
		m.screen = workProblem
		m.setDetail("Work problem", formatCollectionProblem(entry.Problems[index]))
		return
	}
	if entry.Snapshot == nil {
		return
	}
	wanted := index - len(entry.Problems)
	if wanted >= 0 && wanted < len(entry.Snapshot.Problems) {
		m.screen = workProblem
		m.setDetail("Work problem", formatWorkProblem(entry.Snapshot.Problems[wanted]))
	}
}

func (m *workAppModel) currentWork() *inspectworks.Work {
	if m.selectedWork < 0 || m.selectedWork >= len(m.snapshot.Works) {
		return nil
	}
	return &m.snapshot.Works[m.selectedWork]
}

func (m *workAppModel) findWork(name string) int {
	for index, entry := range m.snapshot.Works {
		if entry.Name == name {
			return index
		}
	}
	return -1
}

func (m *workAppModel) setDetail(title, content string) {
	m.detailTitle = terminalSafe(title, false)
	m.detailContent = terminalSafe(content, true)
	m.viewport.SetContent(m.renderDetailContent())
	m.viewport.GotoTop()
}

func (m *workAppModel) resize() {
	listHeight := max(8, m.height-8)
	if m.isListScreen() {
		m.list.SetSize(max(40, m.width), listHeight)
	}
	m.input.Width = min(72, max(20, m.width-6))
	m.viewport.Width = max(40, m.width-4)
	m.viewport.Height = max(8, m.height-detailViewportChrome)
	if m.detailContent != "" {
		offset := m.viewport.YOffset
		m.viewport.SetContent(m.renderDetailContent())
		m.viewport.SetYOffset(offset)
	}
}

func (m workAppModel) renderDetailContent() string {
	width := max(40, m.width-4)
	return lipgloss.NewStyle().Width(width).Render(m.detailContent)
}

func (m workAppModel) View() string {
	if m.quitting {
		return ""
	}
	if m.tooSmall() {
		return fmt.Sprintf("goworktree requires at least %d×%d\ncurrent terminal: %d×%d\n\nq quit",
			minimumWorkAppWidth, minimumWorkAppHeight, m.width, m.height)
	}
	switch m.screen {
	case workLoading:
		return Title("goworktree") + "\n" + subtitleStyle.Render("Inspecting local Works…") + "\n\n" + hintStyle.Render("q quit")
	case workLoadError:
		return m.detailView("r retry  q quit")
	case workHome:
		contextLine := fmt.Sprintf("v%s · %s · %s", m.actions.Version, plural(len(m.snapshot.Works), "Work", "Works"), inspectedLabel(m.snapshot.InspectedAt))
		if len(m.snapshot.Problems) > 0 {
			contextLine += fmt.Sprintf(" · %d inspection issues", len(m.snapshot.Problems))
		}
		return m.listView("Works", contextLine, "j/k move  l/enter open  n new  : actions  / search  r refresh  q quit")
	case workOverview:
		entry := m.currentWork()
		contextLine := ""
		if entry != nil {
			contextLine = workContext(*entry)
		}
		return m.listView("Work · "+m.selectedWorkName, contextLine, "j/k move  l/enter open  h back  : actions  / search  r refresh  q quit")
	case workRepository, workProblem:
		return m.detailView("j/k scroll  ctrl+u/d page  g/G top/bottom  h/esc back  : actions  r refresh  q quit")
	case workActions:
		contextLine := "Home actions"
		if m.selectedWorkName != "" && m.actionReturn != workHome {
			contextLine = "Work · " + m.selectedWorkName
		}
		if m.actionNotice != "" {
			contextLine += " · " + m.actionNotice
		}
		return m.listView("Actions", contextLine, "j/k move  l/enter select  h/esc close  / search  q quit")
	case workNewName:
		return m.newWorkNameView()
	case workNewRepositories:
		contextLine := fmt.Sprintf("Work · %s · mode %s · %d selected", m.input.Value(), m.newWorkMode, len(m.selectedRepositoryIDs()))
		if m.actionNotice != "" {
			contextLine += " · " + m.actionNotice
		}
		return m.listView("New Work · repositories", contextLine, "space toggle  m mode  enter plan  h/esc back  / search  q quit")
	case workNewPlan:
		return m.detailView("j/k scroll  ctrl+u/d page  g/G top/bottom  enter/c create  h/esc back  q quit")
	case workSyncPlan:
		return m.detailView("j/k scroll  ctrl+u/d page  g/G top/bottom  enter/s sync  h/esc back  q quit")
	case workRemovePlan:
		return m.detailView("j/k scroll  ctrl+u/d page  g/G top/bottom  enter continue  h/esc cancel  q quit")
	case workRemoveConfirm:
		return m.removeWorkConfirmView()
	case workRepairPlan:
		return m.detailView("j/k scroll  ctrl+u/d page  g/G top/bottom  enter/r repair  h/esc back  q quit")
	case workOperation:
		return m.operationView()
	case workActionResult:
		return m.detailView("j/k scroll  ctrl+u/d page  g/G top/bottom  enter/h/esc back  q quit")
	default:
		return ""
	}
}

func (m workAppModel) listView(title, contextLine, hint string) string {
	return Title(terminalSafe(title, false)) + "\n" + subtitleStyle.Render(truncate(terminalSafe(contextLine, false), m.width)) + "\n\n" + m.list.View() + "\n" + hintStyle.Render(hint)
}

func (m workAppModel) detailView(hint string) string {
	subtitle := "Read-only details"
	switch m.screen {
	case workProblem:
		subtitle = "Read-only problem details"
	case workRepository:
		subtitle = "Observed Git and filesystem state"
	case workLoadError:
		subtitle = "Inspection did not complete"
	case workNewPlan:
		subtitle = "Review the exact mutation plan"
	case workSyncPlan:
		subtitle = "Review fetched immutable base commits"
	case workRemovePlan:
		subtitle = "Irreversible local deletion plan"
	case workRepairPlan:
		subtitle = "Only deterministic repairs are included"
	case workActionResult:
		subtitle = "Operation result"
	}
	return Title(m.detailTitle) + "\n" + subtitleStyle.Render(subtitle) + "\n\n" + m.viewport.View() + "\n" + hintStyle.Render(hint)
}

func (m workAppModel) removeWorkConfirmView() string {
	message := fmt.Sprintf("Type %s exactly. This deletes listed dirty files and unique local commits.", m.removeWorkPlan.WorkName)
	if m.actionNotice != "" {
		message = m.actionNotice
	}
	return Title("Remove Work · confirm") + "\n" + subtitleStyle.Render(message) + "\n\n" + m.input.View() + "\n\n" + hintStyle.Render("enter remove  esc cancel")
}

func (m workAppModel) newWorkNameView() string {
	message := "The same exact name is used for the Work directory and every local branch."
	if m.actionNotice != "" {
		message = m.actionNotice
	}
	return Title("New Work · name") + "\n" + subtitleStyle.Render(message) + "\n\n" + m.input.View() + "\n\n" + hintStyle.Render("enter continue  esc cancel")
}

func (m workAppModel) operationView() string {
	return Title(terminalSafe(m.operationTitle, false)) + "\n" + subtitleStyle.Render("Operation running") + "\n\n" + terminalSafe(m.operationMessage, true) + "\n\n" + hintStyle.Render("ctrl+c request cancellation")
}

func (m workAppModel) tooSmall() bool {
	return m.width < minimumWorkAppWidth || m.height < minimumWorkAppHeight
}

func (m *workAppModel) cancelLoad() {
	if m.cancel != nil {
		m.cancel()
	}
}

func workEntryTitle(entry inspectworks.Work) string {
	if entry.Name != "" {
		return terminalSafe(entry.Name, false)
	}
	return terminalSafe(filepath.Base(entry.RootPath), false)
}

func workEntryDescription(entry inspectworks.Work) string {
	if entry.Snapshot == nil {
		return fmt.Sprintf("not inspectable · %d problems", max(1, entry.ProblemCount()))
	}
	parts := []string{plural(len(entry.Snapshot.Repositories), "repository", "repositories")}
	dirty := 0
	for _, repository := range entry.Snapshot.Repositories {
		if repository.WorkingTreeKnown && repository.WorkingTree.Dirty() {
			dirty++
		}
	}
	if dirty > 0 {
		parts = append(parts, plural(dirty, "with local changes", "with local changes"))
	}
	if entry.Snapshot.Operation.ResumeSuggested {
		parts = append(parts, "Resume New Work available")
	}
	if count := entry.ProblemCount(); count > 0 {
		parts = append(parts, plural(count, "problem", "problems"))
	}
	return strings.Join(parts, " · ")
}

func repositoryDescription(repository inspectwork.RepositorySnapshot) string {
	parts := make([]string, 0, 3)
	if repository.WorkingTreeKnown && repository.WorkingTree.Dirty() {
		parts = append(parts, "local changes")
	}
	if len(repository.GitOperations) > 0 {
		operations := make([]string, len(repository.GitOperations))
		for index, operation := range repository.GitOperations {
			operations[index] = string(operation)
		}
		parts = append(parts, strings.Join(operations, ", ")+" active")
	}
	if len(repository.Problems) > 0 {
		parts = append(parts, plural(len(repository.Problems), "problem", "problems"))
	}
	if len(parts) == 0 {
		if repository.CheckoutKnown {
			parts = append(parts, shortRef(repository.Checkout.FullRef))
		} else {
			parts = append(parts, "checkout unknown")
		}
	}
	return strings.Join(parts, " · ")
}

func workContext(entry inspectworks.Work) string {
	parts := []string{entry.RootPath}
	if entry.Snapshot != nil {
		parts = append(parts, "intent: "+string(entry.Snapshot.IntentSource))
		parts = append(parts, "manifest: "+string(entry.Snapshot.Manifest.State))
		if entry.Snapshot.Operation.Phase != "" {
			parts = append(parts, "New Work: "+entry.Snapshot.Operation.Phase)
		}
	}
	return strings.Join(parts, " · ")
}

func formatRepository(repository inspectwork.RepositorySnapshot) string {
	var lines []string
	lines = append(lines,
		factLine("Destination", repository.Intent.Destination),
		factLine("Source", knownValue(repository.SourceKnown, repository.Source.SourcePath)),
		factLine("Expected branch", repository.Intent.BranchRef),
		factLine("Observed branch", knownValue(repository.CheckoutKnown, repository.Checkout.FullRef)),
		factLine("HEAD", knownValue(repository.CheckoutKnown, shortOID(repository.Checkout.HeadOID))),
		factLine("Registration", registrationValue(repository)),
		factLine("Working tree", workingTreeValue(repository)),
		factLine("Git operation", operationsValue(repository.GitOperationsKnown, repository.GitOperations)),
	)
	if len(repository.Problems) > 0 {
		lines = append(lines, "", "Problems")
		for _, problem := range repository.Problems {
			lines = append(lines, formatProblemLines(problem)...)
		}
	}
	return strings.Join(lines, "\n")
}

func formatCollectionProblem(problem inspectworks.Problem) string {
	lines := []string{factLine("Code", string(problem.Code))}
	if problem.Name != "" {
		lines = append(lines, factLine("Work", problem.Name))
	}
	if problem.Path != "" {
		lines = append(lines, factLine("Path", problem.Path))
	}
	lines = append(lines, "", problem.Message)
	return strings.Join(lines, "\n")
}

func formatWorkProblem(problem inspectwork.Problem) string {
	return strings.Join(formatProblemLines(problem), "\n")
}

func formatProblemLines(problem inspectwork.Problem) []string {
	lines := []string{"[" + string(problem.Code) + "] " + problem.Message}
	if problem.Path != "" {
		lines = append(lines, "Path: "+problem.Path)
	}
	if problem.Next != inspectwork.ActionNone {
		lines = append(lines, "Next: "+string(problem.Next))
	}
	return append(lines, "")
}

func formatLoadError(err error) string {
	return err.Error() + "\n\nCheck configuration with `goworktree config show` or initialize it with `goworktree init`."
}

func problemDescription(problem inspectwork.Problem) string {
	if problem.Next == inspectwork.ActionNone {
		return compactMessage(problem.Message)
	}
	return compactMessage(problem.Message) + " · next: " + string(problem.Next)
}

func workingTreeValue(repository inspectwork.RepositorySnapshot) string {
	if !repository.WorkingTreeKnown {
		return "unknown"
	}
	status := repository.WorkingTree
	if !status.Dirty() {
		if status.Ignored > 0 {
			return fmt.Sprintf("no tracked changes · ignored %d", status.Ignored)
		}
		return "no local changes"
	}
	parts := make([]string, 0, 6)
	appendCount := func(count int, label string) {
		if count > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", label, count))
		}
	}
	appendCount(status.Staged, "staged")
	appendCount(status.Unstaged, "unstaged")
	appendCount(status.Untracked, "untracked")
	appendCount(status.Conflicted, "conflicted")
	appendCount(status.DirtySubmodules, "dirty submodules")
	appendCount(status.Ignored, "ignored")
	return strings.Join(parts, " · ")
}

func registrationValue(repository inspectwork.RepositorySnapshot) string {
	if !repository.RegistrationsKnown {
		return "unknown"
	}
	if repository.DestinationRegistration == nil {
		return "missing"
	}
	registration := repository.DestinationRegistration
	parts := []string{shortRef(registration.Branch), shortOID(registration.HeadOID)}
	if registration.Locked {
		parts = append(parts, "locked")
	}
	if registration.Prunable {
		parts = append(parts, "prunable")
	}
	return strings.Join(parts, " · ")
}

func operationsValue(known bool, operations []gitops.ActiveOperation) string {
	if !known {
		return "unknown"
	}
	if len(operations) == 0 {
		return "none"
	}
	values := make([]string, 0, len(operations))
	for _, operation := range operations {
		values = append(values, string(operation))
	}
	sort.Strings(values)
	return strings.Join(values, ", ")
}

func knownValue(known bool, value string) string {
	if !known {
		return "unknown"
	}
	if value == "" {
		return "none"
	}
	return value
}

func factLine(label, value string) string {
	return fmt.Sprintf("%-16s %s", label+":", value)
}

func plural(count int, one, many string) string {
	label := many
	if count == 1 {
		label = one
	}
	return fmt.Sprintf("%d %s", count, label)
}

func shortOID(oid string) string {
	if len(oid) > 12 {
		return oid[:12]
	}
	return oid
}

func shortRef(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

func compactMessage(message string) string {
	return strings.Join(strings.Fields(terminalSafe(message, false)), " ")
}

func terminalSafe(value string, multiline bool) string {
	var result strings.Builder
	for _, char := range value {
		if char == '\n' && multiline {
			result.WriteRune(char)
			continue
		}
		if char == '\t' {
			result.WriteByte(' ')
			continue
		}
		if unicode.IsControl(char) {
			result.WriteRune('�')
			continue
		}
		result.WriteRune(char)
	}
	return result.String()
}

func truncate(value string, width int) string {
	if width <= 1 || lipgloss.Width(value) <= width-1 {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width-1 {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func inspectedLabel(value time.Time) string {
	if value.IsZero() {
		return "not inspected"
	}
	return "inspected " + value.Local().Format("15:04:05")
}
