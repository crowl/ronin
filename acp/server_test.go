package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/tool"
)

func TestInitializeIncludesAgentInfoVersion(t *testing.T) {
	conv := &fakeConversation{cwd: "/repo"}
	out := runServer(t, conv, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`+"\n")

	messages := parseMessages(t, out)
	result := messages[0]["result"].(map[string]any)
	agentInfo := result["agentInfo"].(map[string]any)
	if agentInfo["version"] == "" {
		t.Fatalf("agentInfo.version missing: %#v", agentInfo)
	}
}

func TestInitializeAllowsNestedMeta(t *testing.T) {
	conv := &fakeConversation{cwd: "/repo"}
	out := runServer(t, conv, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientInfo":{"_meta":{"vendor":"jetbrains"},"name":"jetbrains","title":"JetBrains"}}}`+"\n")

	messages := parseMessages(t, out)
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	if messages[0]["error"] != nil {
		t.Fatalf("initialize returned error: %v", messages[0]["error"])
	}
}

func TestInitializeAllowsMeta(t *testing.T) {
	conv := &fakeConversation{cwd: "/repo"}
	out := runServer(t, conv, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"_meta":{"client":"jetbrains"},"protocolVersion":1}}`+"\n")

	messages := parseMessages(t, out)
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	if messages[0]["error"] != nil {
		t.Fatalf("initialize returned error: %v", messages[0]["error"])
	}
}

func TestSessionNewAllowsMeta(t *testing.T) {
	conv := &fakeConversation{cwd: "/repo"}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"_meta":{"client":"jetbrains"},"cwd":"/repo"}}`,
	}, "\n") + "\n"

	out := runServer(t, conv, input)
	messages := parseMessages(t, out)
	last := messages[len(messages)-1]
	if last["error"] != nil {
		t.Fatalf("session/new returned error: %v", last["error"])
	}
}

func TestInitialize(t *testing.T) {
	conv := &fakeConversation{cwd: "/repo"}
	out := runServer(t, conv, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`+"\n")

	messages := parseMessages(t, out)
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	if messages[0]["error"] != nil {
		t.Fatalf("initialize returned error: %v", messages[0]["error"])
	}
	result := messages[0]["result"].(map[string]any)
	if got := result["protocolVersion"]; got != float64(1) {
		t.Fatalf("protocolVersion = %v, want 1", got)
	}
}

func TestSessionPromptStreamsAssistantAndToolUpdates(t *testing.T) {
	conv := &fakeConversation{
		cwd: "/repo",
		events: []runtime.Event{
			runtime.AssistantMessageDeltaReceived{Text: "hello"},
			runtime.ToolExecutionStarted{Tool: fakeTool{name: "shell"}, CallID: "call-1", CallTitle: "Run tests", CallArguments: json.RawMessage(`{"command":"go test ./..."}`)},
			runtime.ToolExecutionOutputDeltaReceived{Tool: fakeTool{name: "shell"}, CallID: "call-1", Artifact: tool.TextArtifact{Text: "ok"}},
			runtime.ToolExecutionResultReceived{Tool: fakeTool{name: "shell"}, CallID: "call-1"},
		},
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/repo"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"ronin-session","prompt":[{"type":"text","text":"hi"}]}}`,
	}, "\n") + "\n"

	out := runServer(t, conv, input)
	messages := parseMessages(t, out)
	if len(messages) != 7 {
		t.Fatalf("got %d messages, want 7: %s", len(messages), out)
	}

	var sawAssistant, sawTool, sawCompleted bool
	for _, msg := range messages {
		if msg["method"] != "session/update" {
			continue
		}
		params := msg["params"].(map[string]any)
		update := params["update"].(map[string]any)
		switch update["sessionUpdate"] {
		case "agent_message_chunk":
			sawAssistant = true
		case "tool_call":
			sawTool = true
		case "tool_call_update":
			if update["status"] == "completed" {
				sawCompleted = true
			}
		}
	}
	if !sawAssistant || !sawTool || !sawCompleted {
		t.Fatalf("missing expected updates: assistant=%v tool=%v completed=%v", sawAssistant, sawTool, sawCompleted)
	}

	last := messages[len(messages)-1]
	result := last["result"].(map[string]any)
	if result["stopReason"] != "end_turn" {
		t.Fatalf("stopReason = %v, want end_turn", result["stopReason"])
	}
	if conv.prompt != "hi" {
		t.Fatalf("prompt = %q, want hi", conv.prompt)
	}
}

func TestCancelReturnsCancelledStopReason(t *testing.T) {
	conv := &fakeConversation{cwd: "/repo", block: make(chan struct{})}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/repo"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"ronin-session","prompt":[{"type":"text","text":"hi"}]}}`,
		`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"ronin-session"}}`,
	}, "\n") + "\n"

	out := runServer(t, conv, input)
	messages := parseMessages(t, out)
	last := messages[len(messages)-1]
	result := last["result"].(map[string]any)
	if result["stopReason"] != "cancelled" {
		t.Fatalf("stopReason = %v, want cancelled; output %s", result["stopReason"], out)
	}
}

func TestUnsupportedPromptContentReturnsInvalidParams(t *testing.T) {
	conv := &fakeConversation{cwd: "/repo"}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/repo"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"ronin-session","prompt":[{"type":"image"}]}}`,
	}, "\n") + "\n"

	out := runServer(t, conv, input)
	messages := parseMessages(t, out)
	last := messages[len(messages)-1]
	errObj := last["error"].(map[string]any)
	if errObj["code"] != float64(errInvalidParams) {
		t.Fatalf("error code = %v, want %d", errObj["code"], errInvalidParams)
	}
}

func runServer(t *testing.T, conv *fakeConversation, input string) string {
	t.Helper()
	reader, writer := io.Pipe()
	var out strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Config{Conversation: conv, Input: reader, Output: &out})
	}()
	_, err := io.WriteString(writer, input)
	if err != nil {
		t.Fatalf("write input: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := writer.Close(); err != nil {
		t.Fatalf("close input: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	return out.String()
}

func parseMessages(t *testing.T, out string) []map[string]any {
	t.Helper()
	var messages []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Fatalf("invalid JSON message %q: %v", scanner.Text(), err)
		}
		messages = append(messages, msg)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return messages
}

type fakeConversation struct {
	cwd    string
	events []runtime.Event
	block  chan struct{}
	prompt string
}

func (f *fakeConversation) Prompt(ctx context.Context, prompt string) (<-chan runtime.Event, <-chan error) {
	f.prompt = prompt
	events := make(chan runtime.Event, len(f.events))
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		if f.block != nil {
			<-ctx.Done()
			errs <- ctx.Err()
			return
		}
		for _, event := range f.events {
			events <- event
		}
	}()
	return events, errs
}

func (f *fakeConversation) CWD() string                                        { return f.cwd }
func (f *fakeConversation) Model() llm.Model                                   { return llm.Model{} }
func (f *fakeConversation) ReasoningLevel() llm.ReasoningLevel                 { return llm.ReasoningLevelOff }
func (f *fakeConversation) ContextUsage() llm.Usage                            { return llm.Usage{} }
func (f *fakeConversation) ToolCallTitle(name string, arguments []byte) string { return name }

type fakeTool struct{ name string }

func (f fakeTool) Name() string                                       { return f.name }
func (f fakeTool) Description() string                                { return "" }
func (f fakeTool) Parameters() *jsonschema.Schema                     { return nil }
func (f fakeTool) Call(context.Context, json.RawMessage) (any, error) { return nil, nil }
