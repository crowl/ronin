package runtime_test

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

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/tool"
)

func TestNew(t *testing.T) {
	t.Run("rejects nil tool", func(t *testing.T) {
		_, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: &fakeModelClient{}, Tools: []runtime.Tool{nil}})
		if err == nil || !strings.Contains(err.Error(), "nil") {
			t.Fatalf("New() error = %v, want nil tool error", err)
		}
	})

	t.Run("initializes context usage from latest restored assistant message", func(t *testing.T) {
		wantUsage := llm.Usage{InputTokens: 30, OutputTokens: 12, CachedTokens: 5, TotalTokens: 42}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{},
			Messages: []llm.Message{
				llm.AssistantMessage{Usage: llm.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}},
				llm.UserMessage{Text: "after first assistant"},
				llm.AssistantMessage{Usage: wantUsage},
				llm.ToolOutputMessage{ToolCallID: "call-1", ToolName: "shell"},
			},
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

		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{},
			Messages:    messages,
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

func TestSetToolsAndSystemPrompt(t *testing.T) {
	t.Run("updates the next request", func(t *testing.T) {
		modelClient := &fakeModelClient{events: []llm.PredictionEvent{
			llm.BlockEnded{Block: llm.TextBlock{Text: "done"}},
			llm.PredictionFinished{},
		}}
		conversation, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient:  modelClient,
			SystemPrompt: "before",
		})
		if err != nil {
			t.Fatalf("NewConversation() error = %v", err)
		}
		tool := fakeTool{name: "dynamic"}
		if err := conversation.SetToolsAndSystemPrompt([]runtime.Tool{tool}, "after"); err != nil {
			t.Fatalf("SetToolsAndSystemPrompt() error = %v", err)
		}

		events, errs := conversation.Prompt(t.Context(), "use tools")
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
		if len(modelClient.requests) != 1 {
			t.Fatalf("requests = %d, want 1", len(modelClient.requests))
		}
		request := modelClient.requests[0]
		if request.SystemPrompt != "after" {
			t.Fatalf("system prompt = %q, want after", request.SystemPrompt)
		}
		if len(request.Tools) != 1 || request.Tools[0].Name() != "dynamic" {
			t.Fatalf("tools = %#v", request.Tools)
		}
	})

	t.Run("invalid tools leave the conversation unchanged", func(t *testing.T) {
		modelClient := &fakeModelClient{events: []llm.PredictionEvent{
			llm.BlockEnded{Block: llm.TextBlock{Text: "done"}},
			llm.PredictionFinished{},
		}}
		original := fakeTool{name: "original"}
		conversation, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient:  modelClient,
			Tools:        []runtime.Tool{original},
			SystemPrompt: "before",
		})
		if err != nil {
			t.Fatalf("NewConversation() error = %v", err)
		}
		duplicate := fakeTool{name: "duplicate"}
		if err := conversation.SetToolsAndSystemPrompt([]runtime.Tool{duplicate, duplicate}, "after"); err == nil {
			t.Fatal("SetToolsAndSystemPrompt() error = nil, want duplicate tool error")
		}

		events, errs := conversation.Prompt(t.Context(), "use tools")
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
		request := modelClient.requests[0]
		if request.SystemPrompt != "before" {
			t.Fatalf("system prompt = %q, want before", request.SystemPrompt)
		}
		if len(request.Tools) != 1 || request.Tools[0].Name() != "original" {
			t.Fatalf("tools = %#v", request.Tools)
		}
	})
}

func TestPromptLifecycle(t *testing.T) {
	t.Run("llm error emits processing error and ended", func(t *testing.T) {
		wantErr := errors.New("boom")
		agt, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: &fakeModelClient{err: wantErr}})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		events, errs := agt.Prompt(t.Context(), "hello")
		gotEvents := collectEvents(events)
		gotErr := <-errs
		if gotErr == nil || !strings.Contains(gotErr.Error(), "boom") {
			t.Fatalf("Prompt() error = %v, want boom", gotErr)
		}
		assertEventType(t, gotEvents, runtime.PromptProcessingError{})
		assertLastEventType(t, gotEvents, runtime.PromptProcessingEnded{})
	})

	t.Run("tool failure emits failed and ended", func(t *testing.T) {
		toolErr := errors.New("tool failed")
		tool := fakeTool{name: "fail", err: toolErr}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{eventBatches: [][]llm.PredictionEvent{
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
			Tools:    []runtime.Tool{tool},
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
		assertEventType(t, gotEvents, runtime.ToolExecutionFailed{})
		assertEventType(t, gotEvents, runtime.ToolExecutionEnded{})
	})

	t.Run("unknown tool call emits failed and ended", func(t *testing.T) {
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{eventBatches: [][]llm.PredictionEvent{
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
		assertEventType(t, gotEvents, runtime.ToolExecutionFailed{})
		assertEventType(t, gotEvents, runtime.ToolExecutionEnded{})
	})

	t.Run("repairs interrupted tool calls before sending a new prompt", func(t *testing.T) {
		modelClient := &fakeModelClient{events: []llm.PredictionEvent{
			llm.BlockEnded{Block: llm.TextBlock{Text: "done"}},
			llm.PredictionFinished{},
		}}
		messages := []llm.Message{
			llm.AssistantMessage{Blocks: []llm.AssistantBlock{
				llm.ToolCallBlock{ID: "completed", Name: "read_file", Arguments: json.RawMessage(`{}`)},
				llm.ToolCallBlock{ID: "interrupted", Name: "shell", Arguments: json.RawMessage(`{}`)},
			}},
			llm.ToolOutputMessage{ToolCallID: "completed", ToolName: "read_file", ToolOutput: "ok"},
		}
		store := &fakeSessionStore{
			sessions: map[string]session.Session{"sess-1": {ID: "sess-1", Title: "Existing"}},
			activeID: "sess-1",
		}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient:  modelClient,
			SessionStore: store,
			Session:      store.sessions[store.activeID],
			Messages:     messages,
		})
		if err != nil {
			t.Fatalf("NewConversation() error = %v", err)
		}

		events, errs := agt.Prompt(t.Context(), "continue")
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}

		gotMessages := agt.Messages()
		if len(gotMessages) != 5 {
			t.Fatalf("Messages() length = %d, want 5", len(gotMessages))
		}
		repair, ok := gotMessages[2].(llm.ToolErrorMessage)
		if !ok {
			t.Fatalf("message 2 = %T, want llm.ToolErrorMessage", gotMessages[2])
		}
		if repair.ToolCallID != "interrupted" || repair.ToolName != "shell" || !strings.Contains(repair.Error.Error(), "interrupted") {
			t.Fatalf("repair message = %#v, want interrupted shell call", repair)
		}
		if user, ok := gotMessages[3].(llm.UserMessage); !ok || user.Text != "continue" {
			t.Fatalf("message 3 = %#v, want new user message", gotMessages[3])
		}
		if len(store.messages["sess-1"]) != 5 {
			t.Fatalf("persisted messages = %#v, want repaired history, user, and assistant", store.messages["sess-1"])
		}
		if len(modelClient.requests) != 1 {
			t.Fatalf("PredictNext() requests = %d, want 1", len(modelClient.requests))
		}
		requestMessages := modelClient.requests[0].Messages
		if repair, ok := requestMessages[2].(llm.ToolErrorMessage); !ok || repair.ToolCallID != "interrupted" {
			t.Fatalf("request repair = %#v, want interrupted tool error", requestMessages[2])
		}
	})

	t.Run("persists prediction stop reason", func(t *testing.T) {
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{events: []llm.PredictionEvent{
				llm.BlockEnded{Block: llm.TextBlock{Text: "done"}},
				llm.PredictionFinished{StopReason: llm.StopReasonEndTurn},
			}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		events, errs := agt.Prompt(t.Context(), "hello")
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
		message := agt.Messages()[1].(llm.AssistantMessage)
		if message.StopReason != llm.StopReasonEndTurn {
			t.Fatalf("StopReason = %q, want end_turn", message.StopReason)
		}
	})

	t.Run("returns truncation error after persisting partial response", func(t *testing.T) {
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{events: []llm.PredictionEvent{
				llm.BlockEnded{Block: llm.TextBlock{Text: "partial"}},
				llm.PredictionFinished{StopReason: llm.StopReasonMaxTokens},
			}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		events, errs := agt.Prompt(t.Context(), "hello")
		_ = collectEvents(events)
		if err := <-errs; err == nil || !strings.Contains(err.Error(), "truncated") {
			t.Fatalf("Prompt() error = %v, want truncation error", err)
		}
		message := agt.Messages()[1].(llm.AssistantMessage)
		if message.StopReason != llm.StopReasonMaxTokens || message.Text() != "partial" {
			t.Fatalf("message = %#v, want persisted partial max_tokens response", message)
		}
	})

	t.Run("rejects a prediction without completion event", func(t *testing.T) {
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{events: []llm.PredictionEvent{
				llm.TextDelta{Text: "partial"},
			}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		events, errs := agt.Prompt(t.Context(), "hello")
		_ = collectEvents(events)
		if err := <-errs; err == nil || !strings.Contains(err.Error(), "without a completion event") {
			t.Fatalf("Prompt() error = %v, want missing completion error", err)
		}
	})

	t.Run("configured clock controls timestamps", func(t *testing.T) {
		want := time.Unix(1700000000, 123000000)
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{events: []llm.PredictionEvent{
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
		incrementalTool := fakeIncrementalTool{name: "stream", result: fakeResult{artifacts: []tool.Artifact{tool.ShellStreamArtifact{Stream: tool.ShellStreamStdout, Content: "final"}}}, artifacts: []tool.Artifact{tool.ShellStreamArtifact{Stream: tool.ShellStreamStdout, Content: "live"}}}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{eventBatches: [][]llm.PredictionEvent{
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
			Tools:    []runtime.Tool{incrementalTool},
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

		chunkIndex := eventIndex[runtime.ToolExecutionOutputDeltaReceived](gotEvents)
		resultIndex := eventIndex[runtime.ToolExecutionResultReceived](gotEvents)
		if chunkIndex == -1 {
			t.Fatal("streaming output event not found")
		}
		if resultIndex == -1 {
			t.Fatal("tool result event not found")
		}
		if chunkIndex > resultIndex {
			t.Fatalf("chunk event index %d after result event index %d", chunkIndex, resultIndex)
		}
		resultEvent, ok := gotEvents[resultIndex].(runtime.ToolExecutionResultReceived)
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

func TestToolOutputSummarization(t *testing.T) {
	toolCall := llm.ToolCallBlock{ID: "call-1", Name: "shell", Arguments: json.RawMessage(`{"command":"go test ./..."}`)}
	largeResult := map[string]string{"stdout": strings.Repeat("test output\n", 20)}

	t.Run("summarizes large allowed output and preserves raw output", func(t *testing.T) {
		summarizer := &fakeToolOutputSummarizer{summary: runtime.ToolOutputSummary{Summary: "tests failed in runtime/conversation_test.go:10", Omitted: true}}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{eventBatches: [][]llm.PredictionEvent{
				{llm.BlockEnded{Block: toolCall}, llm.PredictionFinished{}},
				{llm.TextDelta{Text: "done"}, llm.BlockEnded{Block: llm.TextBlock{Text: "done"}}, llm.PredictionFinished{}},
			}},
			Tools:                []runtime.Tool{fakeTool{name: "shell", result: largeResult}},
			MaxTurns:             2,
			ToolOutputSummarizer: summarizer,
			ToolOutputSummarizationPolicy: runtime.ToolOutputSummarizationPolicy{
				Enabled:  true,
				MinBytes: 1,
			},
		})
		if err != nil {
			t.Fatalf("NewConversation() error = %v", err)
		}

		events, errs := agt.Prompt(t.Context(), "run tests")
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}

		messages := agt.Messages()
		var output llm.ToolOutputMessage
		for _, message := range messages {
			if msg, ok := message.(llm.ToolOutputMessage); ok {
				output = msg
				break
			}
		}
		if !output.ToolOutputWasSummarized {
			t.Fatalf("ToolOutputWasSummarized = false, want true")
		}
		if output.RawToolOutput == "" || !strings.Contains(output.RawToolOutput, "test output") {
			t.Fatalf("RawToolOutput = %q, want original output", output.RawToolOutput)
		}
		if !strings.Contains(output.ToolOutput, "tests failed") || strings.Contains(output.ToolOutput, "test output\\n") {
			t.Fatalf("ToolOutput = %q, want summary without raw output", output.ToolOutput)
		}
		if summarizer.req.ToolName != "shell" || summarizer.req.ToolCallID != "call-1" {
			t.Fatalf("summary request tool = %q/%q, want shell/call-1", summarizer.req.ToolName, summarizer.req.ToolCallID)
		}
		if !strings.Contains(summarizer.req.Origin, "run tests") || !strings.Contains(summarizer.req.Origin, "go test ./...") {
			t.Fatalf("summary request origin = %q, want prompt and tool arguments", summarizer.req.Origin)
		}
	})

	t.Run("falls back to raw output when summarizer fails", func(t *testing.T) {
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{eventBatches: [][]llm.PredictionEvent{
				{llm.BlockEnded{Block: toolCall}, llm.PredictionFinished{}},
				{llm.TextDelta{Text: "done"}, llm.BlockEnded{Block: llm.TextBlock{Text: "done"}}, llm.PredictionFinished{}},
			}},
			Tools:                []runtime.Tool{fakeTool{name: "shell", result: largeResult}},
			MaxTurns:             2,
			ToolOutputSummarizer: &fakeToolOutputSummarizer{err: errors.New("summary failed")},
			ToolOutputSummarizationPolicy: runtime.ToolOutputSummarizationPolicy{
				Enabled:  true,
				MinBytes: 1,
			},
		})
		if err != nil {
			t.Fatalf("NewConversation() error = %v", err)
		}

		events, errs := agt.Prompt(t.Context(), "run tests")
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}

		for _, message := range agt.Messages() {
			output, ok := message.(llm.ToolOutputMessage)
			if !ok {
				continue
			}
			if output.ToolOutputWasSummarized || output.RawToolOutput != "" {
				t.Fatalf("output = %#v, want unsummarized raw output", output)
			}
			if !strings.Contains(output.ToolOutput, "test output") {
				t.Fatalf("ToolOutput = %q, want raw output", output.ToolOutput)
			}
			return
		}
		t.Fatal("tool output message not found")
	})

	t.Run("does not summarize excluded tool", func(t *testing.T) {
		summarizer := &fakeToolOutputSummarizer{summary: runtime.ToolOutputSummary{Summary: "summary"}}
		readCall := llm.ToolCallBlock{ID: "call-1", Name: "read_file", Arguments: json.RawMessage(`{}`)}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{eventBatches: [][]llm.PredictionEvent{
				{llm.BlockEnded{Block: readCall}, llm.PredictionFinished{}},
				{llm.TextDelta{Text: "done"}, llm.BlockEnded{Block: llm.TextBlock{Text: "done"}}, llm.PredictionFinished{}},
			}},
			Tools:                []runtime.Tool{fakeTool{name: "read_file", result: largeResult}},
			MaxTurns:             2,
			ToolOutputSummarizer: summarizer,
			ToolOutputSummarizationPolicy: runtime.ToolOutputSummarizationPolicy{
				Enabled:       true,
				MinBytes:      1,
				ExcludedTools: map[string]bool{"read_file": true},
			},
		})
		if err != nil {
			t.Fatalf("NewConversation() error = %v", err)
		}

		events, errs := agt.Prompt(t.Context(), "read file")
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
		if summarizer.called {
			t.Fatal("summarizer was called for excluded tool")
		}
	})
}

func TestNewConversation(t *testing.T) {
	t.Run("without persistence clears messages in memory", func(t *testing.T) {
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{},
			Messages:    []llm.Message{llm.UserMessage{Text: "old"}},
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

	t.Run("uses model client metadata when creating a new session", func(t *testing.T) {
		store := &fakeSessionStore{sessions: map[string]session.Session{"sess-1": {ID: "sess-1"}}, activeID: "sess-1"}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient:  &fakeModelClient{model: llm.Model{Provider: "provider-a", Name: "model-a"}, reasoningLevel: llm.ReasoningLevelHigh},
			SessionStore: store,
			Session: session.Session{
				ID:             "sess-1",
				Model:          config.Model{Provider: "stale", Name: "old"},
				ReasoningLevel: "off",
			},
			Messages: []llm.Message{llm.UserMessage{Text: "old"}},
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if err := agt.NewConversation(); err != nil {
			t.Fatalf("NewConversation() error = %v", err)
		}
		if got := store.sessions["new-sess-123"]; got.Model != (config.Model{Provider: "provider-a", Name: "model-a"}) || got.ReasoningLevel != string(llm.ReasoningLevelHigh) {
			t.Fatalf("created session metadata = %#v, want model client metadata", got)
		}
	})

	t.Run("preserves existing messages when create fails", func(t *testing.T) {
		store := &fakeSessionStore{
			sessions:  map[string]session.Session{"sess-1": {ID: "sess-1"}},
			activeID:  "sess-1",
			createErr: errors.New("create failed"),
		}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient:  &fakeModelClient{},
			SessionStore: store,
			Session:      session.Session{ID: "sess-1"},
			Messages:     []llm.Message{llm.UserMessage{Text: "old"}},
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
		if err := llm.RegisterModel(model, func(level llm.ReasoningLevel) (llm.ModelClient, error) {
			return &fakeModelClient{model: model, reasoningLevel: level}, nil
		}); err != nil {
			t.Fatalf("RegisterModel() error = %v", err)
		}

		store := &fakeSessionStore{
			sessions: map[string]session.Session{"sess-1": {ID: "sess-1", Model: config.Model{Provider: "test", Name: "original"}}},
			activeID: "sess-1",
			saveErr:  errors.New("save failed"),
		}
		originalModel := llm.Model{Provider: "test", Name: "original"}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient:  &fakeModelClient{model: originalModel},
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
		original := &fakeModelClient{model: llm.Model{Provider: "test", Name: "reasoning"}, reasoningLevel: llm.ReasoningLevelOff}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: original, SessionStore: store, Session: store.sessions[store.activeID]})
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
	t.Run("SwitchModel selects nearest supported reasoning level", func(t *testing.T) {
		model := llm.Model{
			Provider:           "test",
			Name:               "nearest-reasoning",
			SupportedReasoning: llm.NewReasoningSet(llm.ReasoningLevelOff, llm.ReasoningLevelHigh),
		}
		if err := llm.RegisterModel(model, func(level llm.ReasoningLevel) (llm.ModelClient, error) {
			return &fakeModelClient{model: model, reasoningLevel: level}, nil
		}); err != nil {
			t.Fatalf("RegisterModel() error = %v", err)
		}
		store := &fakeSessionStore{sessions: map[string]session.Session{"sess-nearest": {ID: "sess-nearest"}}, activeID: "sess-nearest"}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient:  &fakeModelClient{reasoningLevel: llm.ReasoningLevelMedium},
			SessionStore: store,
			Session:      store.sessions[store.activeID],
		})
		if err != nil {
			t.Fatalf("NewConversation() error = %v", err)
		}
		if err := agt.SwitchModel(model); err != nil {
			t.Fatalf("SwitchModel() error = %v", err)
		}
		if got := agt.ReasoningLevel(); got != llm.ReasoningLevelHigh {
			t.Fatalf("reasoning level = %q, want high", got)
		}
		if got := store.sessions[store.activeID].ReasoningLevel; got != string(llm.ReasoningLevelHigh) {
			t.Fatalf("stored reasoning level = %q, want high", got)
		}
	})
}

func TestRewindAndFork(t *testing.T) {
	store := &fakeSessionStore{
		sessions: map[string]session.Session{
			"source": {ID: "source", Title: "Source", WorkingDir: t.TempDir(), Model: config.Model{Provider: "test", Name: "model"}, ReasoningLevel: "high"},
		},
		messages: map[string][]llm.Message{}, activeID: "source",
	}
	messages := []llm.Message{
		llm.UserMessage{Text: "<compacted_context>summary</compacted_context>"},
		llm.UserMessage{Text: "first"},
		llm.AssistantMessage{Blocks: []llm.AssistantBlock{llm.TextBlock{Text: "answer"}}},
		llm.UserMessage{Text: "second"},
	}
	store.messages["source"] = append([]llm.Message(nil), messages...)
	conversation, err := runtime.NewConversation(runtime.ConversationConfig{
		ModelClient: &fakeModelClient{}, SessionStore: store, Session: store.sessions["source"], Messages: messages,
		SessionCost: llm.SessionCost{Total: 1.25, Available: true},
	})
	if err != nil {
		t.Fatalf("NewConversation() error = %v", err)
	}

	points := conversation.RewindPoints()
	if len(points) != 2 || points[0].Prompt != "first" || points[1].Prompt != "second" {
		t.Fatalf("RewindPoints() = %#v", points)
	}
	if err := conversation.Fork(t.Context(), points[1]); err != nil {
		t.Fatalf("Fork() error = %v", err)
	}
	if got := conversation.SessionID(); got != "fork-sess-123" {
		t.Fatalf("SessionID() = %q, want fork-sess-123", got)
	}
	if got := conversation.Messages(); len(got) != 3 {
		t.Fatalf("fork messages = %#v, want retained prefix", got)
	}
	if source := store.messages["source"]; len(source) != 4 {
		t.Fatalf("fork changed source messages: %#v", source)
	}

	forkPoints := conversation.RewindPoints()
	if err := conversation.Rewind(t.Context(), forkPoints[0]); err != nil {
		t.Fatalf("Rewind() error = %v", err)
	}
	got := conversation.Messages()
	if len(got) != 1 || got[0].(llm.UserMessage).Text != "<compacted_context>summary</compacted_context>" {
		t.Fatalf("rewound messages = %#v", got)
	}
}

func TestCompactConversation(t *testing.T) {
	t.Run("updates_messages_on_success", func(t *testing.T) {
		compacted := []llm.Message{llm.UserMessage{Text: "compacted"}}
		compactor := &fakeCompactor{messages: compacted}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{},
			Compactor:   compactor,
			Messages:    []llm.Message{llm.UserMessage{Text: "old"}},
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
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{},
			Compactor:   &fakeCompactor{err: wantErr},
			Messages:    []llm.Message{llm.UserMessage{Text: "old"}},
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
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: &fakeModelClient{},
			Compactor:   compactor,
			Messages:    []llm.Message{llm.UserMessage{Text: "old"}},
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

	t.Run("default compactor follows switched model", func(t *testing.T) {
		originalClient := &fakeModelClient{structuredRaw: json.RawMessage(`{"current_goal":"wrong model"}`)}
		switchedClient := &fakeModelClient{structuredRaw: json.RawMessage(`{"current_goal":"switched model"}`)}
		switchedModel := llm.Model{Provider: "test", Name: "compactor-switch"}
		if err := llm.RegisterModel(switchedModel, func(llm.ReasoningLevel) (llm.ModelClient, error) {
			return switchedClient, nil
		}); err != nil {
			t.Fatalf("RegisterModel() error = %v", err)
		}
		compactor, err := runtime.NewDefaultCompactor(runtime.DefaultCompactorConfig{ModelClient: originalClient})
		if err != nil {
			t.Fatalf("NewDefaultCompactor() error = %v", err)
		}
		messages := make([]llm.Message, 14)
		for i := range messages {
			messages[i] = llm.UserMessage{Text: fmt.Sprintf("message %d", i)}
		}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient: originalClient,
			Compactor:   compactor,
			Messages:    messages,
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if err := agt.SwitchModel(switchedModel); err != nil {
			t.Fatalf("SwitchModel() error = %v", err)
		}
		if err := agt.CompactConversation(t.Context()); err != nil {
			t.Fatalf("CompactConversation() error = %v", err)
		}
		if switchedClient.structuredCalls != 1 {
			t.Fatalf("switched model structured calls = %d, want 1", switchedClient.structuredCalls)
		}
		if originalClient.structuredCalls != 0 {
			t.Fatalf("original model structured calls = %d, want 0", originalClient.structuredCalls)
		}
	})

	t.Run("requires_configured_compactor", func(t *testing.T) {
		agt, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: &fakeModelClient{}})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		err = agt.CompactConversation(context.Background())
		if err == nil || !strings.Contains(err.Error(), "compactor") {
			t.Fatalf("CompactConversation() error = %v, want compactor error", err)
		}
	})
}

func collectEvents(events <-chan runtime.Event) []runtime.Event {
	var collected []runtime.Event
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

func eventIndex[T runtime.Event](events []runtime.Event) int {
	for i, event := range events {
		if _, ok := event.(T); ok {
			return i
		}
	}
	return -1
}

func assertEventType[T runtime.Event](t *testing.T, events []runtime.Event, _ T) {
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

func assertLastEventType[T runtime.Event](t *testing.T, events []runtime.Event, _ T) {
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

type fakeModelClient struct {
	events          []llm.PredictionEvent
	eventBatches    [][]llm.PredictionEvent
	model           llm.Model
	reasoningLevel  llm.ReasoningLevel
	err             error
	predictCalls    int
	structuredRaw   json.RawMessage
	structuredCalls int
	requests        []llm.PredictNextRequest
}

func (f *fakeModelClient) Model() llm.Model                   { return f.model }
func (f *fakeModelClient) ReasoningLevel() llm.ReasoningLevel { return f.reasoningLevel }
func (f *fakeModelClient) SetReasoningLevel(level llm.ReasoningLevel) error {
	f.reasoningLevel = level
	return nil
}
func (f *fakeModelClient) PredictNext(_ context.Context, req llm.PredictNextRequest) (<-chan llm.PredictionEvent, <-chan error) {
	f.requests = append(f.requests, req)
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
func (f *fakeModelClient) PredictNextStructured(context.Context, llm.PredictNextStructuredRequest) (json.RawMessage, error) {
	f.structuredCalls++
	return f.structuredRaw, nil
}

type fakeToolOutputSummarizer struct {
	called  bool
	req     runtime.ToolOutputSummaryRequest
	summary runtime.ToolOutputSummary
	err     error
}

func (f *fakeToolOutputSummarizer) SummarizeToolOutput(_ context.Context, req runtime.ToolOutputSummaryRequest) (runtime.ToolOutputSummary, error) {
	f.called = true
	f.req = req
	if f.err != nil {
		return runtime.ToolOutputSummary{}, f.err
	}
	return f.summary, nil
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
	sessions           map[string]session.Session
	messages           map[string][]llm.Message
	activeID           string
	saveErr            error
	createErr          error
	cancelOnAppend     context.CancelFunc
	metadataContextErr error
}

func (f *fakeSessionStore) Latest(context.Context, string) (session.Session, []llm.Message, bool, error) {
	sess, ok := f.sessions[f.activeID]
	return sess, append([]llm.Message(nil), f.messages[f.activeID]...), ok, nil
}

func (f *fakeSessionStore) Load(_ context.Context, id string) (session.Session, []llm.Message, bool, error) {
	sess, ok := f.sessions[id]
	return sess, append([]llm.Message(nil), f.messages[id]...), ok, nil
}

func (f *fakeSessionStore) Create(_ context.Context, workingDir string, metadata session.Metadata) (session.Session, error) {
	if f.createErr != nil {
		return session.Session{}, f.createErr
	}
	sess := session.Session{ID: "new-sess-123", WorkingDir: workingDir, Title: metadata.Title, Model: metadata.Model, ReasoningLevel: metadata.ReasoningLevel}
	if f.sessions == nil {
		f.sessions = make(map[string]session.Session)
	}
	f.sessions[sess.ID] = sess
	f.activeID = sess.ID
	return sess, nil
}

func (f *fakeSessionStore) Append(_ context.Context, id string, event session.Event) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	if f.messages == nil {
		f.messages = make(map[string][]llm.Message)
	}
	if event.Type == session.EventCompaction || event.Type == session.EventContextReset {
		f.messages[id] = append([]llm.Message(nil), event.Compacted...)
	} else {
		f.messages[id] = append(f.messages[id], event.Message)
	}
	if f.cancelOnAppend != nil {
		f.cancelOnAppend()
		f.cancelOnAppend = nil
	}
	return nil
}

func (f *fakeSessionStore) Fork(_ context.Context, parentID string, metadata session.Metadata, event session.Event) (session.Session, error) {
	if f.saveErr != nil {
		return session.Session{}, f.saveErr
	}
	parent := f.sessions[parentID]
	child := session.Session{
		ID: "fork-sess-123", ParentID: parentID, WorkingDir: parent.WorkingDir, Title: metadata.Title,
		Model: metadata.Model, ReasoningLevel: metadata.ReasoningLevel, Cost: llm.SessionCost{Available: true},
	}
	f.sessions[child.ID] = child
	f.messages[child.ID] = append([]llm.Message(nil), event.Compacted...)
	f.activeID = child.ID
	return child, nil
}

func (f *fakeSessionStore) UpdateMetadata(ctx context.Context, id string, metadata session.Metadata) error {
	f.metadataContextErr = ctx.Err()
	if f.saveErr != nil {
		return f.saveErr
	}
	record := f.sessions[id]
	record.Title = metadata.Title
	record.Model = metadata.Model
	record.ReasoningLevel = metadata.ReasoningLevel
	f.sessions[id] = record
	return nil
}

func (f *fakeSessionStore) List(context.Context, string) ([]session.Ref, error) {
	refs := make([]session.Ref, 0, len(f.sessions))
	for id := range f.sessions {
		refs = append(refs, session.Ref{ID: id})
	}
	return refs, nil
}
func (f *fakeSessionStore) Delete(_ context.Context, id string) error {
	delete(f.sessions, id)
	delete(f.messages, id)
	return nil
}
func (f *fakeSessionStore) Clear(context.Context, string) error {
	f.sessions = nil
	f.messages = nil
	return nil
}

func TestSessionPersistence(t *testing.T) {
	t.Run("loads messages from ActiveSession when Messages is empty", func(t *testing.T) {
		activeSess := session.Session{ID: "sess-1"}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: &fakeModelClient{}, Session: activeSess, Messages: []llm.Message{llm.UserMessage{Text: "restored text"}}})
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
		agt, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: &fakeModelClient{events: []llm.PredictionEvent{llm.TextDelta{Text: "reply"}, llm.BlockEnded{Block: llm.TextBlock{Text: "reply"}}, llm.PredictionFinished{}}}, SessionStore: store, Session: store.sessions[store.activeID]})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		events, errs := agt.Prompt(t.Context(), "hello")
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
		saved := store.messages["sess-1"]
		if len(saved) != 2 {
			t.Fatalf("expected 2 messages in saved session, got: %d", len(saved))
		}
		if saved[0].(llm.UserMessage).Text != "hello" {
			t.Fatalf("expected first message to be 'hello', got: %q", saved[0].(llm.UserMessage).Text)
		}
	})

	t.Run("derives session title from first prompt", func(t *testing.T) {
		store := &fakeSessionStore{sessions: map[string]session.Session{"sess-1": {ID: "sess-1", Title: "New session"}}, activeID: "sess-1"}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: &fakeModelClient{events: []llm.PredictionEvent{llm.TextDelta{Text: "reply"}, llm.BlockEnded{Block: llm.TextBlock{Text: "reply"}}, llm.PredictionFinished{}}}, SessionStore: store, Session: store.sessions[store.activeID]})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		events, errs := agt.Prompt(t.Context(), "Add a login page")
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
		if got := store.sessions["sess-1"].Title; got != "Add a login page" {
			t.Fatalf("title = %q, want %q", got, "Add a login page")
		}
		if got := agt.SessionTitle(); got != "Add a login page" {
			t.Fatalf("SessionTitle() = %q, want %q", got, "Add a login page")
		}

		// A second prompt must not overwrite the established title.
		events, errs = agt.Prompt(t.Context(), "Now add logout")
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("Prompt() error = %v", err)
		}
		if got := store.sessions["sess-1"].Title; got != "Add a login page" {
			t.Fatalf("title after second prompt = %q, want unchanged", got)
		}
	})

	t.Run("persists first title when prompt is canceled after the user message", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		store := &fakeSessionStore{
			sessions:       map[string]session.Session{"sess-1": {ID: "sess-1", Title: "New session"}},
			activeID:       "sess-1",
			cancelOnAppend: cancel,
		}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{
			ModelClient:  &fakeModelClient{},
			SessionStore: store,
			Session:      store.sessions[store.activeID],
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		events, errs := agt.Prompt(ctx, "Keep this title")
		_ = collectEvents(events)
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Fatalf("Prompt() error = %v, want context canceled", err)
		}
		if got := store.sessions["sess-1"].Title; got != "Keep this title" {
			t.Fatalf("stored title = %q, want %q", got, "Keep this title")
		}
		if store.metadataContextErr != nil {
			t.Fatalf("UpdateMetadata() context error = %v, want cancellation detached", store.metadataContextErr)
		}
	})

	t.Run("emits SessionSaveFailed event and returns error on save failure", func(t *testing.T) {
		store := &fakeSessionStore{sessions: map[string]session.Session{"sess-1": {ID: "sess-1"}}, activeID: "sess-1", saveErr: errors.New("disk full")}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: &fakeModelClient{events: []llm.PredictionEvent{llm.TextDelta{Text: "reply"}, llm.BlockEnded{Block: llm.TextBlock{Text: "reply"}}, llm.PredictionFinished{}}}, SessionStore: store, Session: store.sessions[store.activeID]})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		events, errs := agt.Prompt(t.Context(), "hello")
		gotEvents := collectEvents(events)
		gotErr := <-errs
		if gotErr == nil || !strings.Contains(gotErr.Error(), "disk full") {
			t.Fatalf("Prompt() error = %v, want disk full error", gotErr)
		}
		assertEventType(t, gotEvents, runtime.SessionSaveFailed{})
	})

	t.Run("saves session after SwitchModel and SwitchReasoningLevel", func(t *testing.T) {
		store := &fakeSessionStore{sessions: map[string]session.Session{"sess-1": {ID: "sess-1"}}, activeID: "sess-1"}
		_ = llm.RegisterModel(llm.Model{Provider: "openai", Name: "gpt-4"}, func(lvl llm.ReasoningLevel) (llm.ModelClient, error) {
			return &fakeModelClient{model: llm.Model{Provider: "openai", Name: "gpt-4"}, reasoningLevel: lvl}, nil
		})
		agt, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: &fakeModelClient{model: llm.Model{Provider: "openai", Name: "gpt-3.5"}}, SessionStore: store, Session: store.sessions[store.activeID]})
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

	t.Run("creates a new active session without clearing existing sessions", func(t *testing.T) {
		store := &fakeSessionStore{sessions: map[string]session.Session{"sess-1": {ID: "sess-1"}}, activeID: "sess-1"}
		agt, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: &fakeModelClient{}, SessionStore: store, Session: store.sessions[store.activeID]})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if err := agt.NewConversation(); err != nil {
			t.Fatalf("NewConversation() error = %v", err)
		}
		if store.activeID != "new-sess-123" {
			t.Fatalf("active session ID = %q, want new-sess-123", store.activeID)
		}
		if _, ok := store.sessions["sess-1"]; !ok {
			t.Fatalf("existing session sess-1 was removed, want preserved")
		}
	})
}
