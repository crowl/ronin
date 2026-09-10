package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/shell"
	"github.com/crowl/ronin/tui/internal/editor"
	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/workflow"
)

type pendingTextDeltaKind int

const (
	pendingTextDeltaNone pendingTextDeltaKind = iota
	pendingTextDeltaThinking
	pendingTextDeltaAssistant

	maxWorkflowNameSize   = 256
	maxWorkflowStatusSize = 32
	maxWorkflowDetailSize = 128 * 1024

	maxWorkflowSummaryBytes       = 64 * 1024
	maxWorkflowVisualLines        = 1000
	maxWorkflowSummaryLines       = 6
	maxWorkflowRecentSteps        = 5
	maxWorkflowStepErrorSize      = 1024
	maxWorkflowLatestActivitySize = 256
)

type appModel struct {
	editor *editor.Readline
	menu   *menuState

	boxes          []box
	boxLineCache   boxLineCache
	statusBarCache statusBarCache

	usage         llm.Usage
	sessionUsage  llm.Usage
	working       bool
	workingLabel  string
	toolsExpanded bool

	shellRunning     bool
	shellOutputIndex int
	steeringPrompt   string
	workflowInput    *workflow.Workflow

	indicatorFrame int

	saveError string

	confirmRewind *confirmRewindAction
	commandItems  []menuItem

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

type runWorkflowAction struct {
	Workflow workflow.Workflow
	Input    string
}

type confirmRewindAction struct {
	Item         menuItem
	Point        runtime.RewindPoint
	RemovedTurns int
}

type modelAction interface{ modelAction() }

func (noAction) modelAction()            {}
func (exitAction) modelAction()          {}
func (cancelPromptAction) modelAction()  {}
func (submitPromptAction) modelAction()  {}
func (runCommandAction) modelAction()    {}
func (runWorkflowAction) modelAction()   {}
func (confirmRewindAction) modelAction() {}

func newAppModel(commands []Command) (*appModel, error) {
	appMenu, err := newMenu(commands)
	if err != nil {
		return nil, fmt.Errorf("create menu: %w", err)
	}

	return &appModel{
		editor: editor.New(),
		menu:   appMenu,
		boxes:  make([]box, 0, defaultInitialBoxesCapacity),
	}, nil
}

func (m *appModel) populateInitialBoxes(conversation Conversation) {
	if conversation == nil {
		return
	}
	m.usage = conversation.ContextUsage()
	m.sessionUsage = conversation.SessionUsage()
	messages := conversation.Messages()
	history := make([]session.Event, 0, len(messages))
	for _, message := range messages {
		history = append(history, session.Event{Type: session.EventMessage, Message: message})
	}
	if local, ok := conversation.(shellHistory); ok {
		history = local.DisplayHistory()
	}
	unfinished := map[string]bool{}
	for _, event := range history {
		if event.Type != session.EventMessage {
			m.appendShellHistory(event)
			if event.ShellCommand != nil {
				unfinished[event.ShellCommand.ID] = true
			}
			if event.ShellStatus != nil {
				delete(unfinished, event.ShellStatus.ID)
			}
			continue
		}
		switch msg := event.Message.(type) {
		case llm.UserMessage:
			m.boxes = append(m.boxes, userMessageBox{Text: msg.Text})
		case llm.ErrorMessage:
			m.boxes = append(m.boxes, errorMessageBox{Text: msg.Error.Error()})
		case llm.WorkflowResultMessage:
			m.boxes = append(m.boxes, workflowBox{Name: boundWorkflowText(msg.Name, maxWorkflowNameSize), Input: boundWorkflowText(msg.Input, maxWorkflowDetailSize), Status: boundWorkflowText(string(msg.Status), maxWorkflowStatusSize), Summary: boundWorkflowSummary(msg.Summary), StartedAt: msg.Timestamp, EndedAt: msg.Timestamp})
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
					title := conversation.ToolCallTitle(block.Name, block.Arguments)
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
	if len(unfinished) > 0 {
		m.boxes = append(m.boxes, systemMessageBox{Text: "Shell execution interrupted before completion was recorded"})
	}
}

func (m *appModel) handleKey(key terminal.Key) (modelUpdate, error) {
	if m.confirmRewind != nil {
		switch {
		case key.Type == terminal.KeyEscape || key.Type == terminal.KeyRune && (key.Rune == 'n' || key.Rune == 'N'):
			m.confirmRewind = nil
			return modelUpdate{Render: true}, nil
		case key.Type == terminal.KeyRune && (key.Rune == 'y' || key.Rune == 'Y'):
			confirmation := *m.confirmRewind
			m.confirmRewind = nil
			return modelUpdate{Render: true, Action: confirmation}, nil
		default:
			return modelUpdate{}, nil
		}
	}

	if key.Type == terminal.KeyEscape && m.menu.Shown() {
		m.restoreCommandMenu()
		m.editor.Clear()
		return modelUpdate{Render: true}, nil
	}

	if key.Type == terminal.KeyCtrlC {
		if m.working {
			m.boxes = append(m.boxes, errorMessageBox{Text: "Cancellation requested"})
			return modelUpdate{Render: true, Action: cancelPromptAction{}}, nil
		}
		return modelUpdate{Action: exitAction{}}, nil
	}

	if key.Type == terminal.KeyEscape {
		if m.workflowInput != nil && !m.working {
			m.workflowInput = nil
			m.editor.Clear()
			return modelUpdate{Render: true}, nil
		}
		if m.working {
			m.boxes = append(m.boxes, errorMessageBox{Text: "Cancellation requested"})
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
		if m.workflowInput != nil {
			item := *m.workflowInput
			m.workflowInput = nil
			return modelUpdate{Render: true, Action: runWorkflowAction{Workflow: item, Input: fx.Prompt}}, nil
		}
		return modelUpdate{Render: true, Action: submitPromptAction{Prompt: fx.Prompt}}, nil
	}

	return modelUpdate{}, nil
}

func (m *appModel) showRewindPoints(points []runtime.RewindPoint, fork bool) {
	if m.commandItems == nil {
		m.commandItems = append([]menuItem(nil), m.menu.items...)
	}
	m.editor.SetText("/")
	m.menu.ReplaceItems(rewindMenuItems(points, fork))
}

func (m *appModel) restoreCommandMenu() {
	if m.commandItems != nil {
		m.menu.items = m.commandItems
		m.commandItems = nil
	}
	m.menu.Hide()
}

func (m *appModel) applyHistoryChange(conversation Conversation, prompt, message string) {
	m.restoreCommandMenu()
	m.pendingTextDelta.Reset()
	m.pendingTextDeltaKind = pendingTextDeltaNone
	m.boxes = nil
	m.populateInitialBoxes(conversation)
	m.boxes = append(m.boxes, systemMessageBox{Text: message})
	m.boxLineCache.Reset()
	m.statusBarCache.Reset()
	m.editor.SetText(prompt)
}

func (m *appModel) startShell(command string) {
	m.shellRunning = true
	m.boxes = append(m.boxes, systemMessageBox{Text: "$ " + shellText(command)})
	m.shellOutputIndex = len(m.boxes)
	m.boxes = append(m.boxes, shellOutputBox{})
	m.working = true
	m.workingLabel = "Running shell command"
	m.indicatorFrame = 0
	m.saveError = ""
}

func (m *appModel) finishShell(_ string, result shell.Result, err error) modelUpdate {
	m.shellRunning = false
	m.working = false
	m.workingLabel = ""
	box := shellOutputBox{
		Stdout:          shellText(result.Stdout),
		Stderr:          shellText(result.Stderr),
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
	}
	var notices []string
	if result.TimedOut {
		notices = append(notices, "[timed out]")
	}
	if result.CleanupTimedOut {
		notices = append(notices, "[process cleanup timed out]")
	}
	box.Notice = strings.Join(notices, " ")
	if err != nil {
		box.Error = "Shell error: " + shellText(err.Error())
	}
	if box.Stdout == "" && box.Stderr == "" && !box.StdoutTruncated && !box.StderrTruncated && box.Notice == "" && box.Error == "" {
		m.boxes = m.boxes[:m.shellOutputIndex]
	} else {
		m.boxes[m.shellOutputIndex] = box
	}
	m.boxLineCache.Reset()
	return modelUpdate{Render: true}
}

func (m *appModel) startPrompt(prompt string) {
	m.boxes = append(m.boxes, userMessageBox{Text: prompt})
	m.working = true
	m.workingLabel = "Working"
	m.indicatorFrame = 0
	m.saveError = ""
}

func (m *appModel) enterWorkflowInput(item workflow.Workflow) {
	m.workflowInput = &item
	m.editor.Clear()
	m.menu.Hide()
}

func (m *appModel) startWorkflow(item workflow.Workflow, input string) {
	m.boxes = append(m.boxes, workflowBox{Name: boundWorkflowText(item.Name, maxWorkflowNameSize), Input: boundWorkflowText(input, maxWorkflowDetailSize), StartedAt: time.Now()})
	m.working = true
	m.workingLabel = "Running workflow " + item.Name
	m.indicatorFrame = 0
	m.saveError = ""
}

func (m *appModel) handleWorkflowEvent(event workflow.Event, now time.Time) modelUpdate {
	index := findWorkflowBoxIndex(m.boxes)
	if index == -1 {
		return modelUpdate{}
	}
	box := m.boxes[index].(workflowBox)
	if !box.EndedAt.IsZero() {
		return modelUpdate{}
	}
	switch event := event.(type) {
	case workflow.Log:
		box.LatestActivity = boundWorkflowText(event.Text, maxWorkflowLatestActivitySize)
	case workflow.AgentStarted:
		for _, step := range box.Active {
			if step.Invocation == event.Invocation {
				return modelUpdate{}
			}
		}
		name := boundWorkflowText(event.Request.Name, maxWorkflowNameSize)
		if strings.TrimSpace(name) == "" {
			name = fmt.Sprintf("Agent %d", event.Invocation)
		}
		box.Active = append(box.Active, workflowStep{Invocation: event.Invocation, Name: name, Status: "running", StartedAt: now})
	case workflow.AgentFinished:
		for i, step := range box.Active {
			if step.Invocation != event.Invocation {
				continue
			}
			step.Status = "completed"
			if event.Cancelled {
				step.Status = "cancelled"
			} else if event.Error != "" {
				step.Status = "failed"
			}
			step.Error = boundWorkflowText(event.Error, maxWorkflowStepErrorSize)
			step.EndedAt = now
			box.recordFinishedStep(step)
			box.Active = slices.Delete(box.Active, i, i+1)
			break
		}
	case workflow.Finished:
		box.Status = boundWorkflowText(string(event.Result.Status), maxWorkflowStatusSize)
		box.Summary = boundWorkflowSummary(event.Result.Summary)
		box.EndedAt = now
		for _, step := range box.Active {
			// A terminal workflow cannot leave an invocation running, even if
			// its individual completion event was not delivered.
			step.Status = "cancelled"
			if event.Result.Status == workflow.StatusFailed {
				step.Status = "failed"
			}
			step.EndedAt = now
			box.recordFinishedStep(step)
		}
		box.Active = nil
	default:
		// Agent transcripts belong to internal workflow handoffs, not the TUI.
		return modelUpdate{}
	}
	m.boxes[index] = box
	return modelUpdate{Render: true}
}

func (b *workflowBox) recordFinishedStep(step workflowStep) {
	b.Completed++
	if len(b.Recent) == maxWorkflowRecentSteps {
		b.Recent = slices.Delete(b.Recent, 0, 1)
	}
	b.Recent = append(b.Recent, step)
}

func (m *appModel) finishWorkflow(err error) modelUpdate {
	m.working = false
	m.statusBarCache.Reset()
	if err != nil {
		m.saveError = err.Error()
	}

	index := findWorkflowBoxIndex(m.boxes)
	if index != -1 {
		box := m.boxes[index].(workflowBox)
		if box.Status == string(workflow.StatusCancelled) {
			m.steeringPrompt = ""
			return modelUpdate{Render: true}
		}
	}

	nextPrompt := m.steeringPrompt
	m.steeringPrompt = ""
	update := modelUpdate{Render: true}
	if nextPrompt != "" {
		update.Action = submitPromptAction{Prompt: nextPrompt}
	}
	return update
}

func (m *appModel) startMCPActivation(item menuItem, name string) {
	m.boxes = append(m.boxes, systemMessageBox{Text: item.Value})
	m.working = true
	m.workingLabel = "Activating MCP " + name
	m.indicatorFrame = 0
}

func (m *appModel) finishMCPActivation(item menuItem, activated bool, err error) modelUpdate {
	m.working = false
	m.statusBarCache.Reset()

	if err != nil {
		if !errors.Is(err, context.Canceled) {
			m.boxes = append(m.boxes, errorMessageBox{Text: err.Error()})
		}
	} else if !activated {
		m.boxes = append(m.boxes, systemMessageBox{Text: item.Value + " is already active"})
	} else {
		m.boxes = append(m.boxes, systemMessageBox{Text: item.Value + " activated"})
	}

	nextPrompt := m.steeringPrompt
	m.steeringPrompt = ""
	update := modelUpdate{Render: true}
	if nextPrompt != "" {
		update.Action = submitPromptAction{Prompt: nextPrompt}
	}
	return update
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

func (m *appModel) finishPrompt(cancelled bool, now time.Time) (modelUpdate, string) {
	m.flushPendingTextDelta()
	// Cancellation and other early exits can omit individual tool-end events.
	// The prompt worker drains all events before reporting completion, so any
	// remaining open blocks are interrupted, not still running.
	for i, block := range m.boxes {
		call, ok := block.(toolCallBox)
		if !ok || !call.EndedAt.IsZero() {
			continue
		}
		call.EndedAt = now
		if call.Error == "" {
			call.Error = "Tool execution interrupted"
			if cancelled {
				call.Error = "Tool execution cancelled"
			}
		}
		m.boxes[i] = call
	}
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

func (m *appModel) handleConversationEvent(event runtime.Event, now time.Time) (modelUpdate, error) {
	switch typedEvent := event.(type) {
	case runtime.SessionSaveFailed:
		m.flushPendingTextDelta()
		m.saveError = typedEvent.Error.Error()
	case runtime.PromptProcessingStarted:
		// ignored
	case runtime.ConversationTurnStarted:
		// ignored
	case runtime.AssistantMessageStarted:
		// ignored
	case runtime.AssistantThinkingDeltaReceived:
		m.queueTextDelta(pendingTextDeltaThinking, typedEvent.Text)
	case runtime.AssistantMessageDeltaReceived:
		m.queueTextDelta(pendingTextDeltaAssistant, typedEvent.Text)
	case runtime.AssistantMessageEnded:
		cost := m.sessionUsage.Cost
		if typedEvent.Message.Usage.Cost.Available {
			cost.Total += typedEvent.Message.Usage.Cost.Total
		} else {
			cost.Available = false
		}
		m.usage = typedEvent.Message.Usage
		m.usage.Cost = cost
		m.sessionUsage.AddTokens(typedEvent.Message.Usage)
		m.sessionUsage.Cost = cost
		m.flushPendingTextDelta()
	case runtime.ToolExecutionStarted:
		m.flushPendingTextDelta()
		m.boxes = append(m.boxes, toolCallBox{
			ToolCallID: typedEvent.CallID,
			Title:      typedEvent.CallTitle,
			StartedAt:  now,
		})
	case runtime.ToolExecutionOutputDeltaReceived:
		m.flushPendingTextDelta()
		index := findToolBlockIndex(m.boxes, typedEvent.CallID)
		if index == -1 {
			return modelUpdate{}, fmt.Errorf("expected tool block to exist for call id %q (%q)", typedEvent.CallID, typedEvent.Tool.Name())
		}
		toolCallBox, ok := m.boxes[index].(toolCallBox)
		if !ok {
			return modelUpdate{}, fmt.Errorf("expected tool call box at index %d, got %T", index, m.boxes[index])
		}
		toolCallBox.addDisplayArtifact(typedEvent.Artifact)
		m.boxes[index] = toolCallBox
	case runtime.ToolExecutionResultReceived:
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
		toolCallBox.Artifacts = nil
		toolCallBox.DisplayBytes = 0
		toolCallBox.DisplayTruncated = false
		for _, artifact := range typedEvent.Artifacts {
			toolCallBox.addDisplayArtifact(artifact)
		}
		m.boxes[index] = toolCallBox
	case runtime.ToolExecutionFailed:
		m.flushPendingTextDelta()
		index := findToolBlockIndex(m.boxes, typedEvent.CallID)
		if index == -1 {
			m.boxes = append(m.boxes, toolCallBox{ToolCallID: typedEvent.CallID, Title: typedEvent.CallID, StartedAt: now})
			index = len(m.boxes) - 1
		}
		toolCallBox, ok := m.boxes[index].(toolCallBox)
		if !ok {
			return modelUpdate{}, fmt.Errorf("expected tool call box at index %d, got %T", index, m.boxes[index])
		}
		toolCallBox.Error = boundWorkflowText(toolCallBox.Error+typedEvent.Error.Error(), maxWorkflowDetailSize)
		if toolCallBox.StartedAt.IsZero() {
			toolCallBox.StartedAt = now
		}
		toolCallBox.EndedAt = now
		m.boxes[index] = toolCallBox
	case runtime.ToolExecutionEnded:
		m.flushPendingTextDelta()
		index := findToolBlockIndex(m.boxes, typedEvent.CallID)
		if index == -1 {
			m.boxes = append(m.boxes, toolCallBox{ToolCallID: typedEvent.CallID, Title: typedEvent.CallID, StartedAt: now})
			index = len(m.boxes) - 1
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
	case runtime.ConversationTurnEnded:
		// ignored
	case runtime.PromptProcessingEnded:
		// ignored
	case runtime.PromptProcessingError:
		m.flushPendingTextDelta()
	}

	return modelUpdate{Render: true}, nil
}

func (m *appModel) handleConversationError(err error) modelUpdate {
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

func (m *appModel) lines(width int, conversation Conversation, now time.Time) ([]string, error) {
	if !m.working {
		m.usage = conversation.ContextUsage()
		m.sessionUsage = conversation.SessionUsage()
	}
	m.flushPendingTextDelta()

	lines := []string{""}
	appendBlankLine := func() {
		if len(lines) == 0 || lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
	}
	lines = append(lines, m.boxLineCache.Lines(m.boxes, width, m.toolsExpanded, now)...)

	if m.steeringPrompt != "" {
		appendBlankLine()
		lines = append(lines, pendingSteeringPresenter{
			Text: m.steeringPrompt,
		}.Lines(width)...)
	}

	if m.working {
		appendBlankLine()
		lines = append(lines, workingIndicator{
			Frame: m.indicatorFrame,
			Label: m.workingLabel,
		}.Lines(width)...)
	}

	if m.confirmRewind != nil {
		appendBlankLine()
		lines = append(lines, rewindConfirmationPresenter{
			Point: m.confirmRewind.Point, RemovedTurns: m.confirmRewind.RemovedTurns,
		}.Lines(width)...)
	}

	appendBlankLine()
	lines = append(lines, editorPresenter{
		Text:   m.editor.Text(),
		Cursor: m.editor.Cursor(),
		Label:  m.editorLabel(),
	}.Lines(width)...)

	if m.menu.Shown() {
		lines = append(lines, menuPresenter{
			SelectedIndex: m.menu.SelectedIndex(),
			Items:         m.menu.Items(),
		}.Lines(width)...)
	}

	cwdStatus := m.statusBarCache.CWDStatus(conversation.CWD())
	if m.saveError != "" {
		cwdStatus = fmt.Sprintf("Save Error: %s | %s", m.saveError, cwdStatus)
	}

	lines = append(lines, statusBar{
		CWD:            conversation.CWD(),
		CWDStatus:      cwdStatus,
		UseCWDStatus:   true,
		Model:          conversation.Model(),
		ReasoningLevel: conversation.ReasoningLevel(),
		ContextUsage:   m.usage,
		SessionUsage:   m.sessionUsage,
	}.Lines(width)...)

	return lines, nil
}

func (m *appModel) setReasoningLevels(levels []llm.ReasoningLevel) {
	items := make([]menuItem, 0, len(m.menu.items)+len(levels))
	for _, item := range m.menu.items {
		if _, ok := item.Command.(SwitchReasoningLevel); !ok {
			items = append(items, item)
		}
	}
	insertAt := len(items)
	if len(items) > 0 {
		if _, ok := items[len(items)-1].Command.(Exit); ok {
			insertAt--
		}
	}
	reasoningItems := make([]menuItem, 0, len(levels))
	for _, level := range levels {
		command := SwitchReasoningLevel{Level: level}
		argument := string(level)
		reasoningItems = append(reasoningItems, menuItem{
			Name:        "/reasoning",
			Argument:    argument,
			Value:       "/reasoning " + argument,
			Description: "switch reasoning level to " + argument,
			Command:     command,
		})
	}
	updated := make([]menuItem, 0, len(items)+len(reasoningItems))
	updated = append(updated, items[:insertAt]...)
	updated = append(updated, reasoningItems...)
	updated = append(updated, items[insertAt:]...)
	for i := range updated {
		updated[i].Index = i
	}
	m.menu.items = updated
}

func (m *appModel) editorLabel() string {
	if m.workflowInput == nil {
		return ""
	}
	return "workflow " + m.workflowInput.Name + " input"
}

func findWorkflowBoxIndex(blocks []box) int {
	for i, block := range slices.Backward(blocks) {
		if _, ok := block.(workflowBox); ok {
			return i
		}
	}
	return -1
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
	for i, block := range slices.Backward(blocks) {
		switch typedBlock := block.(type) {
		case toolCallBox:
			if typedBlock.ToolCallID == toolCallID {
				return i
			}
		}
	}
	return -1
}
