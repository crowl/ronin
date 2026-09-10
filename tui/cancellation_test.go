package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tui/internal/terminal"
)

func TestPromptCompletionFinalizesOpenTools(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "interrupted"
		if cancelled {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			model := newTestModel(t)
			model.working = true
			start := time.Unix(100, 0)
			end := start.Add(2 * time.Second)
			model.boxes = []box{
				toolCallBox{ToolCallID: "open", Title: "$ sleep 30", StartedAt: start, Artifacts: []tool.Artifact{tool.ShellStreamArtifact{Stream: tool.ShellStreamStdout, Content: "partial output"}}},
				toolCallBox{ToolCallID: "completed", StartedAt: start, EndedAt: start.Add(time.Second)},
				toolCallBox{ToolCallID: "failed", StartedAt: start, EndedAt: start.Add(time.Second), Error: "existing failure"},
			}
			update, _ := model.finishPrompt(cancelled, end)
			if model.working || !update.Render {
				t.Fatal("completion did not clear working state and request rendering")
			}
			call := model.boxes[0].(toolCallBox)
			if !call.EndedAt.Equal(end) || call.Error != "Tool execution "+name {
				t.Fatalf("unfinished call: %+v", call)
			}
			before := strings.Join(plainLines(renderBoxLinesAt(call, 80, true, end)), "\n")
			after := strings.Join(plainLines(renderBoxLinesAt(call, 80, true, end.Add(time.Hour))), "\n")
			if before != after || strings.Contains(after, "Elapsed") || !strings.Contains(after, "partial output") || !strings.Contains(after, name) {
				t.Fatalf("unexpected finished rendering: %s", after)
			}
			completed := model.boxes[1].(toolCallBox)
			failed := model.boxes[2].(toolCallBox)
			if !completed.EndedAt.Equal(start.Add(time.Second)) || completed.Error != "" || failed.Error != "existing failure" {
				t.Fatal("completion changed terminal tool outcomes")
			}
			model.finishPrompt(cancelled, end.Add(time.Hour))
			if !model.boxes[0].(toolCallBox).EndedAt.Equal(end) {
				t.Fatal("repeated completion changed duration")
			}
		})
	}
}

func TestEscapeCancellationWithoutToolEndEvent(t *testing.T) {
	app := newTestApp(t, testAppConfig{})
	conversation := &cancellationConversation{}
	app.conversation = conversation
	app.submitPrompt(t.Context(), "run shell")
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-app.events:
			if err := app.handleAppEvent(t.Context(), event); err != nil {
				t.Fatal(err)
			}
			if received, ok := event.(conversationEventReceived); ok {
				if _, ok := received.Event.(runtime.ToolExecutionStarted); ok {
					if err := app.handleKey(t.Context(), terminal.Key{Type: terminal.KeyEscape}); err != nil {
						t.Fatal(err)
					}
					if !app.model.working {
						t.Fatal("Escape reported completion before worker finished")
					}
				}
			}
			if done, ok := event.(conversationPromptDone); ok {
				if !done.Cancelled {
					t.Fatal("completion lost cancellation state")
				}
				index := findToolBlockIndex(app.model.boxes, "shell")
				if index < 0 {
					t.Fatal("missing tool block")
				}
				call := app.model.boxes[index].(toolCallBox)
				if call.EndedAt.IsZero() || call.Error != "Tool execution cancelled" {
					t.Fatalf("tool still open: %+v", call)
				}
				return
			}
		case <-deadline.C:
			t.Fatal("cancelled prompt did not finish")
		}
	}
}

type cancellationConversation struct{ fakeConversation }

func (*cancellationConversation) Prompt(ctx context.Context, _ string) (<-chan runtime.Event, <-chan error) {
	events := make(chan runtime.Event)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		select {
		case events <- runtime.ToolExecutionStarted{CallID: "shell", CallTitle: "$ sleep 30"}:
		case <-ctx.Done():
			return
		}
		<-ctx.Done()
		// Model a runtime cancellation that omits ToolExecutionFailed/Ended.
		errs <- ctx.Err()
	}()
	return events, errs
}
