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
	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/tool"
)

func TestNew(t *testing.T) {
	t.Run("rejects nil tool", func(t *testing.T) {
		_, err := agent.New(agent.Config{Assistant: &fakeLLM{}, Tools: []agent.Tool{nil}})
		if err == nil || !strings.Contains(err.Error(), "nil") {
			t.Fatalf("New() error = %v, want nil tool error", err)
		}
	})

	t.Run("initializes context usage from latest restored assistant message", func(t *testing.T) {
		wantUsage := llm.Usage{InputTokens: 30, OutputTokens: 12, CachedTokens: 5, TotalTokens: 42}
		agt, err := agent.New(agent.Config{
			Assistant: &fakeLLM{},
			Session: session.Session{Messages: []llm.Message{
				llm.AssistantMessage{Usage: llm.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}},
				llm.UserMessage{Text: "after first assistant"},
				llm.AssistantMessage{Usage: wantUsage},
				llm.ToolOutputMessage{ToolCallID: "call-1", ToolName: "shell"},
			}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if got := agt.ContextUsage(); got != wantUsage {
			t.Fatalf("ContextUsage() = %#v, want %#v", got, wantUsage)
		}
	})

	t.Run("copies initial session messages", func(t *testing.T) {
		messages := make([]llm.Message, 1, 4)
		messages[0] = llm.UserMessage{Text: "original"}

		agt, err := agent.New(agent.Config{
			Assistant: &fakeLLM{},
			Session:   session.Session{Messages: messages},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		messages[0] = llm.UserMessage{Text: "mutated"}
		if got := agt.Messages(); len(got) != 1 || got[0].(llm.UserMessage).Text != "original" {
			t.Fatalf("Messages() = %#v, want original copy", got)
		}
	})
}

func TestPromptLifecycle(t *testing.T) {
	t.Run("llm error emits processing error and ended", func(t *testing.T) {
		wantErr := errors.New("boom")
		agt, err := agent.New(agent.Config{Assistant: &fakeLLM{err: wantErr}})
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
			Assistant: &fakeLLM{eventBatches: [][]llm.PredictionEvent{
				{
					llm.BlockEnded{Block: llm.ToolCallBlock{ID: "call-1", Name: "fail", Arguments: json.RawMessage(`{}`)}},
					llm.PredictionFinished{},
				},
				{
					llm.TextDelta{Text: "done"},
					llm.BlockEnded{Block: llm.TextBlock{Text: "done"}},
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
			Assistant: &fakeLLM{eventBatches: [][]llm.PredictionEvent{
				{
					llm.BlockEnded{Block: llm.ToolCallBlock{ID: "call-1", Name: "missing", Arguments: json.RawMessage(`{}`)}},
					llm.PredictionFinished{},
				},
				{
					llm.TextDelta{Text: "done"},
					llm.BlockEnded{Block: llm.TextBlock{Text: "done"}},
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
			Assistant: &fakeLLM{events: []llm.PredictionEvent{
				llm.TextDelta{Text: "hello"},
				llm.BlockEnded{Block: llm.TextBlock{Text: "hello"}},
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
	t.Run("incremental tool emits chunks before final result", func(t *testing.T) {
		incrementalTool := fakeIncrementalTool{fakeTool: fakeTool{name: "stream", result: fakeResult{artifacts: []tool.Artifact{tool.ShellStreamArtifact{Stream: tool.ShellStreamStdout, Content: "final"}}}}, artifacts: []tool.Artifact{tool.ShellStreamArtifact{Stream: tool.ShellStreamStdout, Content: "live"}}}
		agt, err := agent.New(agent.Config{
			Assistant: &fakeLLM{eventBatches: [][]llm.PredictionEvent{
				{
					llm.BlockEnded{Block: llm.ToolCallBlock{ID: "call-1", Name: "stream", Arguments: json.RawMessage(`{}`)}},
					llm.PredictionFinished{},
				},
				{
					llm.TextDelta{Text: "done"},
					llm.BlockEnded{Block: llm.TextBlock{Text: "done"}},
					llm.PredictionFinished{},
				},
			}},
			Tools:    []agent.Tool{incrementalTool},
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

func TestNewConversation(t *testing.T) {
	t.Run("without persistence clears messages in memory", func(t *testing.T) {
		agt, err := agent.New(agent.Config{
			Assistant: &fakeLLM{},
			Session:   session.Session{Messages: []llm.Message{llm.UserMessage{Text: "old"}}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if err := agt.NewConversation(); err != nil {
			t.Fatalf("NewConversation() error = %v", err)
		}
		if got := agt.Messages(); len(got) != 0 {
			t.Fatalf("messages = %#v, want empty", got)
		}
	})

	t.Run("uses assistant metadata when creating a new session", func(t *testing.T) {
		store := &fakeSessionStore{sessions: map[string]session.Session{"sess-1": {ID: "sess-1"}}, activeID: "sess-1"}
		agt, err := agent.New(agent.Config{
			Assistant:    &fakeLLM{model: llm.Model{Provider: "provider-a", Name: "model-a"}, reasoningLevel: llm.ReasoningLevelHigh},
			SessionStore: store,
			Session: session.Session{
				ID:             "sess-1",
				Model:          config.Model{Provider: "stale", Name: "old"},
				ReasoningLevel: "off",
				Messages:       []llm.Message{llm.UserMessage{Text: "old"}},
			},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if err := agt.NewConversation(); err != nil {
			t.Fatalf("NewConversation() error = %v", err)
		}
		if got := store.sessions["new-sess-123"]; got.Model != (config.Model{Provider: "provider-a", Name: "model-a"}) || got.ReasoningLevel != string(llm.ReasoningLevelHigh) {
			t.Fatalf("created session metadata = %#v, want assistant metadata", got)
		}
	})

	t.Run("preserves existing messages when clear fails", func(t *testing.T) {
		store := &fakeSessionStore{
			sessions: map[string]session.Session{"sess-1": {ID: "sess-1"}},
			activeID: "sess-1",
			clearErr: errors.New("clear failed"),
		}
		agt, err := agent.New(agent.Config{
			Assistant:    &fakeLLM{},
			SessionStore: store,
			Session:      session.Session{ID: "sess-1", Messages: []llm.Message{llm.UserMessage{Text: "old"}}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if err := agt.NewConversation(); err == nil || !strings.Contains(err.Error(), "clear session") {
			t.Fatalf("NewConversation() error = %v, want clear session error", err)
		}
		if got := agt.Messages(); len(got) != 1 || got[0].(llm.UserMessage).Text != "old" {
			t.Fatalf("messages = %#v, want original", got)
		}
		if got := agt.ContextUsage(); got != (llm.Usage{}) {
			t.Fatalf("ContextUsage() = %#v, want zero", got)
		}
	})

	t.Run("preserves existing messages when create fails", func(t *testing.T) {
		store := &fakeSessionStore{
			sessions:  map[string]session.Session{"sess-1": {ID: "sess-1"}},
			activeID:  "sess-1",
			createErr: errors.New("create failed"),
		}
		agt, err := agent.New(agent.Config{
			Assistant:    &fakeLLM{},
			SessionStore: store,
			Session:      session.Session{ID: "sess-1", Messages: []llm.Message{llm.UserMessage{Text: "old"}}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if err := agt.NewConversation(); err == nil || !strings.Contains(err.Error(), "create session") {
			t.Fatalf("NewConversation() error = %v, want create session error", err)
		}
		if got := agt.Messages(); len(got) != 1 || got[0].(llm.UserMessage).Text != "old" {
			t.Fatalf("messages = %#v, want original", got)
		}
		if got := agt.ContextUsage(); got != (llm.Usage{}) {
			t.Fatalf("ContextUsage() = %#v, want zero", got)
		}
	})
}

func TestSessionMutationTransactions(t *testing.T) {
	t.Run("SwitchModel save failure leaves active model unchanged", func(t *testing.T) {
		model := llm.Model{Provider: "test", Name: "switch-model-failure"}
		if err := llm.RegisterModel(model, func(level llm.ReasoningLevel) (llm.Assistant, error) {
			return &fakeLLM{model: model, reasoningLevel: level}, nil
		}); err != nil {
			t.Fatalf("RegisterModel() error = %v", err)
		}

		store := &fakeSessionStore{
			sessions: map[string]session.Session{"sess-1": {ID: "sess-1", Model: config.Model{Provider: "test", Name: "original"}}},
			activeID: "sess-1",
			saveErr:  errors.New("save failed"),
		}
		originalModel := llm.Model{Provider: "test", Name: "original"}
		agt, err := agent.New(agent.Config{
			Assistant:    &fakeLLM{model: originalModel},
			SessionStore: store,
			Session:      store.sessions[store.activeID],
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		err = agt.SwitchModel(model)
		if err == nil || !strings.Contains(err.Error(), "save session model") {
			t.Fatalf("SwitchModel() error = %v, want save session model error", err)
		}
		if got := agt.Model(); got != originalModel {
			t.Fatalf("Model() = %#v, want %#v", got, originalModel)
		}
		if got := agt.Messages(); len(got) != 0 {
			t.Fatalf("Messages() = %#v, want unchanged", got)
		}
		if got := store.sessions["sess-1"].Model; got != (config.Model{Provider: "test", Name: "original"}) {
			t.Fatalf("stored model = %#v, want original", got)
		}
	})

	t.Run("SwitchReasoningLevel save failure rolls back runtime level", func(t *testing.T) {
		store := &fakeSessionStore{sessions: map[string]session.Session{"sess-1": {ID: "sess-1", ReasoningLevel: string(llm.ReasoningLevelOff)}}, activeID: "sess-1", saveErr: errors.New("save failed")}
		original := &fakeLLM{model: llm.Model{Provider: "test", Name: "reasoning"}, reasoningLevel: llm.ReasoningLevelOff}
		agt, err := agent.New(agent.Config{Assistant: original, SessionStore: store, Session: store.sessions[store.activeID]})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		err = agt.SwitchReasoningLevel(llm.ReasoningLevelHigh)
		if err == nil || !strings.Contains(err.Error(), "save session reasoning level") {
			t.Fatalf("SwitchReasoningLevel() error = %v, want save session reasoning level error", err)
		}
		if got := agt.ReasoningLevel(); got != llm.ReasoningLevelOff {
			t.Fatalf("ReasoningLevel() = %q, want %q", got, llm.ReasoningLevelOff)
		}
		if got := store.sessions["sess-1"].ReasoningLevel; got != string(llm.ReasoningLevelOff) {
			t.Fatalf("stored reasoning level = %q, want %q", got, llm.ReasoningLevelOff)
		}
	})
}

func TestCompactConversation(t *testing.T) {
	t.Run("updates_messages_on_success", func(t *testing.T) {
		compacted := []llm.Message{llm.UserMessage{Text: "compacted"}}
		compactor := &fakeCompactor{messages: compacted}
		agt, err := agent.New(agent.Config{
			Assistant: &fakeLLM{},
			Compactor: compactor,
			Session:   session.Session{Messages: []llm.Message{llm.UserMessage{Text: "old"}}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if err := agt.CompactConversation(context.Background()); err != nil {
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
			Assistant: &fakeLLM{},
			Compactor: &fakeCompactor{err: wantErr},
			Session:   session.Session{Messages: []llm.Message{llm.UserMessage{Text: "old"}}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		err = agt.CompactConversation(context.Background())
		if !errors.Is(err, wantErr) {
			t.Fatalf("CompactConversation() error = %v, want compact failed", err)
		}
		got := agt.Messages()
		if len(got) != 1 || got[0].(llm.UserMessage).Text != "old" {
			t.Fatalf("messages = %#v, want original", got)
		}
	})

	t.Run("propagates_caller_context", func(t *testing.T) {
		compactor := &fakeCompactor{messages: []llm.Message{llm.UserMessage{Text: "compacted"}}}
		agt, err := agent.New(agent.Config{
			Assistant: &fakeLLM{},
			Compactor: compactor,
			Session:   session.Session{Messages: []llm.Message{llm.UserMessage{Text: "old"}}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		type ctxKey struct{}
		ctx := context.WithValue(context.Background(), ctxKey{}, "v")
		if err := agt.CompactConversation(ctx); err != nil {
			t.Fatalf("CompactConversation() error = %v", err)
		}
		if compactor.gotCtx == nil || compactor.gotCtx.Value(ctxKey{}) != "v" {
			t.Fatalf("compactor did not receive caller context")
		}
	})

	t.Run("requires_configured_compactor", func(t *testing.T) {
		agt, err := agent.New(agent.Config{Assistant: &fakeLLM{}})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		err = agt.CompactConversation(context.Background())
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
	events         []llm.PredictionEvent
	eventBatches   [][]llm.PredictionEvent
	model          llm.Model
	reasoningLevel llm.ReasoningLevel
	err            error
	predictCalls   int
}

func (f *fakeLLM) Model() llm.Model                   { return f.model }
func (f *fakeLLM) ReasoningLevel() llm.ReasoningLevel { return f.reasoningLevel }
func (f *fakeLLM) SetReasoningLevel(level llm.ReasoningLevel) error {
	f.reasoningLevel = level
	return nil
}
func (f *fakeLLM) PredictNext(_ context.Context, _ llm.PredictNextRequest) (<-chan llm.PredictionEvent, <-chan error) {
	events := f.events
	if f.eventBatches != nil {
		if f.predictCalls < len(f.eventBatches) {
			events = f.eventBatches[f.predictCalls]
		} else {
			events = nil
		}
	}
	eventsCh := make(chan llm.PredictionEvent, len(events))
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
	gotCtx   context.Context
	err      error
}

func (f *fakeCompactor) Compact(ctx context.Context, messages []llm.Message) ([]llm.Message, error) {
	f.gotCtx = ctx
	f.input = append([]llm.Message(nil), messages...)
	if f.err != nil {
		return nil, f.err
	}
	return append([]llm.Message(nil), f.messages...), nil
}

type fakeIncrementalTool struct {
	fakeTool
	artifacts []tool.Artifact
}

func (f fakeIncrementalTool) CallIncremental(_ context.Context, _ json.RawMessage, emit func(tool.Artifact) error) (any, error) {
	for _, artifact := range f.artifacts {
		if err := emit(artifact); err != nil {
			return nil, err
		}
	}
	return f.Call(context.Background(), nil)
}

type fakeResult struct{ artifacts []tool.Artifact }

func (r fakeResult) Artifacts() []tool.Artifact { return r.artifacts }

type fakeTool struct {
	name   string
	result any
	err    error
}

func (f fakeTool) Name() string                   { return f.name }
func (f fakeTool) Description() string            { return "fake tool" }
func (f fakeTool) Parameters() *jsonschema.Schema { return &jsonschema.Schema{Type: "object"} }
func (f fakeTool) Call(context.Context, json.RawMessage) (any, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return map[string]string{"ok": "true"}, nil
}

type fakeSessionStore struct {
	sessions  map[string]session.Session
	activeID  string
	saveErr   error
	clearErr  error
	createErr error
}

func (f *fakeSessionStore) LoadActive(workingDir string) (session.Session, bool, error) {
	if f.sessions == nil {
		return session.Session{}, false, nil
	}
	sess, ok := f.sessions[f.activeID]
	return sess, ok, nil
}
func (f *fakeSessionStore) Create(workingDir string, metadata session.Metadata) (session.Session, error) {
	if f.createErr != nil {
		return session.Session{}, f.createErr
	}
	sess := session.Session{ID: "new-sess-123", WorkingDir: workingDir, Model: metadata.Model, ReasoningLevel: metadata.ReasoningLevel}
	if f.sessions == nil {
		f.sessions = make(map[string]session.Session)
	}
	f.sessions[sess.ID] = sess
	f.activeID = sess.ID
	return sess, nil
}
func (f *fakeSessionStore) Save(record session.Session) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	if f.sessions == nil {
		f.sessions = make(map[string]session.Session)
	}
	f.sessions[record.ID] = record
	return nil
}
func (f *fakeSessionStore) Clear(workingDir string) error {
	if f.clearErr != nil {
		return f.clearErr
	}
	f.activeID = ""
	f.sessions = nil
	return nil
}

func TestSessionPersistence(t *testing.T) {
	t.Run("loads messages from ActiveSession when Messages is empty", func(t *testing.T) {
		activeSess := session.Session{ID: "sess-1", Messages: []llm.Message{llm.UserMessage{Text: "restored text"}}}
		agt, err := agent.New(agent.Config{Assistant: &fakeLLM{}, Session: activeSess})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		msgs := agt.Messages()
		if len(msgs) != 1 || msgs[0].(llm.UserMessage).Text != "restored text" {
			t.Fatalf("unexpected loaded messages: %#v", msgs)
		}
	})

	t.Run("saves session after successful Prompt", func(t *testing.T) {
		store := &fakeSessionStore{sessions: map[string]session.Session{"sess-1": {ID: "sess-1"}}, activeID: "sess-1"}
		agt, err := agent.New(agent.Config{Assistant: &fakeLLM{events: []llm.PredictionEvent{llm.TextDelta{Text: "reply"}, llm.BlockEnded{Block: llm.TextBlock{Text: "reply"}}, llm.PredictionFinished{}}}, SessionStore: store, Session: store.sessions[store.activeID]})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		events, errs := agt.Prompt(t.Context(), "hello")
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
		saved := store.sessions["sess-1"]
		if len(saved.Messages) != 2 {
			t.Fatalf("expected 2 messages in saved session, got: %d", len(saved.Messages))
		}
		if saved.Messages[0].(llm.UserMessage).Text != "hello" {
			t.Fatalf("expected first message to be 'hello', got: %q", saved.Messages[0].(llm.UserMessage).Text)
		}
	})

	t.Run("emits SessionSaveFailed event and returns error on save failure", func(t *testing.T) {
		store := &fakeSessionStore{sessions: map[string]session.Session{"sess-1": {ID: "sess-1"}}, activeID: "sess-1", saveErr: errors.New("disk full")}
		agt, err := agent.New(agent.Config{Assistant: &fakeLLM{events: []llm.PredictionEvent{llm.TextDelta{Text: "reply"}, llm.BlockEnded{Block: llm.TextBlock{Text: "reply"}}, llm.PredictionFinished{}}}, SessionStore: store, Session: store.sessions[store.activeID]})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		events, errs := agt.Prompt(t.Context(), "hello")
		gotEvents := collectEvents(events)
		gotErr := <-errs
		if gotErr == nil || !strings.Contains(gotErr.Error(), "disk full") {
			t.Fatalf("Prompt() error = %v, want disk full error", gotErr)
		}
		assertEventType(t, gotEvents, agent.SessionSaveFailed{})
	})

	t.Run("saves session after SwitchModel and SwitchReasoningLevel", func(t *testing.T) {
		store := &fakeSessionStore{sessions: map[string]session.Session{"sess-1": {ID: "sess-1"}}, activeID: "sess-1"}
		_ = llm.RegisterModel(llm.Model{Provider: "openai", Name: "gpt-4"}, func(lvl llm.ReasoningLevel) (llm.Assistant, error) {
			return &fakeLLM{model: llm.Model{Provider: "openai", Name: "gpt-4"}, reasoningLevel: lvl}, nil
		})
		agt, err := agent.New(agent.Config{Assistant: &fakeLLM{model: llm.Model{Provider: "openai", Name: "gpt-3.5"}}, SessionStore: store, Session: store.sessions[store.activeID]})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if err := agt.SwitchModel(llm.Model{Provider: "openai", Name: "gpt-4"}); err != nil {
			t.Fatalf("SwitchModel() error = %v", err)
		}
		saved := store.sessions["sess-1"]
		if saved.Model.Name != "gpt-4" {
			t.Fatalf("saved session model = %q, want gpt-4", saved.Model.Name)
		}
		if err := agt.SwitchReasoningLevel(llm.ReasoningLevelHigh); err != nil {
			t.Fatalf("SwitchReasoningLevel() error = %v", err)
		}
		saved = store.sessions["sess-1"]
		if saved.ReasoningLevel != "high" {
			t.Fatalf("saved session reasoning level = %q, want high", saved.ReasoningLevel)
		}
	})

	t.Run("clears session and creates a new one on NewConversation", func(t *testing.T) {
		store := &fakeSessionStore{sessions: map[string]session.Session{"sess-1": {ID: "sess-1"}}, activeID: "sess-1"}
		agt, err := agent.New(agent.Config{Assistant: &fakeLLM{}, SessionStore: store, Session: store.sessions[store.activeID]})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if err := agt.NewConversation(); err != nil {
			t.Fatalf("NewConversation() error = %v", err)
		}
		if store.activeID != "new-sess-123" {
			t.Fatalf("active session ID = %q, want new-sess-123", store.activeID)
		}
	})
}
