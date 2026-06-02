package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crowl/ronin/agent"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tui/internal/editor"
	"github.com/crowl/ronin/tui/internal/terminal"
)

type pendingTextDeltaKind int

const (
	pendingTextDeltaNone pendingTextDeltaKind = iota
	pendingTextDeltaThinking
	pendingTextDeltaAssistant
)

type appModel struct {
	editor *editor.Readline
	menu   *menuState
	theme  Theme

	boxes          []box
	boxLineCache   boxLineCache
	statusBarCache statusBarCache

	working       bool
	workingLabel  string
	toolsExpanded bool

	steeringPrompt string

	indicatorFrame int

	pendingTextDeltaKind pendingTextDeltaKind
	pendingTextDelta     strings.Builder
}

type modelUpdate struct {
	Render bool
	Action modelAction
}

type noAction struct{}

type exitAction struct{}

type cancelPromptAction struct{}

type submitPromptAction struct{ Prompt string }

type runCommandAction struct {
	Item    menuItem
	Command Command
}

type modelAction interface{ modelAction() }

func (noAction) modelAction()           {}
func (exitAction) modelAction()         {}
func (cancelPromptAction) modelAction() {}
func (submitPromptAction) modelAction() {}
func (runCommandAction) modelAction()   {}

func newAppModel(commands []Command, theme Theme) (*appModel, error) {
	appMenu, err := newMenu(commands)
	if err != nil {
		return nil, fmt.Errorf("create menu: %w", err)
	}

	return &appModel{
		editor: editor.New(),
		menu:   appMenu,
		theme:  theme,
		boxes:  make([]box, 0, defaultInitialBoxesCapacity),
	}, nil
}

func (m *appModel) handleKey(key terminal.Key) (modelUpdate, error) {
	if key.Type == terminal.KeyCtrlC {
		if m.working {
			m.boxes = append(m.boxes, errorMessageBox{Text: "Operation cancelled"})
			return modelUpdate{Render: true, Action: cancelPromptAction{}}, nil
		}
		return modelUpdate{Action: exitAction{}}, nil
	}

	if key.Type == terminal.KeyEscape {
		if m.working {
			m.boxes = append(m.boxes, errorMessageBox{Text: "Operation canceled"})
			return modelUpdate{Render: true, Action: cancelPromptAction{}}, nil
		}
	}

	if key.Type == terminal.KeyCtrlO {
		m.toolsExpanded = !m.toolsExpanded
		return modelUpdate{Render: true}, nil
	}

	menuFx, err := m.menu.HandleKey(key)
	if err != nil {
		return modelUpdate{}, fmt.Errorf("failed to handle menu key: %w", err)
	}
	switch fx := menuFx.(type) {
	case hideMenu:
		m.menu.Hide()
		m.editor.Clear()
		return modelUpdate{Render: true}, nil
	case menuItemChange:
		return modelUpdate{Render: true}, nil
	case menuItemPreSelected:
		m.editor.SetText(fx.MenuItem.Value)
		return modelUpdate{Render: true}, nil
	case menuItemSelected:
		m.editor.Clear()
		m.menu.Hide()
		return modelUpdate{
			Render: true,
			Action: runCommandAction{
				Item:    fx.MenuItem,
				Command: fx.MenuItem.Command,
			},
		}, nil
	}

	editorFx, err := m.editor.HandleKey(key)
	if err != nil {
		return modelUpdate{}, fmt.Errorf("failed to handle editor key: %w", err)
	}
	switch fx := editorFx.(type) {
	case editor.CursorMove:
		return modelUpdate{Render: true}, nil
	case editor.TextChange:
		m.menu.SetQuery(fx.Text)
		return modelUpdate{Render: true}, nil
	case editor.SubmitPrompt:
		m.editor.Clear()
		return modelUpdate{Render: true, Action: submitPromptAction{Prompt: fx.Prompt}}, nil
	}

	return modelUpdate{}, nil
}

func (m *appModel) startPrompt(prompt string) {
	m.boxes = append(m.boxes, userMessageBox{Text: prompt})
	m.working = true
	m.workingLabel = "Working"
	m.indicatorFrame = 0
}

func (m *appModel) startCompaction(item menuItem) {
	m.boxes = append(m.boxes, systemMessageBox{Text: item.Value})
	m.working = true
	m.workingLabel = "Compacting"
	m.indicatorFrame = 0
}

func (m *appModel) queueSteeringPrompt(prompt string) {
	if m.steeringPrompt == "" {
		m.steeringPrompt = prompt
		return
	}
	m.steeringPrompt += "\n\n" + prompt
}

func (m *appModel) finishCompaction(err error) modelUpdate {
	m.working = false
	m.statusBarCache.Reset()

	if err != nil && !errors.Is(err, context.Canceled) {
		m.boxes = append(m.boxes, errorMessageBox{Text: err.Error()})
	}

	nextPrompt := m.steeringPrompt
	m.steeringPrompt = ""

	update := modelUpdate{Render: true}
	if nextPrompt != "" {
		update.Action = submitPromptAction{Prompt: nextPrompt}
	}
	return update
}

func (m *appModel) finishPrompt() (modelUpdate, string) {
	m.flushPendingTextDelta()
	m.working = false
	m.statusBarCache.Reset()

	nextPrompt := m.steeringPrompt
	m.steeringPrompt = ""

	update := modelUpdate{Render: true}
	if nextPrompt != "" {
		update.Action = submitPromptAction{Prompt: nextPrompt}
	}
	return update, nextPrompt
}

func (m *appModel) handleAgentEvent(event agent.Event, now time.Time) (modelUpdate, error) {
	switch typedEvent := event.(type) {
	case agent.PromptProcessingStarted:
		// ignored
	case agent.ConversationTurnStarted:
		// ignored
	case agent.AssistantMessageStarted:
		// ignored
	case agent.AssistantThinkingDeltaReceived:
		m.queueTextDelta(pendingTextDeltaThinking, typedEvent.Text)
	case agent.AssistantMessageDeltaReceived:
		m.queueTextDelta(pendingTextDeltaAssistant, typedEvent.Text)
	case agent.AssistantMessageEnded:
		m.flushPendingTextDelta()
	case agent.ToolExecutionStarted:
		m.flushPendingTextDelta()
		m.boxes = append(m.boxes, toolCallBox{
			ToolCallID: typedEvent.CallID,
			Title:      typedEvent.CallTitle,
			StartedAt:  now,
		})
	case agent.ToolExecutionOutputDeltaReceived:
		m.flushPendingTextDelta()
		index := findToolBlockIndex(m.boxes, typedEvent.CallID)
		if index == -1 {
			return modelUpdate{}, fmt.Errorf("expected tool block to exist for call id %q (%q)", typedEvent.CallID, typedEvent.Tool.Name())
		}
		toolCallBox, ok := m.boxes[index].(toolCallBox)
		if !ok {
			return modelUpdate{}, fmt.Errorf("expected tool call box at index %d, got %T", index, m.boxes[index])
		}
		toolCallBox.Artifacts = append(toolCallBox.Artifacts, typedEvent.Artifact)
		m.boxes[index] = toolCallBox
	case agent.ToolExecutionResultReceived:
		m.flushPendingTextDelta()
		if len(typedEvent.Artifacts) == 0 {
			return modelUpdate{}, nil
		}
		index := findToolBlockIndex(m.boxes, typedEvent.CallID)
		if index == -1 {
			return modelUpdate{}, fmt.Errorf("expected tool block to exist for call id %q (%q)", typedEvent.CallID, typedEvent.Tool.Name())
		}
		toolCallBox, ok := m.boxes[index].(toolCallBox)
		if !ok {
			return modelUpdate{}, fmt.Errorf("expected tool call box at index %d, got %T", index, m.boxes[index])
		}
		toolCallBox.Artifacts = append([]tool.Artifact(nil), typedEvent.Artifacts...)
		m.boxes[index] = toolCallBox
	case agent.ToolExecutionFailed:
		m.flushPendingTextDelta()
		index := findToolBlockIndex(m.boxes, typedEvent.CallID)
		if index == -1 {
			return modelUpdate{}, fmt.Errorf("expected tool block for call id %q (%q) to exist", typedEvent.CallID, typedEvent.Tool.Name())
		}
		toolCallBox, ok := m.boxes[index].(toolCallBox)
		if !ok {
			return modelUpdate{}, fmt.Errorf("expected tool call box at index %d, got %T", index, m.boxes[index])
		}
		toolCallBox.Error += typedEvent.Error.Error()
		if toolCallBox.StartedAt.IsZero() {
			toolCallBox.StartedAt = now
		}
		toolCallBox.EndedAt = now
		m.boxes[index] = toolCallBox
	case agent.ToolExecutionEnded:
		m.flushPendingTextDelta()
		index := findToolBlockIndex(m.boxes, typedEvent.CallID)
		if index == -1 {
			return modelUpdate{}, fmt.Errorf("expected tool block for call id %q (%q) to exist", typedEvent.CallID, typedEvent.Tool.Name())
		}
		toolCallBox, ok := m.boxes[index].(toolCallBox)
		if !ok {
			return modelUpdate{}, fmt.Errorf("expected tool call box at index %d, got %T", index, m.boxes[index])
		}
		if toolCallBox.StartedAt.IsZero() {
			toolCallBox.StartedAt = now
		}
		toolCallBox.EndedAt = now
		m.boxes[index] = toolCallBox
	case agent.ConversationTurnEnded:
		// ignored
	case agent.PromptProcessingEnded:
		// ignored
	case agent.PromptProcessingError:
		m.flushPendingTextDelta()
	}

	return modelUpdate{Render: true}, nil
}

func (m *appModel) handleAgentError(err error) modelUpdate {
	if errors.Is(err, context.Canceled) {
		return modelUpdate{}
	}

	m.flushPendingTextDelta()

	m.boxes = append(m.boxes, errorMessageBox{
		Text: err.Error(),
	})

	return modelUpdate{Render: true}
}

func (m *appModel) tickWorking() modelUpdate {
	if !m.working {
		return modelUpdate{}
	}
	m.indicatorFrame++
	return modelUpdate{Render: true}
}

func (m *appModel) recordCommand(item menuItem, err error) {
	m.boxes = append(m.boxes, systemMessageBox{
		Text: item.Value,
	})
	if err != nil {
		m.boxes = append(m.boxes, errorMessageBox{
			Text: err.Error(),
		})
	}
}

func (m *appModel) lines(width int, agent Agent, now time.Time) ([]string, error) {
	m.flushPendingTextDelta()

	lines := []string{""}
	lines = append(lines, m.boxLineCache.Lines(m.boxes, width, m.theme, m.toolsExpanded, now)...)

	if m.steeringPrompt != "" {
		lines = append(lines, pendingSteeringPresenter{
			Text: m.steeringPrompt,
		}.Lines(width, m.theme)...)
	}

	if m.working {
		lines = append(lines, workingIndicator{
			Frame: m.indicatorFrame,
			Label: m.workingLabel,
		}.Lines(width, m.theme)...)
	}

	lines = append(lines, editorPresenter{
		Text:   m.editor.Text(),
		Cursor: m.editor.Cursor(),
	}.Lines(width, m.theme)...)

	if m.menu.Shown() {
		lines = append(lines, menuPresenter{
			SelectedIndex: m.menu.SelectedIndex(),
			Items:         m.menu.Items(),
		}.Lines(width, m.theme)...)
	}

	cwdStatus := m.statusBarCache.CWDStatus(agent.CWD())

	lines = append(lines, statusBar{
		CWD:            agent.CWD(),
		CWDStatus:      cwdStatus,
		UseCWDStatus:   true,
		Model:          agent.Model(),
		ReasoningLevel: agent.ReasoningLevel(),
		ContextUsage:   agent.ContextUsage(),
	}.Lines(width, m.theme)...)

	return lines, nil
}

func (m *appModel) queueTextDelta(kind pendingTextDeltaKind, text string) {
	if text == "" {
		return
	}
	if m.pendingTextDeltaKind != pendingTextDeltaNone && m.pendingTextDeltaKind != kind {
		m.flushPendingTextDelta()
	}
	m.pendingTextDeltaKind = kind
	m.pendingTextDelta.WriteString(text)
}

func (m *appModel) flushPendingTextDelta() {
	if m.pendingTextDeltaKind == pendingTextDeltaNone {
		return
	}

	text := m.pendingTextDelta.String()
	m.pendingTextDelta.Reset()
	kind := m.pendingTextDeltaKind
	m.pendingTextDeltaKind = pendingTextDeltaNone

	switch kind {
	case pendingTextDeltaNone:
		// Ignored
	case pendingTextDeltaThinking:
		if len(m.boxes) == 0 {
			m.boxes = append(m.boxes, assistantThinkingBox{Text: text})
			return
		}
		index := len(m.boxes) - 1
		thinkingBox, ok := m.boxes[index].(assistantThinkingBox)
		if !ok {
			m.boxes = append(m.boxes, assistantThinkingBox{Text: text})
			return
		}
		thinkingBox.Text += text
		m.boxes[index] = thinkingBox
	case pendingTextDeltaAssistant:
		if len(m.boxes) == 0 {
			m.boxes = append(m.boxes, assistantMessageBox{Text: text})
			return
		}
		index := len(m.boxes) - 1
		messageBox, ok := m.boxes[index].(assistantMessageBox)
		if !ok {
			m.boxes = append(m.boxes, assistantMessageBox{Text: text})
			return
		}
		messageBox.Text += text
		m.boxes[index] = messageBox
	}
}

func findToolBlockIndex(blocks []box, toolCallID string) int {
	for i := len(blocks) - 1; i >= 0; i-- {
		switch typedBlock := blocks[i].(type) {
		case toolCallBox:
			if typedBlock.ToolCallID == toolCallID {
				return i
			}
		}
	}
	return -1
}
