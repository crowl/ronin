package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crowl/ronin/agent"
	"github.com/crowl/ronin/llm"
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

	saveError string

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

func (m *appModel) populateInitialBoxes(agent Agent) {
	if agent == nil {
		return
	}
	messages := agent.Messages()
	for _, message := range messages {
		switch msg := message.(type) {
		case llm.UserMessage:
			m.boxes = append(m.boxes, userMessageBox{Text: msg.Text})
		case llm.ErrorMessage:
			m.boxes = append(m.boxes, errorMessageBox{Text: msg.Error.Error()})
		case llm.AssistantMessage:
			for _, b := range msg.Blocks {
				switch block := b.(type) {
				case llm.TextBlock:
					if len(m.boxes) > 0 {
						if lastBox, ok := m.boxes[len(m.boxes)-1].(assistantMessageBox); ok {
							lastBox.Text += block.Text
							m.boxes[len(m.boxes)-1] = lastBox
							continue
						}
					}
					m.boxes = append(m.boxes, assistantMessageBox{Text: block.Text})
				case llm.ThinkingBlock:
					if len(m.boxes) > 0 {
						if lastBox, ok := m.boxes[len(m.boxes)-1].(assistantThinkingBox); ok {
							lastBox.Text += block.Text
							m.boxes[len(m.boxes)-1] = lastBox
							continue
						}
					}
					m.boxes = append(m.boxes, assistantThinkingBox{Text: block.Text})
				case llm.ToolCallBlock:
					title := agent.ToolCallTitle(block.Name, block.Arguments)
					m.boxes = append(m.boxes, toolCallBox{
						ToolCallID: block.ID,
						Title:      title,
						StartedAt:  msg.Timestamp,
						EndedAt:    msg.Timestamp,
					})
				}
			}
		case llm.ToolOutputMessage:
			index := findToolBlockIndex(m.boxes, msg.ToolCallID)
			if index != -1 {
				if tBox, ok := m.boxes[index].(toolCallBox); ok {
					tBox.EndedAt = msg.Timestamp
					m.boxes[index] = tBox
				}
			}
		case llm.ToolErrorMessage:
			index := findToolBlockIndex(m.boxes, msg.ToolCallID)
			if index != -1 {
				if tBox, ok := m.boxes[index].(toolCallBox); ok {
					tBox.EndedAt = msg.Timestamp
					tBox.Error = msg.Error.Error()
					m.boxes[index] = tBox
				}
			}
		}
	}
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
	m.saveError = ""
}

func (m *appModel) startCompaction(item menuItem) {
	m.boxes = append(m.boxes, systemMessageBox{Text: item.Value})
	m.working = true
	m.workingLabel = "Compacting"
	m.indicatorFrame = 0
}

func (m *appModel) startNewConversation() {
	m.pendingTextDelta.Reset()
	m.pendingTextDeltaKind = pendingTextDeltaNone
	m.boxes = []box{systemMessageBox{Text: newConversationStartedMessage}}
	m.boxLineCache.Reset()
	m.saveError = ""
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
	case agent.SessionSaveFailed:
		m.flushPendingTextDelta()
		m.saveError = typedEvent.Error.Error()
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
		toolCallBox.Artifacts = appendToolArtifact(toolCallBox.Artifacts, typedEvent.Artifact)
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

func (m *appModel) setTheme(theme Theme) bool {
	if theme.Empty() {
		return false
	}
	m.theme = theme
	return true
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
	if m.saveError != "" {
		cwdStatus = fmt.Sprintf("%s (Save Error: %s)", cwdStatus, m.saveError)
	}

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

func appendToolArtifact(artifacts []tool.Artifact, artifact tool.Artifact) []tool.Artifact {
	streamArtifact, ok := artifact.(tool.ShellStreamArtifact)
	if !ok || len(artifacts) == 0 {
		return append(artifacts, artifact)
	}

	previousStreamArtifact, ok := artifacts[len(artifacts)-1].(tool.ShellStreamArtifact)
	if !ok || previousStreamArtifact.Stream != streamArtifact.Stream {
		return append(artifacts, artifact)
	}

	previousStreamArtifact.Content += streamArtifact.Content
	artifacts[len(artifacts)-1] = previousStreamArtifact
	return artifacts
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
