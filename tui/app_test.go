package tui

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/agent"
	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/tool"

	"github.com/crowl/ronin/tui/internal/render"
	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/tui/internal/text"
)

func TestTUIRendering(t *testing.T) {
	t.Run("shift enter renders blank editor line", func(t *testing.T) {
		renderer := &fakeRenderer{}
		app := newTestApp(t, testAppConfig{Renderer: renderer})

		if err := app.render(); err != nil {
			t.Fatalf("render initial app: %v", err)
		}
		initialLines := renderer.lastLines()

		for _, key := range []terminal.Key{{Type: terminal.KeyRune, Rune: 'a'}, {Type: terminal.KeyShiftEnter}} {
			if err := app.handleKey(context.Background(), key); err != nil {
				t.Fatalf("handle key %#v: %v", key, err)
			}
		}

		if err := app.render(); err != nil {
			t.Fatalf("render multiline editor: %v", err)
		}
		multilineLines := renderer.lastLines()

		if len(multilineLines) != len(initialLines)+1 {
			t.Fatalf("editor did not grow by one rendered line\ninitial lines:   %#v\nmultiline lines: %#v", initialLines, multilineLines)
		}
		if !renderedLinesContain(multilineLines, " "+"a") {
			t.Fatalf("first editor line missing from render: %#v", multilineLines)
		}
		if !renderedLinesContain(multilineLines, " "+terminal.SGRReverse+" "+terminal.SGRReset) {
			t.Fatalf("blank second editor line with cursor missing from render: %#v", multilineLines)
		}
	})

	t.Run("shift enter grows rendered editor", func(t *testing.T) {
		renderer := &fakeRenderer{}
		app := newTestApp(t, testAppConfig{Renderer: renderer})

		if err := app.render(); err != nil {
			t.Fatalf("render initial app: %v", err)
		}
		initialLines := renderer.lastLines()

		for _, key := range []terminal.Key{{Type: terminal.KeyRune, Rune: 'a'}, {Type: terminal.KeyShiftEnter}, {Type: terminal.KeyRune, Rune: 'b'}} {
			if err := app.handleKey(context.Background(), key); err != nil {
				t.Fatalf("handle key %#v: %v", key, err)
			}
		}

		if err := app.render(); err != nil {
			t.Fatalf("render multiline editor: %v", err)
		}
		multilineLines := renderer.lastLines()

		if len(multilineLines) != len(initialLines)+1 {
			t.Fatalf("editor did not grow by one rendered line\ninitial lines:   %#v\nmultiline lines: %#v", initialLines, multilineLines)
		}
		if !renderedLinesContain(multilineLines, " "+"a") {
			t.Fatalf("first editor line missing from render: %#v", multilineLines)
		}
		if !renderedLinesContain(multilineLines, " b") {
			t.Fatalf("second editor line missing from render: %#v", multilineLines)
		}
	})

	t.Run("closing menu restores initial rendered lines", func(t *testing.T) {
		renderer := &fakeRenderer{}
		app := newTestApp(t, testAppConfig{Renderer: renderer})

		if err := app.render(); err != nil {
			t.Fatalf("render initial app: %v", err)
		}
		initialLines := renderer.lastLines()

		if err := app.handleKey(t.Context(), terminal.Key{Type: terminal.KeyRune, Rune: '/'}); err != nil {
			t.Fatalf("open menu: %v", err)
		}
		if err := app.render(); err != nil {
			t.Fatalf("render open menu: %v", err)
		}
		if reflect.DeepEqual(renderer.lastLines(), initialLines) {
			t.Fatalf("menu render matched initial render; menu did not open")
		}

		if err := app.handleKey(t.Context(), terminal.Key{Type: terminal.KeyEscape}); err != nil {
			t.Fatalf("close menu: %v", err)
		}
		if err := app.render(); err != nil {
			t.Fatalf("render closed menu: %v", err)
		}
		closedLines := renderer.lastLines()
		if !reflect.DeepEqual(closedLines, initialLines) {
			t.Fatalf("closing menu did not restore initial rendered lines\ninitial: %#v\nclosed:  %#v", initialLines, closedLines)
		}
	})

	t.Run("caches terminal size", func(t *testing.T) {
		term := &fakeTerminal{size: terminal.Size{Width: 80, Height: 24}}
		app := newTestApp(t, testAppConfig{Terminal: term})

		for range 3 {
			if err := app.render(); err != nil {
				t.Fatalf("render: %v", err)
			}
		}

		if term.sizeCalls != 1 {
			t.Fatalf("terminal size calls\ngot:  %d\nwant: 1", term.sizeCalls)
		}
	})

	t.Run("terminal resize refreshes cached size", func(t *testing.T) {
		term := &fakeTerminal{size: terminal.Size{Width: 80, Height: 24}}
		renderer := &fakeRenderer{}
		app := newTestApp(t, testAppConfig{Terminal: term, Renderer: renderer})

		if err := app.render(); err != nil {
			t.Fatalf("render initial size: %v", err)
		}

		term.size = terminal.Size{Width: 100, Height: 40}
		if err := app.handleAppEvent(t.Context(), terminalResized{}); err != nil {
			t.Fatalf("handle resize: %v", err)
		}

		if term.sizeCalls != 2 {
			t.Fatalf("terminal size calls\ngot:  %d\nwant: 2", term.sizeCalls)
		}
		if app.size != (terminal.Size{Width: 100, Height: 40}) {
			t.Fatalf("cached size\ngot:  %#v\nwant: %#v", app.size, terminal.Size{Width: 100, Height: 40})
		}
		if !app.renderRequested {
			t.Fatalf("resize did not request render")
		}

		if err := app.render(); err != nil {
			t.Fatalf("render resized frame: %v", err)
		}
		request := renderer.lastRequest()
		if request.Width != 100 || request.Height != 40 {
			t.Fatalf("render request size\ngot:  %dx%d\nwant: 100x40", request.Width, request.Height)
		}
		if term.sizeCalls != 2 {
			t.Fatalf("resized render should use cached size, got %d size calls", term.sizeCalls)
		}
	})

	t.Run("working indicator advances on tick not render", func(t *testing.T) {
		renderer := &fakeRenderer{}
		app := newTestApp(t, testAppConfig{Renderer: renderer})
		app.model.working = true

		if err := app.render(); err != nil {
			t.Fatalf("render initial working frame: %v", err)
		}
		initialIndicator := renderedWorkingIndicatorLine(t, renderer.lastLines())

		for range 5 {
			if err := app.render(); err != nil {
				t.Fatalf("render repeated working frame: %v", err)
			}
		}
		repeatedIndicator := renderedWorkingIndicatorLine(t, renderer.lastLines())
		if repeatedIndicator != initialIndicator {
			t.Fatalf("indicator advanced on render\ninitial:  %q\nrepeated: %q", initialIndicator, repeatedIndicator)
		}

		if err := app.handleAppEvent(t.Context(), workingTick{}); err != nil {
			t.Fatalf("handle working tick: %v", err)
		}
		if err := app.render(); err != nil {
			t.Fatalf("render after working tick: %v", err)
		}

		tickedIndicator := renderedWorkingIndicatorLine(t, renderer.lastLines())
		if tickedIndicator == initialIndicator {
			t.Fatalf("indicator did not advance on working tick: %q", tickedIndicator)
		}
	})

	t.Run("status bar context usage uses latest context usage", func(t *testing.T) {
		lines := statusBar{
			CWD:            ".",
			Model:          llm.Model{Provider: "test", Name: "model", ContextLimit: 1000},
			ReasoningLevel: llm.ReasoningLevelOff,
			ContextUsage:   llm.Usage{InputTokens: 100, OutputTokens: 50},
		}.Lines(120, DefaultTheme())

		if len(lines) != 2 {
			t.Fatalf("line count\ngot:  %d\nwant: 2", len(lines))
		}
		usageLine := lines[1]
		if strings.Contains(usageLine, "↑") || strings.Contains(usageLine, "↓") || strings.Contains(usageLine, "R") {
			t.Fatalf("status usage line contains cumulative token counts: %q", usageLine)
		}
		if !strings.Contains(usageLine, "15.0%/1000") {
			t.Fatalf("status usage line used wrong context usage: %q", usageLine)
		}
		if !strings.Contains(usageLine, "test:model off") {
			t.Fatalf("status usage line missing model details: %q", usageLine)
		}
	})
}

func TestTUIRenderScheduling(t *testing.T) {
	t.Run("coalesces rapid requests", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})

		app.requestRender()
		app.requestRender()
		app.requestRender()

		if !receiveRenderRequest(app.events, 50*time.Millisecond) {
			t.Fatalf("expected one render request")
		}
		if receiveRenderRequest(app.events, 20*time.Millisecond) {
			t.Fatalf("expected rapid render requests to coalesce")
		}
	})

	t.Run("schedules delayed frame", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})
		app.lastRenderStartedAt = time.Now()

		app.requestRender()
		app.requestRender()
		app.requestRender()

		if receiveRenderRequest(app.events, renderInterval/2) {
			t.Fatalf("render request was queued before the render interval elapsed")
		}
		if !receiveRenderTimer(activeRenderTimerC(app), 3*renderInterval) {
			t.Fatalf("expected delayed render timer")
		}
		if receiveRenderRequest(app.events, 20*time.Millisecond) {
			t.Fatalf("expected delayed render requests to coalesce")
		}
	})

	t.Run("falls back to timer when event queue is full", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})
		for len(app.events) < cap(app.events) {
			app.events <- workingTick{}
		}

		app.requestRender()

		if app.renderTimer == nil || !app.renderTimerActive {
			t.Fatalf("expected fallback render timer when event queue is full")
		}
		if !receiveRenderTimer(activeRenderTimerC(app), 3*renderInterval) {
			t.Fatalf("expected fallback render timer to fire")
		}
	})

	t.Run("does not stack delayed timers", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})
		app.lastRenderStartedAt = time.Now()

		app.requestRender()
		firstTimer := app.renderTimer
		app.requestRender()
		app.requestRender()

		if firstTimer == nil {
			t.Fatalf("expected delayed render timer")
		}
		if app.renderTimer != firstTimer {
			t.Fatalf("expected delayed render requests to reuse the active timer")
		}
		if receiveRenderRequest(app.events, renderInterval/2) {
			t.Fatalf("render request was queued before the render interval elapsed")
		}
		if !receiveRenderTimer(activeRenderTimerC(app), 3*renderInterval) {
			t.Fatalf("expected delayed render timer")
		}
	})

	t.Run("after render waits for next frame interval", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})

		if err := app.render(); err != nil {
			t.Fatalf("render: %v", err)
		}

		app.requestRender()

		if receiveRenderRequest(app.events, renderInterval/2) {
			t.Fatalf("render request was queued before the next frame interval")
		}
		if !receiveRenderTimer(activeRenderTimerC(app), 3*renderInterval) {
			t.Fatalf("expected delayed render timer")
		}
	})

	t.Run("coalesces streaming agent events", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})
		app.lastRenderStartedAt = time.Now()

		for range 100 {
			if err := app.handleAppEvent(t.Context(), agentEventReceived{Event: agent.AssistantMessageDeltaReceived{Text: "x"}}); err != nil {
				t.Fatalf("handle agent event: %v", err)
			}
		}

		if app.renderTimer == nil || !app.renderTimerActive {
			t.Fatalf("expected one active delayed render timer")
		}
		if receiveRenderRequest(app.events, renderInterval/2) {
			t.Fatalf("render request was queued before the render interval elapsed")
		}
		if !receiveRenderTimer(activeRenderTimerC(app), 3*renderInterval) {
			t.Fatalf("expected delayed render timer")
		}
	})
}

func TestTUIAgentEvents(t *testing.T) {
	t.Run("assistant message deltas coalesce until render", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})

		for _, text := range []string{"hel", "lo", "!"} {
			if _, err := app.model.handleAgentEvent(agent.AssistantMessageDeltaReceived{Text: text}, time.Now()); err != nil {
				t.Fatalf("handle delta: %v", err)
			}
		}

		if len(app.model.boxes) != 0 {
			t.Fatalf("deltas flushed before render: %#v", app.model.boxes)
		}
		if err := app.render(); err != nil {
			t.Fatalf("render: %v", err)
		}

		if len(app.model.boxes) != 1 {
			t.Fatalf("box count\ngot:  %d\nwant: 1", len(app.model.boxes))
		}
		message, ok := app.model.boxes[0].(assistantMessageBox)
		if !ok || message.Text != "hello!" {
			t.Fatalf("assistant box\ngot:  %#v\nwant: assistantMessageBox hello!", app.model.boxes[0])
		}
	})

	t.Run("pending thinking delta flushes before assistant delta", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})

		if _, err := app.model.handleAgentEvent(agent.AssistantThinkingDeltaReceived{Text: "think"}, time.Now()); err != nil {
			t.Fatalf("handle thinking delta: %v", err)
		}
		if _, err := app.model.handleAgentEvent(agent.AssistantMessageDeltaReceived{Text: "answer"}, time.Now()); err != nil {
			t.Fatalf("handle assistant delta: %v", err)
		}
		app.model.flushPendingTextDelta()

		if len(app.model.boxes) != 2 {
			t.Fatalf("box count\ngot:  %d\nwant: 2", len(app.model.boxes))
		}
		thinking, ok := app.model.boxes[0].(assistantThinkingBox)
		if !ok || thinking.Text != "think" {
			t.Fatalf("thinking box\ngot:  %#v\nwant: assistantThinkingBox think", app.model.boxes[0])
		}
		message, ok := app.model.boxes[1].(assistantMessageBox)
		if !ok || message.Text != "answer" {
			t.Fatalf("assistant box\ngot:  %#v\nwant: assistantMessageBox answer", app.model.boxes[1])
		}
	})

	t.Run("pending assistant delta flushes before tool start", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})

		if _, err := app.model.handleAgentEvent(agent.AssistantMessageDeltaReceived{Text: "before tool"}, time.Now()); err != nil {
			t.Fatalf("handle assistant delta: %v", err)
		}
		if _, err := app.model.handleAgentEvent(agent.ToolExecutionStarted{CallID: "call_1", Tool: fakeStartedTool{name: "read_file"}, CallArguments: []byte(`{"path":"a.go"}`), CallTitle: "read_file a.go"}, time.Now()); err != nil {
			t.Fatalf("handle tool start: %v", err)
		}

		if len(app.model.boxes) != 2 {
			t.Fatalf("box count\ngot:  %d\nwant: 2", len(app.model.boxes))
		}
		message, ok := app.model.boxes[0].(assistantMessageBox)
		if !ok || message.Text != "before tool" {
			t.Fatalf("assistant box\ngot:  %#v\nwant: assistantMessageBox before tool", app.model.boxes[0])
		}
		if _, ok := app.model.boxes[1].(toolCallBox); !ok {
			t.Fatalf("tool box\ngot:  %#v\nwant: toolCallBox", app.model.boxes[1])
		}
	})

	t.Run("thinking delta after tool creates new thinking box", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})

		for _, event := range []agent.Event{
			agent.AssistantThinkingDeltaReceived{Text: "first"},
			agent.AssistantMessageDeltaReceived{Text: "text"},
			agent.ToolExecutionStarted{CallID: "call_1", Tool: fakeStartedTool{name: "read_file"}, CallArguments: []byte(`{"path":"a.go"}`), CallTitle: "read_file a.go"},
			agent.AssistantThinkingDeltaReceived{Text: "second"},
		} {
			if _, err := app.model.handleAgentEvent(event, time.Now()); err != nil {
				t.Fatalf("handle event %T: %v", event, err)
			}
		}
		app.model.flushPendingTextDelta()

		if len(app.model.boxes) != 4 {
			t.Fatalf("box count\ngot:  %d\nwant: 4\nboxes: %#v", len(app.model.boxes), app.model.boxes)
		}
		firstThinking, ok := app.model.boxes[0].(assistantThinkingBox)
		if !ok || firstThinking.Text != "first" {
			t.Fatalf("first box\ngot:  %#v\nwant: assistantThinkingBox first", app.model.boxes[0])
		}
		assistantMessage, ok := app.model.boxes[1].(assistantMessageBox)
		if !ok || assistantMessage.Text != "text" {
			t.Fatalf("second box\ngot:  %#v\nwant: assistantMessageBox text", app.model.boxes[1])
		}
		if _, ok := app.model.boxes[2].(toolCallBox); !ok {
			t.Fatalf("third box\ngot:  %#v\nwant: toolCallBox", app.model.boxes[2])
		}
		secondThinking, ok := app.model.boxes[3].(assistantThinkingBox)
		if !ok || secondThinking.Text != "second" {
			t.Fatalf("fourth box\ngot:  %#v\nwant: assistantThinkingBox second", app.model.boxes[3])
		}
	})

	t.Run("tool output streaming and final result", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})

		if _, err := app.model.handleAgentEvent(agent.ToolExecutionStarted{CallID: "call_1", Tool: fakeStartedTool{name: "shell"}, CallArguments: []byte(`{"command":"printf final"}`), CallTitle: "$ printf final"}, time.Now()); err != nil {
			t.Fatalf("handle tool start: %v", err)
		}
		if _, err := app.model.handleAgentEvent(agent.ToolExecutionOutputDeltaReceived{CallID: "call_1", Tool: fakeStartedTool{name: "shell"}, Artifact: tool.ShellStreamArtifact{Stream: tool.ShellStreamStdout, Content: "live"}}, time.Now()); err != nil {
			t.Fatalf("handle output chunk: %v", err)
		}

		box, ok := app.model.boxes[0].(toolCallBox)
		if !ok || !hasShellStreamArtifact(box.Artifacts, tool.ShellStreamStdout, "live") {
			t.Fatalf("streaming box\ngot:  %#v\nwant: toolCallBox live artifact", app.model.boxes[0])
		}

		if _, err := app.model.handleAgentEvent(agent.ToolExecutionResultReceived{CallID: "call_1", Tool: fakeStartedTool{name: "shell"}, Artifacts: []tool.Artifact{tool.ShellStreamArtifact{Stream: tool.ShellStreamStdout, Content: "final"}}}, time.Now()); err != nil {
			t.Fatalf("handle final result: %v", err)
		}

		box, ok = app.model.boxes[0].(toolCallBox)
		if !ok || !hasShellStreamArtifact(box.Artifacts, tool.ShellStreamStdout, "final") {
			t.Fatalf("final box\ngot:  %#v\nwant: toolCallBox final artifact", app.model.boxes[0])
		}
	})

	t.Run("context canceled agent error is ignored", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})

		app.applyUpdate(t.Context(), app.model.handleAgentError(context.Canceled))

		if len(app.model.boxes) != 0 {
			t.Fatalf("context cancellation should not append error box: %#v", app.model.boxes)
		}
	})
}

func TestTUIKeyHandling(t *testing.T) {
	t.Run("ctrl c exits when idle", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})

		err := app.handleKey(t.Context(), terminal.Key{Type: terminal.KeyCtrlC})
		if !errors.Is(err, errExitRequested) {
			t.Fatalf("error\ngot:  %v\nwant: %v", err, errExitRequested)
		}
	})

	t.Run("ctrl c and escape cancel working prompt", func(t *testing.T) {
		tests := []struct {
			name    string
			keyType terminal.KeyType
			want    string
		}{
			{name: "ctrl c", keyType: terminal.KeyCtrlC, want: "Operation cancelled"},
			{name: "escape", keyType: terminal.KeyEscape, want: "Operation canceled"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cancelled := false
				app := newTestApp(t, testAppConfig{})
				app.model.working = true
				app.cancelFunc = func() { cancelled = true }

				if err := app.handleKey(t.Context(), terminal.Key{Type: tt.keyType}); err != nil {
					t.Fatalf("handle cancel key: %v", err)
				}
				if !cancelled {
					t.Fatalf("cancel function was not called")
				}
				if len(app.model.boxes) != 1 {
					t.Fatalf("box count\ngot:  %d\nwant: 1", len(app.model.boxes))
				}
				errorBox, ok := app.model.boxes[0].(errorMessageBox)
				if !ok || errorBox.Text != tt.want {
					t.Fatalf("cancel box\ngot:  %#v\nwant: errorMessageBox %q", app.model.boxes[0], tt.want)
				}
			})
		}
	})

	t.Run("submit prompt while working queues steering prompt", func(t *testing.T) {
		app := newTestApp(t, testAppConfig{})
		app.model.working = true

		app.submitPrompt(t.Context(), "first")
		app.submitPrompt(t.Context(), "second")

		if app.model.steeringPrompt != "first\n\nsecond" {
			t.Fatalf("steering prompt\ngot:  %q\nwant: %q", app.model.steeringPrompt, "first\n\nsecond")
		}
		if len(app.model.boxes) != 0 {
			t.Fatalf("working steering prompt should not append boxes: %#v", app.model.boxes)
		}
	})

	t.Run("menu item error renders error box", func(t *testing.T) {
		agent := &fakeAgent{newConversationErr: errors.New("cannot reset")}
		app := newTestApp(t, testAppConfig{Agent: agent})

		err := app.runCommand(menuItem{Value: "/new", Command: StartNewConversation{}}, StartNewConversation{})
		if err != nil {
			t.Fatalf("handle menu item: %v", err)
		}
		if len(app.model.boxes) != 2 {
			t.Fatalf("box count\ngot:  %d\nwant: 2", len(app.model.boxes))
		}
		errorBox, ok := app.model.boxes[1].(errorMessageBox)
		if !ok || !strings.Contains(errorBox.Text, "failed to start new conversation") || !strings.Contains(errorBox.Text, "cannot reset") {
			t.Fatalf("error box\ngot:  %#v\nwant: failed new conversation error", app.model.boxes[1])
		}
	})
}

func hasShellStreamArtifact(artifacts []tool.Artifact, stream tool.ShellStream, content string) bool {
	for _, artifact := range artifacts {
		streamArtifact, ok := artifact.(tool.ShellStreamArtifact)
		if ok && streamArtifact.Stream == stream && streamArtifact.Content == content {
			return true
		}
	}
	return false
}

func renderedLinesContain(lines []string, value string) bool {
	for _, line := range lines {
		if strings.Contains(line, value) {
			return true
		}
	}
	return false
}

func renderedWorkingIndicatorLine(t *testing.T, lines []string) string {
	t.Helper()

	for _, line := range lines {
		stripped := text.StripANSI(line)
		if strings.Contains(stripped, "Working") {
			return stripped
		}
	}

	t.Fatalf("working indicator line not found in %#v", lines)
	return ""
}

func receiveRenderRequest(events <-chan event, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case event := <-events:
		_, ok := event.(renderRequested)
		return ok
	case <-timer.C:
		return false
	}
}

func activeRenderTimerC(app *app) <-chan time.Time {
	if app.renderTimer == nil || !app.renderTimerActive {
		return nil
	}
	return app.renderTimer.C
}

func receiveRenderTimer(renderTimer <-chan time.Time, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-renderTimer:
		return true
	case <-timer.C:
		return false
	}
}

type testAppConfig struct {
	Terminal *fakeTerminal
	Agent    *fakeAgent
	Renderer *fakeRenderer
}

func newTestApp(t *testing.T, cfg testAppConfig) *app {
	t.Helper()

	term := cfg.Terminal
	if term == nil {
		term = &fakeTerminal{size: terminal.Size{Width: 80, Height: 24}}
	}
	agt := cfg.Agent
	if agt == nil {
		agt = &fakeAgent{}
	}
	renderer := cfg.Renderer
	if renderer == nil {
		renderer = &fakeRenderer{}
	}

	app, err := newApp(appConfig{
		Terminal: term,
		Agent:    agt,
		Renderer: renderer,
		Commands: []Command{StartNewConversation{}, CompactConversation{}, Exit{}},
		Theme:    DefaultTheme(),
	})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	return app
}

type fakeRenderer struct {
	requests []render.Request
}

func (r *fakeRenderer) Render(req render.Request) error {
	lines := append([]string(nil), req.Lines...)
	r.requests = append(r.requests, render.Request{
		Lines:  lines,
		Width:  req.Width,
		Height: req.Height,
		Force:  req.Force,
	})
	return nil
}

func (r *fakeRenderer) lastLines() []string {
	last := r.lastRequest()
	return append([]string(nil), last.Lines...)
}

func (r *fakeRenderer) lastRequest() render.Request {
	if len(r.requests) == 0 {
		return render.Request{}
	}
	return r.requests[len(r.requests)-1]
}

type fakeTerminal struct {
	size      terminal.Size
	sizeCalls int
}

func (t *fakeTerminal) ReadKey(context.Context) (terminal.Key, error) {
	return terminal.Key{}, nil
}

func (t *fakeTerminal) Write(string) error {
	return nil
}

func (t *fakeTerminal) Size() (terminal.Size, error) {
	t.sizeCalls++
	return t.size, nil
}

type fakeAgent struct {
	newConversationErr      error
	compactConversationErr  error
	switchModelErr          error
	switchReasoningLevelErr error
}

func (a *fakeAgent) State() agent.State {
	return agent.State{
		CWD: ".",
		Model: llm.Model{
			Provider:     "test",
			Name:         "model",
			ContextLimit: 100,
		},
		ReasoningLevel: llm.ReasoningLevelOff,
	}
}

func (a *fakeAgent) NewConversation() error {
	return a.newConversationErr
}

func (a *fakeAgent) CompactConversation() error {
	return a.compactConversationErr
}

func (a *fakeAgent) SwitchModel(llm.Model) error {
	return a.switchModelErr
}

func (a *fakeAgent) SwitchReasoningLevel(llm.ReasoningLevel) error {
	return a.switchReasoningLevelErr
}

func (a *fakeAgent) Prompt(context.Context, string) (<-chan agent.Event, <-chan error) {
	events := make(chan agent.Event)
	errs := make(chan error)
	close(events)
	close(errs)
	return events, errs
}

type fakeStartedTool struct {
	name string
}

func (t fakeStartedTool) Name() string {
	return t.name
}

func (t fakeStartedTool) Description() string {
	return "fake tool"
}

func (t fakeStartedTool) Parameters() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object"}
}

func (t fakeStartedTool) Call(context.Context, json.RawMessage) (any, error) {
	return nil, nil
}
