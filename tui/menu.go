package tui

import (
	"fmt"
	"strings"

	"github.com/crowl/ronin/tui/internal/terminal"
)

func newMenu(commands []Command) (*menuState, error) {
	if len(commands) == 0 {
		return nil, fmt.Errorf("no commands provided")
	}

	items := make([]menuItem, 0, len(commands))
	for i, command := range commands {
		switch typedCommand := command.(type) {
		case CompactConversation:
			items = append(items, menuItem{
				Index:       i,
				Name:        "/compact",
				Value:       "/compact",
				Description: "compact the conversation context",
				Command:     typedCommand,
			})
		case StartNewConversation:
			items = append(items, menuItem{
				Index:       i,
				Name:        "/new",
				Value:       "/new",
				Description: "start a fresh conversation",
				Command:     typedCommand,
			})
		case SwitchModel:
			argument := typedCommand.Model.String()
			items = append(items, menuItem{
				Index:       i,
				Name:        "/model",
				Argument:    argument,
				Value:       "/model " + argument,
				Description: "switch model to " + argument,
				Command:     typedCommand,
			})
		case SwitchReasoningLevel:
			argument := string(typedCommand.Level)
			items = append(items, menuItem{
				Index:       i,
				Name:        "/reasoning",
				Argument:    argument,
				Value:       "/reasoning " + argument,
				Description: "switch reasoning level to " + argument,
				Command:     typedCommand,
			})
		case InvokeSkill:
			items = append(items, menuItem{
				Index:       i,
				Name:        "/skill:" + typedCommand.Skill.Name,
				Value:       "/skill:" + typedCommand.Skill.Name,
				Description: typedCommand.Skill.Description,
				Command:     typedCommand,
			})
		case InvokeWorkflow:
			items = append(items, menuItem{
				Index:       i,
				Name:        "/workflow:" + typedCommand.Workflow.Name,
				Value:       "/workflow:" + typedCommand.Workflow.Name,
				Description: "run the " + typedCommand.Workflow.Name + " workflow",
				Command:     typedCommand,
			})
		case Exit:
			items = append(items, menuItem{
				Index:       i,
				Name:        "/exit",
				Value:       "/exit",
				Description: "exit from ronin",
				Command:     typedCommand,
			})
		}
	}

	return &menuState{
		shown:         false,
		selectedIndex: 0,
		items:         items,
		query:         "",
	}, nil
}

type menuItem struct {
	Index       int
	Name        string
	Argument    string
	Value       string
	Description string
	Command     Command
}

func (m menuItem) displayParts() (string, string) {
	if m.Name != "" || m.Argument != "" {
		return m.Name, m.Argument
	}
	name, argument, ok := strings.Cut(m.Value, " ")
	if !ok {
		return m.Value, ""
	}
	return name, argument
}

type menuState struct {
	shown         bool
	selectedIndex int
	items         []menuItem
	query         string
}

func (m *menuState) Shown() bool {
	return m.shown
}

func (m *menuState) Show() {
	m.shown = true
}

func (m *menuState) Hide() {
	m.selectedIndex = 0
	m.query = ""
	m.shown = false
}

func (m *menuState) SetQuery(query string) {
	query = strings.TrimSpace(query)
	m.selectedIndex = 0
	m.query = query
	switch query {
	case "":
		m.Hide()
	case "/":
		m.Show()
	default:
	}
}

func (m *menuState) SelectedIndex() int {
	return m.selectedIndex
}

func (m *menuState) Items() []menuItem {
	if m.query == "" || m.query == "/" {
		return m.items
	}
	return m.filteredMenuItems()
}

func (m *menuState) HandleKey(key terminal.Key) (menuEffect, error) {
	if !m.shown {
		return noMenuEffect{}, nil
	}
	switch key.Type {
	case terminal.KeyEscape:
		return hideMenu{}, nil
	case terminal.KeyArrowUp:
		m.moveSelection(-1)
		return menuItemChange{}, nil
	case terminal.KeyArrowDown:
		m.moveSelection(1)
		return menuItemChange{}, nil
	case terminal.KeyTab:
		item, err := m.selectedItem()
		if err != nil {
			return noMenuEffect{}, nil
		}
		return menuItemPreSelected{MenuItem: item}, nil
	case terminal.KeyEnter:
		item, err := m.selectedItem()
		if err != nil {
			return noMenuEffect{}, nil
		}
		return menuItemSelected{MenuItem: item}, nil
	default:
		return noMenuEffect{}, nil
	}
}

func (m *menuState) moveSelection(delta int) {
	items := m.Items()
	if !m.shown || len(items) == 0 {
		return
	}
	m.selectedIndex = (m.selectedIndex + delta + len(items)) % len(items)
}

func (m *menuState) selectedItem() (menuItem, error) {
	items := m.Items()
	if !m.shown || len(items) == 0 || m.selectedIndex < 0 || m.selectedIndex >= len(items) {
		return menuItem{}, fmt.Errorf("invalid selected index: %d", m.selectedIndex)
	}
	return items[m.selectedIndex], nil
}

func (m *menuState) filteredMenuItems() []menuItem {
	prefix := m.query
	var prefixMatches []menuItem
	var fuzzyMatches []menuItem
	for _, item := range m.items {
		if strings.HasPrefix(item.Value, prefix) {
			prefixMatches = append(prefixMatches, item)
			continue
		}
		if matchesSubsequence(
			strings.TrimPrefix(prefix, "/"),
			strings.TrimPrefix(item.Value, "/"),
		) {
			fuzzyMatches = append(fuzzyMatches, item)
		}
	}
	return append(prefixMatches, fuzzyMatches...)
}

func matchesSubsequence(query string, candidate string) bool {
	query = strings.ToLower(query)
	candidate = strings.ToLower(candidate)
	if query == "" {
		return true
	}
	j := 0
	for _, r := range candidate {
		if j < len(query) && byte(r) == query[j] {
			j++
		}
		if j == len(query) {
			return true
		}
	}
	return false
}
