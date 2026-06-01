package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/agent"
	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/tool"
)

func TestNew(t *testing.T) {
	t.Run("rejects nil tool", func(t *testing.T) {
		_, err := agent.New(agent.Config{LLM: &fakeLLM{}, Tools: []agent.Tool{nil}})
		if err == nil || !strings.Contains(err.Error(), "nil") {
			t.Fatalf("New() error = %v, want nil tool error", err)
		}
	})

	t.Run("rejects duplicate tool names", func(t *testing.T) {
		_, err := agent.New(agent.Config{LLM: &fakeLLM{}, Tools: []agent.Tool{fakeTool{name: "same"}, fakeTool{name: "same"}}})
		if err == nil || !strings.Contains(err.Error(), "duplicate tool") {
			t.Fatalf("New() error = %v, want duplicate tool error", err)
		}
	})
}

func TestPromptLifecycle(t *testing.T) {
	t.Run("llm error emits processing error and ended", func(t *testing.T) {
		wantErr := errors.New("boom")
		agt, err := agent.New(agent.Config{LLM: &fakeLLM{err: wantErr}})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		events, errs := agt.Prompt(t.Context(), "hello")
		gotEvents := collectEvents(events)
		gotErr := <-errs
		if gotErr == nil || !strings.Contains(gotErr.Error(), "boom") {
			t.Fatalf("Prompt() error = %v, want boom", gotErr)
		}
		assertEventType(t, gotEvents, agent.PromptProcessingError{})
		assertLastEventType(t, gotEvents, agent.PromptProcessingEnded{})
	})

	t.Run("tool failure emits failed and ended", func(t *testing.T) {
		toolErr := errors.New("tool failed")
		tool := fakeTool{name: "fail", err: toolErr}
		agt, err := agent.New(agent.Config{
			LLM: &fakeLLM{eventBatches: [][]llm.Event{
				{
					llm.ToolCallRequested{ToolCall: llm.ToolCallBlock{ID: "call-1", Name: "fail", Arguments: json.RawMessage(`{}`)}},
					llm.PredictionFinished{},
				},
				{
					llm.TextDelta{Text: "done"},
					llm.PredictionFinished{},
				},
			}},
			Tools:    []agent.Tool{tool},
			MaxTurns: 2,
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		events, errs := agt.Prompt(t.Context(), "hello")
		gotEvents := collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
		assertEventType(t, gotEvents, agent.ToolExecutionFailed{})
		assertEventType(t, gotEvents, agent.ToolExecutionEnded{})
	})

	t.Run("unknown tool call emits failed and ended", func(t *testing.T) {
		agt, err := agent.New(agent.Config{
			LLM: &fakeLLM{eventBatches: [][]llm.Event{
				{
					llm.ToolCallRequested{ToolCall: llm.ToolCallBlock{ID: "call-1", Name: "missing", Arguments: json.RawMessage(`{}`)}},
					llm.PredictionFinished{},
				},
				{
					llm.TextDelta{Text: "done"},
					llm.PredictionFinished{},
				},
			}},
			MaxTurns: 2,
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		events, errs := agt.Prompt(t.Context(), "hello")
		gotEvents := collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
		assertEventType(t, gotEvents, agent.ToolExecutionFailed{})
		assertEventType(t, gotEvents, agent.ToolExecutionEnded{})
	})

	t.Run("configured clock controls timestamps", func(t *testing.T) {
		want := time.Unix(1700000000, 123000000)
		agt, err := agent.New(agent.Config{
			LLM: &fakeLLM{events: []llm.Event{
				llm.TextDelta{Text: "hello"},
				llm.PredictionFinished{},
			}},
			Now: func() time.Time { return want },
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		events, errs := agt.Prompt(t.Context(), "hello")
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
		messages := agt.Messages()
		if got := messages[0].(llm.UserMessage).Timestamp; !got.Equal(want) {
			t.Fatalf("user timestamp = %v, want %v", got, want)
		}
		if got := messages[1].(llm.AssistantMessage).Timestamp; !got.Equal(want) {
			t.Fatalf("assistant timestamp = %v, want %v", got, want)
		}
	})
	t.Run("streaming tool emits chunks before final result", func(t *testing.T) {
		streamTool := fakeStreamingTool{fakeTool: fakeTool{name: "stream", result: fakeResult{artifacts: []tool.Artifact{tool.ShellStreamArtifact{Stream: tool.ShellStreamStdout, Content: "final"}}}}, artifacts: []tool.Artifact{tool.ShellStreamArtifact{Stream: tool.ShellStreamStdout, Content: "live"}}}
		agt, err := agent.New(agent.Config{
			LLM: &fakeLLM{eventBatches: [][]llm.Event{
				{
					llm.ToolCallRequested{ToolCall: llm.ToolCallBlock{ID: "call-1", Name: "stream", Arguments: json.RawMessage(`{}`)}},
					llm.PredictionFinished{},
				},
				{
					llm.TextDelta{Text: "done"},
					llm.PredictionFinished{},
				},
			}},
			Tools:    []agent.Tool{streamTool},
			MaxTurns: 2,
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		events, errs := agt.Prompt(t.Context(), "hello")
		gotEvents := collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}

		chunkIndex := eventIndex[agent.ToolExecutionOutputDeltaReceived](gotEvents)
		resultIndex := eventIndex[agent.ToolExecutionResultReceived](gotEvents)
		if chunkIndex == -1 {
			t.Fatal("streaming output event not found")
		}
		if resultIndex == -1 {
			t.Fatal("tool result event not found")
		}
		if chunkIndex > resultIndex {
			t.Fatalf("chunk event index %d after result event index %d", chunkIndex, resultIndex)
		}
		resultEvent, ok := gotEvents[resultIndex].(agent.ToolExecutionResultReceived)
		if !ok || !containsShellStreamArtifact(resultEvent.Artifacts, tool.ShellStreamStdout, "final") {
			t.Fatalf("result artifacts = %#v, want final stdout artifact", resultEvent.Artifacts)
		}

		messages := agt.Messages()
		toolOutputs := 0
		for _, message := range messages {
			if _, ok := message.(llm.ToolOutputMessage); ok {
				toolOutputs++
			}
		}
		if toolOutputs != 1 {
			t.Fatalf("tool output message count = %d, want 1", toolOutputs)
		}
	})
}

func TestCompactConversation(t *testing.T) {
	t.Run("updates_messages_on_success", func(t *testing.T) {
		compacted := []llm.Message{llm.UserMessage{Text: "compacted"}}
		compactor := &fakeCompactor{messages: compacted}
		agt, err := agent.New(agent.Config{
			LLM:       &fakeLLM{},
			Compactor: compactor,
			Messages:  []llm.Message{llm.UserMessage{Text: "old"}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if err := agt.CompactConversation(); err != nil {
			t.Fatalf("CompactConversation() error = %v", err)
		}
		got := agt.Messages()
		if len(got) != 1 || got[0].(llm.UserMessage).Text != "compacted" {
			t.Fatalf("messages = %#v, want compacted", got)
		}
		if len(compactor.input) != 1 || compactor.input[0].(llm.UserMessage).Text != "old" {
			t.Fatalf("compactor input = %#v, want old messages", compactor.input)
		}
	})

	t.Run("preserves_messages_on_failure", func(t *testing.T) {
		wantErr := errors.New("compact failed")
		agt, err := agent.New(agent.Config{
			LLM:       &fakeLLM{},
			Compactor: &fakeCompactor{err: wantErr},
			Messages:  []llm.Message{llm.UserMessage{Text: "old"}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		err = agt.CompactConversation()
		if !errors.Is(err, wantErr) {
			t.Fatalf("CompactConversation() error = %v, want compact failed", err)
		}
		got := agt.Messages()
		if len(got) != 1 || got[0].(llm.UserMessage).Text != "old" {
			t.Fatalf("messages = %#v, want original", got)
		}
	})

	t.Run("requires_configured_compactor", func(t *testing.T) {
		agt, err := agent.New(agent.Config{LLM: &fakeLLM{}})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		err = agt.CompactConversation()
		if err == nil || !strings.Contains(err.Error(), "compactor") {
			t.Fatalf("CompactConversation() error = %v, want compactor error", err)
		}
	})
}

func collectEvents(events <-chan agent.Event) []agent.Event {
	var collected []agent.Event
	for event := range events {
		collected = append(collected, event)
	}
	return collected
}

func containsShellStreamArtifact(artifacts []tool.Artifact, stream tool.ShellStream, content string) bool {
	for _, artifact := range artifacts {
		streamArtifact, ok := artifact.(tool.ShellStreamArtifact)
		if ok && streamArtifact.Stream == stream && streamArtifact.Content == content {
			return true
		}
	}
	return false
}

func eventIndex[T agent.Event](events []agent.Event) int {
	for i, event := range events {
		if _, ok := event.(T); ok {
			return i
		}
	}
	return -1
}

func assertEventType[T agent.Event](t *testing.T, events []agent.Event, _ T) {
	t.Helper()
	for _, event := range events {
		if _, ok := event.(T); ok {
			return
		}
	}
	var zero T
	if len(events) == 0 {
		t.Fatalf("event %T not found in empty event list", zero)
	}
	t.Fatalf("event %T not found in %d events", zero, len(events))
}

func assertLastEventType[T agent.Event](t *testing.T, events []agent.Event, _ T) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("events empty")
	}
	if _, ok := events[len(events)-1].(T); !ok {
		var zero T
		t.Fatalf("last event = %T, want %T", events[len(events)-1], zero)
	}
}

func writeSkill(t *testing.T, dir string, name string, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	writeFile(t, filepath.Join(dir, "SKILL.md"), fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n", name, description))
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
}

type fakeLLM struct {
	events         []llm.Event
	eventBatches   [][]llm.Event
	model          llm.Model
	reasoningLevel llm.ReasoningLevel
	err            error
	predictCalls   int
}

func (f *fakeLLM) Model() llm.Model {
	return f.model
}

func (f *fakeLLM) ReasoningLevel() llm.ReasoningLevel {
	return f.reasoningLevel
}

func (f *fakeLLM) SetReasoningLevel(level llm.ReasoningLevel) error {
	f.reasoningLevel = level
	return nil
}

func (f *fakeLLM) PredictNext(_ context.Context, _ llm.PredictNextRequest) (<-chan llm.Event, <-chan error) {
	events := f.events
	if f.eventBatches != nil {
		if f.predictCalls < len(f.eventBatches) {
			events = f.eventBatches[f.predictCalls]
		} else {
			events = nil
		}
	}
	eventsCh := make(chan llm.Event, len(events))
	for _, event := range events {
		eventsCh <- event
	}
	close(eventsCh)

	errsCh := make(chan error, 1)
	if f.err != nil {
		errsCh <- f.err
	}
	close(errsCh)

	f.predictCalls++
	return eventsCh, errsCh
}

func (f *fakeLLM) PredictNextStructured(context.Context, llm.PredictNextStructuredRequest) (json.RawMessage, error) {
	panic("not implemented")
}

type fakeCompactor struct {
	messages []llm.Message
	input    []llm.Message
	err      error
}

func (f *fakeCompactor) Compact(_ context.Context, messages []llm.Message) ([]llm.Message, error) {
	f.input = append([]llm.Message(nil), messages...)
	if f.err != nil {
		return nil, f.err
	}
	return append([]llm.Message(nil), f.messages...), nil
}

type fakeStreamingTool struct {
	fakeTool
	artifacts []tool.Artifact
}

func (f fakeStreamingTool) CallWithOutput(_ context.Context, _ json.RawMessage, emit func(tool.Artifact) error) (any, error) {
	for _, artifact := range f.artifacts {
		if err := emit(artifact); err != nil {
			return nil, err
		}
	}
	return f.Call(context.Background(), nil)
}

type fakeResult struct {
	artifacts []tool.Artifact
}

func (r fakeResult) Artifacts() []tool.Artifact {
	return r.artifacts
}

type fakeTool struct {
	name   string
	result any
	err    error
}

func (f fakeTool) Name() string {
	return f.name
}

func (f fakeTool) Description() string {
	return "fake tool"
}

func (f fakeTool) Parameters() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object"}
}

func (f fakeTool) Call(context.Context, json.RawMessage) (any, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return map[string]string{"ok": "true"}, nil
}
