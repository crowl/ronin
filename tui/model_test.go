package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/tui/internal/text"
)

func TestAppModel(t *testing.T) {
	t.Run("ctrl c exits when idle", func(t *testing.T) {
		model := newTestModel(t)

		update, err := model.handleKey(terminal.Key{Type: terminal.KeyCtrlC})
		if err != nil {
			t.Fatalf("handle key: %v", err)
		}
		if _, ok := update.Action.(exitAction); !ok {
			t.Fatalf("action\ngot:  %T\nwant: %T", update.Action, exitAction{})
		}
	})

	t.Run("ctrl c cancels when working", func(t *testing.T) {
		model := newTestModel(t)
		model.working = true

		update, err := model.handleKey(terminal.Key{Type: terminal.KeyCtrlC})
		if err != nil {
			t.Fatalf("handle key: %v", err)
		}
		if !update.Render {
			t.Fatalf("cancel did not request render")
		}
		if _, ok := update.Action.(cancelPromptAction); !ok {
			t.Fatalf("action\ngot:  %T\nwant: %T", update.Action, cancelPromptAction{})
		}
		if len(model.boxes) != 1 {
			t.Fatalf("box count\ngot:  %d\nwant: 1", len(model.boxes))
		}
		box, ok := model.boxes[0].(errorMessageBox)
		if !ok || box.Text != "Operation cancelled" {
			t.Fatalf("cancel box\ngot:  %#v\nwant: Operation cancelled", model.boxes[0])
		}
	})

	t.Run("submit prompt returns submit action", func(t *testing.T) {
		model := newTestModel(t)

		if _, err := model.handleKey(terminal.Key{Type: terminal.KeyRune, Rune: 'h'}); err != nil {
			t.Fatalf("type h: %v", err)
		}
		if _, err := model.handleKey(terminal.Key{Type: terminal.KeyRune, Rune: 'i'}); err != nil {
			t.Fatalf("type i: %v", err)
		}
		update, err := model.handleKey(terminal.Key{Type: terminal.KeyEnter})
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		action, ok := update.Action.(submitPromptAction)
		if !ok {
			t.Fatalf("action\ngot:  %T\nwant: %T", update.Action, submitPromptAction{})
		}
		if action.Prompt != "hi" {
			t.Fatalf("prompt\ngot:  %q\nwant: hi", action.Prompt)
		}
	})

	t.Run("menu selection returns command action", func(t *testing.T) {
		model := newTestModel(t)

		if _, err := model.handleKey(terminal.Key{Type: terminal.KeyRune, Rune: '/'}); err != nil {
			t.Fatalf("open menu: %v", err)
		}
		update, err := model.handleKey(terminal.Key{Type: terminal.KeyEnter})
		if err != nil {
			t.Fatalf("select menu: %v", err)
		}
		action, ok := update.Action.(runCommandAction)
		if !ok {
			t.Fatalf("action\ngot:  %T\nwant: %T", update.Action, runCommandAction{})
		}
		if _, ok := action.Command.(StartNewConversation); !ok {
			t.Fatalf("command\ngot:  %T\nwant: %T", action.Command, StartNewConversation{})
		}
	})

	t.Run("compaction shows Compacting indicator", func(t *testing.T) {
		model := newTestModel(t)
		model.startCompaction(menuItem{Value: "/compact"})

		if !model.working || model.workingLabel != "Compacting" {
			t.Fatalf("working=%v label=%q, want Compacting", model.working, model.workingLabel)
		}

		lines, err := model.lines(80, &fakeConversation{}, time.Now())
		if err != nil {
			t.Fatalf("lines: %v", err)
		}
		if !renderedLinesContain(stripLines(lines), "Compacting") {
			t.Fatalf("Compacting indicator not rendered: %#v", lines)
		}
		if renderedLinesContain(stripLines(lines), "Working") {
			t.Fatalf("unexpected Working indicator: %#v", lines)
		}
	})

	t.Run("pending steering prompt renders above indicator", func(t *testing.T) {
		model := newTestModel(t)
		model.startPrompt("do the thing")
		model.steeringPrompt = "then this"

		lines, err := model.lines(80, &fakeConversation{}, time.Now())
		if err != nil {
			t.Fatalf("lines: %v", err)
		}
		stripped := stripLines(lines)
		steeringIdx := lineIndexContaining(stripped, "then this")
		indicatorIdx := lineIndexContaining(stripped, "Working")
		if steeringIdx < 0 {
			t.Fatalf("steering prompt not rendered: %#v", lines)
		}
		if indicatorIdx < 0 {
			t.Fatalf("working indicator not rendered: %#v", lines)
		}
		if steeringIdx >= indicatorIdx {
			t.Fatalf("steering prompt (%d) should render above indicator (%d)", steeringIdx, indicatorIdx)
		}
	})

	t.Run("working tick increments indicator only while working", func(t *testing.T) {
		model := newTestModel(t)

		update := model.tickWorking()
		if update.Render || model.indicatorFrame != 0 {
			t.Fatalf("idle tick changed model: update=%#v frame=%d", update, model.indicatorFrame)
		}

		model.working = true
		update = model.tickWorking()
		if !update.Render || model.indicatorFrame != 1 {
			t.Fatalf("working tick\nupdate: %#v\nframe: %d", update, model.indicatorFrame)
		}
	})

	t.Run("renders padding between editor and status bar", func(t *testing.T) {
		model := newTestModel(t)

		lines, err := model.lines(80, &fakeConversation{}, time.Now())
		if err != nil {
			t.Fatalf("lines: %v", err)
		}
		if len(lines) < 3 {
			t.Fatalf("rendered lines = %#v, want editor, padding, and status bar", lines)
		}
		if lines[len(lines)-2] != "" {
			t.Fatalf("line before status bar = %q, want blank padding", lines[len(lines)-2])
		}
		if !strings.Contains(lines[len(lines)-1], mutedStyle.start()) {
			t.Fatalf("last line is not the status bar: %q", lines[len(lines)-1])
		}
	})

	t.Run("menu remains attached to editor above status padding", func(t *testing.T) {
		model := newTestModel(t)
		if _, err := model.handleKey(terminal.Key{Type: terminal.KeyRune, Rune: '/'}); err != nil {
			t.Fatalf("open menu: %v", err)
		}

		lines, err := model.lines(80, &fakeConversation{}, time.Now())
		if err != nil {
			t.Fatalf("lines: %v", err)
		}
		if len(lines) < 4 || lines[len(lines)-2] != "" {
			t.Fatalf("rendered lines = %#v, want menu followed by padding and status bar", lines)
		}
		if lines[len(lines)-3] == "" {
			t.Fatalf("menu is separated from editor area: %#v", lines)
		}
	})

	t.Run("session save failed event displays error in status bar", func(t *testing.T) {
		model := newTestModel(t)

		update, err := model.handleConversationEvent(runtime.SessionSaveFailed{Error: errors.New("out of space")}, time.Now())
		if err != nil {
			t.Fatalf("handleConversationEvent: %v", err)
		}
		if !update.Render {
			t.Fatal("expected update.Render to be true")
		}
		if model.saveError != "out of space" {
			t.Fatalf("saveError = %q, want 'out of space'", model.saveError)
		}

		lines, err := model.lines(80, &fakeConversation{}, time.Now())
		if err != nil {
			t.Fatalf("lines: %v", err)
		}

		found := false
		for _, line := range lines {
			if strings.Contains(line, "Save Error: out of space") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected 'Save Error: out of space' in rendered lines, got: %#v", lines)
		}

		// starting a new prompt should clear the error
		model.startPrompt("next prompt")
		if model.saveError != "" {
			t.Fatalf("expected saveError to be cleared, got %q", model.saveError)
		}
	})

	t.Run("populate initial boxes from conversation messages", func(t *testing.T) {
		model := newTestModel(t)
		now := time.Now()

		messages := []llm.Message{
			llm.UserMessage{
				Timestamp: now,
				Text:      "user prompt",
			},
			llm.AssistantMessage{
				Timestamp: now.Add(time.Second),
				Blocks: []llm.AssistantBlock{
					llm.ThinkingBlock{Text: "think 1"},
					llm.ThinkingBlock{Text: " think 2"},
					llm.TextBlock{Text: "assistant reply"},
					llm.TextBlock{Text: " continued"},
					llm.ToolCallBlock{ID: "call_1", Name: "shell", Arguments: []byte(`{"command":"ls"}`)},
				},
			},
			llm.ToolOutputMessage{
				Timestamp:  now.Add(2 * time.Second),
				ToolName:   "shell",
				ToolCallID: "call_1",
				ToolOutput: "file.txt",
			},
			llm.ErrorMessage{
				Timestamp: now.Add(3 * time.Second),
				Error:     errors.New("something went wrong"),
			},
		}

		conv := &fakeConversation{messages: messages}
		model.populateInitialBoxes(conv)

		if len(model.boxes) != 5 {
			t.Fatalf("expected 5 boxes, got %d", len(model.boxes))
		}

		userBox, ok := model.boxes[0].(userMessageBox)
		if !ok || userBox.Text != "user prompt" {
			t.Fatalf("box 0 incorrect: %#v", model.boxes[0])
		}

		thinkingBox, ok := model.boxes[1].(assistantThinkingBox)
		if !ok || thinkingBox.Text != "think 1 think 2" {
			t.Fatalf("box 1 incorrect: %#v", model.boxes[1])
		}

		assistantBox, ok := model.boxes[2].(assistantMessageBox)
		if !ok || assistantBox.Text != "assistant reply continued" {
			t.Fatalf("box 2 incorrect: %#v", model.boxes[2])
		}

		toolBox, ok := model.boxes[3].(toolCallBox)
		if !ok || toolBox.ToolCallID != "call_1" || toolBox.Title != "shell" {
			t.Fatalf("box 3 incorrect: %#v", model.boxes[3])
		}
		if toolBox.EndedAt != now.Add(2*time.Second) {
			t.Fatalf("toolBox.EndedAt incorrect: %v", toolBox.EndedAt)
		}

		errBox, ok := model.boxes[4].(errorMessageBox)
		if !ok || errBox.Text != "something went wrong" {
			t.Fatalf("box 4 incorrect: %#v", model.boxes[4])
		}
	})
}

func stripLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = text.StripANSI(line)
	}
	return out
}

func lineIndexContaining(lines []string, value string) int {
	for i, line := range lines {
		if strings.Contains(line, value) {
			return i
		}
	}
	return -1
}

func newTestModel(t *testing.T) *appModel {
	t.Helper()

	model, err := newAppModel([]Command{
		StartNewConversation{},
		CompactConversation{},
		Exit{},
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	return model
}
