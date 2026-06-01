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
				Value:       "/compact",
				Description: "compact the conversation context",
				Command:     typedCommand,
			})
		case StartNewConversation:
			items = append(items, menuItem{
				Index:       i,
				Value:       "/new",
				Description: "start a fresh conversation",
				Command:     typedCommand,
			})
		case SwitchModel:
			items = append(items, menuItem{
				Index:       i,
				Value:       "/model " + typedCommand.Model.String(),
				Description: "switch model to " + typedCommand.Model.String(),
				Command:     typedCommand,
			})
		case SwitchReasoningLevel:
			items = append(items, menuItem{
				Index:       i,
				Value:       "/reasoning " + string(typedCommand.Level),
				Description: "switch reasoning level to " + string(typedCommand.Level),
				Command:     typedCommand,
			})
		case InvokeSkill:
			items = append(items, menuItem{
				Index:       i,
				Value:       "/skill:" + typedCommand.Skill.Name,
				Description: typedCommand.Skill.Description,
				Command:     typedCommand,
			})
		case Exit:
			items = append(items, menuItem{
				Index:       i,
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
	Value       string
	Description string
	Command     Command
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
	switch query {
	case "":
		m.Hide()
	case "/":
		m.Show()
	default:
		m.query = query
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
			return noMenuEffect{}, err
		}
		return menuItemPreSelected{MenuItem: item}, nil
	case terminal.KeyEnter:
		item, err := m.selectedItem()
		if err != nil {
			return noMenuEffect{}, err
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
