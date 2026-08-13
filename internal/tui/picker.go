package tui

import (
	"errors"
	"fmt"
	"io"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ErrCancelled is returned when the user aborts a picker or prompt with esc/q.
var ErrCancelled = errors.New("cancelled")

// IsCancelled reports whether err is or wraps ErrCancelled.
func IsCancelled(err error) bool {
	return errors.Is(err, ErrCancelled)
}

// applyVimListKeys sets shared j/k/g/G// bindings on a bubbles list.
func applyVimListKeys(l *list.Model) {
	l.KeyMap.CursorUp = key.NewBinding(key.WithKeys("k", "up", "ctrl+p"), key.WithHelp("k", "up"))
	l.KeyMap.CursorDown = key.NewBinding(key.WithKeys("j", "down", "ctrl+n"), key.WithHelp("j", "down"))
	l.KeyMap.GoToStart = key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top"))
	l.KeyMap.GoToEnd = key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom"))
	l.KeyMap.PrevPage = key.NewBinding(key.WithKeys("ctrl+u", "pgup"), key.WithHelp("ctrl+u", "page up"))
	l.KeyMap.NextPage = key.NewBinding(key.WithKeys("ctrl+d", "pgdown"), key.WithHelp("ctrl+d", "page down"))
	l.KeyMap.Filter = key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter"))
}

// Item is a selectable row in Pick / PickMulti.
type Item struct {
	ID    string
	Title string
	Desc  string
}

func (i Item) FilterValue() string {
	return i.Title + " " + i.Desc
}

type itemDelegate struct {
	multi    bool
	selected map[string]struct{}
}

func (d itemDelegate) Height() int                               { return 2 }
func (d itemDelegate) Spacing() int                              { return 1 }
func (d itemDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }

func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	it, ok := listItem.(Item)
	if !ok {
		return
	}

	mark := "  "
	if d.multi {
		if _, on := d.selected[it.ID]; on {
			mark = okStyle.Render("✓ ")
		} else {
			mark = descStyle.Render("○ ")
		}
	}

	cursor := "  "
	titleStyle := lipgloss.NewStyle().Bold(true)
	desc := descStyle.Render(it.Desc)
	title := titleStyle.Render(it.Title)
	if index == m.Index() {
		cursor = labelStyle.Render("› ")
		title = labelStyle.Underline(true).Bold(true).Render(it.Title)
	}

	_, _ = fmt.Fprintf(w, "%s%s%s\n    %s", cursor, mark, title, desc)
}

type pickerKeyMap struct {
	Toggle, Confirm, Cancel key.Binding
}

type pickerModel struct {
	list     list.Model
	keys     pickerKeyMap
	multi    bool
	selected map[string]struct{}
	result   []string
	cancel   bool
	quitting bool
}

func newPicker(title string, items []Item, multi bool) pickerModel {
	listItems := make([]list.Item, len(items))
	for i, it := range items {
		listItems[i] = it
	}

	selected := map[string]struct{}{}
	delegate := itemDelegate{multi: multi, selected: selected}

	l := list.New(listItems, delegate, 80, 22)
	l.Title = title
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(true)
	l.Styles.Title = titleStyle

	applyVimListKeys(&l)
	// Handle quit ourselves so we can distinguish cancel vs confirm.
	l.KeyMap.Quit = key.NewBinding()
	l.KeyMap.ForceQuit = key.NewBinding()

	keys := pickerKeyMap{
		Toggle:  key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
		Confirm: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		Cancel:  key.NewBinding(key.WithKeys("esc", "q", "ctrl+c"), key.WithHelp("esc", "cancel")),
	}

	l.AdditionalShortHelpKeys = func() []key.Binding {
		if multi {
			return []key.Binding{keys.Toggle, keys.Confirm, keys.Cancel}
		}
		return []key.Binding{keys.Confirm, keys.Cancel}
	}
	l.AdditionalFullHelpKeys = l.AdditionalShortHelpKeys

	return pickerModel{
		list:     l,
		keys:     keys,
		multi:    multi,
		selected: selected,
	}
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h := msg.Height - 2
		if h < 8 {
			h = 8
		}
		m.list.SetSize(msg.Width, h)
		return m, nil

	case tea.KeyMsg:
		// Always honor hard interrupt; esc/q while filtering clear the filter via list.
		if msg.String() == "ctrl+c" {
			m.cancel = true
			m.quitting = true
			return m, tea.Quit
		}
		if m.list.FilterState() == list.Filtering {
			break
		}
		switch {
		case key.Matches(msg, m.keys.Cancel):
			m.cancel = true
			m.quitting = true
			return m, tea.Quit
		case key.Matches(msg, m.keys.Confirm):
			if m.multi {
				if len(m.selected) == 0 {
					if it, ok := m.list.SelectedItem().(Item); ok {
						m.selected[it.ID] = struct{}{}
					}
				}
				ids := make([]string, 0, len(m.selected))
				for _, li := range m.list.Items() {
					it, ok := li.(Item)
					if !ok {
						continue
					}
					if _, on := m.selected[it.ID]; on {
						ids = append(ids, it.ID)
					}
				}
				m.result = ids
			} else if it, ok := m.list.SelectedItem().(Item); ok {
				m.result = []string{it.ID}
			}
			if len(m.result) == 0 {
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		case m.multi && key.Matches(msg, m.keys.Toggle):
			if it, ok := m.list.SelectedItem().(Item); ok {
				if _, on := m.selected[it.ID]; on {
					delete(m.selected, it.ID)
				} else {
					m.selected[it.ID] = struct{}{}
				}
				m.list.SetDelegate(itemDelegate{multi: true, selected: m.selected})
			}
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m pickerModel) View() string {
	if m.quitting {
		return ""
	}
	hint := "j/k move  enter ok  / filter  esc cancel"
	if m.multi {
		hint = "j/k move  space toggle  enter ok  / filter  esc cancel"
	}
	return m.list.View() + "\n" + hintStyle.Render(hint)
}

// Pick shows a single-select list. Returns cancelled error on abort.
func Pick(title string, items []Item) (string, error) {
	if len(items) == 0 {
		return "", fmt.Errorf("nothing to select")
	}
	m := newPicker(title, items, false)
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return "", err
	}
	pm := final.(pickerModel)
	if pm.cancel {
		return "", ErrCancelled
	}
	if len(pm.result) == 0 {
		return "", fmt.Errorf("nothing selected")
	}
	return pm.result[0], nil
}

// PickMulti shows a multi-select list with Space to toggle.
func PickMulti(title string, items []Item) ([]string, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("nothing to select")
	}
	m := newPicker(title, items, true)
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return nil, err
	}
	pm := final.(pickerModel)
	if pm.cancel {
		return nil, ErrCancelled
	}
	if len(pm.result) == 0 {
		return nil, fmt.Errorf("nothing selected")
	}
	return pm.result, nil
}

// PickerKeyBindings documents the vim-style bindings for tests.
func PickerKeyBindings(multi bool) []string {
	keys := []string{"j", "k", "g", "G", "/", "enter", "esc", "q"}
	if multi {
		keys = append(keys, "space")
	}
	return keys
}
