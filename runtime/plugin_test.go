package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/plugin"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/tool"
)

// runToolPrompt runs one prompt where the model calls "echo" with args and
// then finishes. It returns the persisted messages and UI events.
func runToolPrompt(t *testing.T, host *plugin.Host, args json.RawMessage, echo *argTool) ([]session.Message, []runtime.Event) {
	t.Helper()
	client := &fakeModelClient{model: llm.Model{Provider: "test", Name: "model"}, eventBatches: [][]llm.PredictionEvent{
		{llm.BlockEnded{Block: llm.ToolCallBlock{ID: "call-1", Name: "echo", Arguments: args}}, llm.PredictionFinished{}},
		{llm.PredictionFinished{}},
	}}
	conv, err := runtime.NewConversation(runtime.ConversationConfig{CWD: "/work", ModelClient: client, Tools: []runtime.Tool{echo}})
	if err != nil {
		t.Fatal(err)
	}
	events, errs := conv.Prompt(plugin.NewContext(t.Context(), host), "go")
	collected := collectEvents(events)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	return conv.Messages(), collected
}

func TestPluginGateDeniesToolCall(t *testing.T) {
	echo := &argTool{}
	policy := &recordingPlugin{gate: func(c plugin.ToolCall) (plugin.Decision, error) {
		if c.Name == "echo" && c.WorkingDir == "/work" {
			return plugin.Deny("echo is not allowed here"), nil
		}
		return plugin.Allow(), nil
	}}
	messages, events := runToolPrompt(t, plugin.NewHost(policy), json.RawMessage(`{"x":1}`), echo)
	if echo.calls != 0 {
		t.Fatal("denied tool executed")
	}
	toolErr := findToolError(t, messages, "call-1")
	if !strings.Contains(toolErr.Error.Error(), "echo is not allowed here") {
		t.Fatalf("model did not receive reason: %v", toolErr.Error)
	}
	var failed *runtime.ToolExecutionFailed
	for _, e := range events {
		if f, ok := e.(runtime.ToolExecutionFailed); ok {
			failed = &f
		}
	}
	if failed == nil || failed.CallID != "call-1" {
		t.Fatalf("UI not told about denial: %+v", events)
	}
	var denied *plugin.ToolCallDenied
	for _, e := range policy.events {
		if d, ok := e.(plugin.ToolCallDenied); ok {
			denied = &d
		}
	}
	if denied == nil || denied.Rejected || !errors.As(denied.Err, new(*plugin.DeniedError)) {
		t.Fatalf("denial event = %+v", denied)
	}
}

func TestPluginGateRewritesArguments(t *testing.T) {
	echo := &argTool{}
	rewriter := &recordingPlugin{gate: func(plugin.ToolCall) (plugin.Decision, error) {
		return plugin.Rewrite(json.RawMessage(`{"x":2}`)), nil
	}}
	_, events := runToolPrompt(t, plugin.NewHost(rewriter), json.RawMessage(`{"x":1}`), echo)
	if echo.calls != 1 || string(echo.lastArgs) != `{"x":2}` {
		t.Fatalf("tool saw %s after %d calls", echo.lastArgs, echo.calls)
	}
	for _, e := range events {
		if s, ok := e.(runtime.ToolExecutionStarted); ok && string(s.CallArguments) != `{"x":2}` {
			t.Fatalf("UI shows ungated arguments: %s", s.CallArguments)
		}
	}
	for _, e := range rewriter.events {
		if s, ok := e.(plugin.ToolCallStarted); ok && string(s.Call.Arguments) != `{"x":2}` {
			t.Fatalf("started event has ungated arguments: %s", s.Call.Arguments)
		}
	}
}

func TestPluginFilterRewritesModelVisibleResult(t *testing.T) {
	echo := &argTool{}
	redactor := &recordingPlugin{filter: func(_ plugin.ToolCall, r json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(strings.ReplaceAll(string(r), "secret", "[redacted]")), nil
	}}
	messages, _ := runToolPrompt(t, plugin.NewHost(redactor), json.RawMessage(`{"x":"secret"}`), echo)
	output := findToolOutput(t, messages, "call-1")
	if strings.Contains(output.ToolOutput, "secret") || !strings.Contains(output.ToolOutput, "[redacted]") {
		t.Fatalf("output not filtered: %s", output.ToolOutput)
	}
	for _, e := range redactor.events {
		if ended, ok := e.(plugin.ToolCallEnded); ok && ended.ResultSize != len(output.ToolOutput) {
			t.Fatalf("result size %d != %d", ended.ResultSize, len(output.ToolOutput))
		}
	}
}

func TestModelTextResultReachesModelUnescaped(t *testing.T) {
	echo := &argTool{text: true}
	messages, _ := runToolPrompt(t, plugin.NewHost(), json.RawMessage(`{"x":"a\nb"}`), echo)
	output := findToolOutput(t, messages, "call-1")
	if output.ToolOutput != "plain: {\"x\":\"a\\nb\"}" {
		t.Fatalf("model text not used: %q", output.ToolOutput)
	}
}

func TestPluginFilterOverridesModelText(t *testing.T) {
	echo := &argTool{text: true}
	redactor := &recordingPlugin{filter: func(_ plugin.ToolCall, r json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(strings.ReplaceAll(string(r), "secret", "[redacted]")), nil
	}}
	messages, _ := runToolPrompt(t, plugin.NewHost(redactor), json.RawMessage(`{"x":"secret"}`), echo)
	output := findToolOutput(t, messages, "call-1")
	if strings.Contains(output.ToolOutput, "secret") || strings.HasPrefix(output.ToolOutput, "plain:") {
		t.Fatalf("filtered JSON must win over model text: %s", output.ToolOutput)
	}
}

func TestPluginFilterFailureFailsToolCall(t *testing.T) {
	echo := &argTool{}
	broken := &recordingPlugin{filter: func(plugin.ToolCall, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("filter broke")
	}}
	messages, _ := runToolPrompt(t, plugin.NewHost(broken), json.RawMessage(`{}`), echo)
	toolErr := findToolError(t, messages, "call-1")
	if !strings.Contains(toolErr.Error.Error(), "filter broke") {
		t.Fatalf("error = %v", toolErr.Error)
	}
}

func TestPluginEventsFormOperationTree(t *testing.T) {
	observer := &recordingPlugin{}
	runToolPrompt(t, plugin.NewHost(observer), json.RawMessage(`{}`), &argTool{})
	var kinds []string
	var prompt plugin.PromptTurnStarted
	cycles := map[string]plugin.CycleStarted{}
	for _, e := range observer.events {
		kinds = append(kinds, strings.TrimPrefix(fmt.Sprintf("%T", e), "plugin."))
		switch typed := e.(type) {
		case plugin.PromptTurnStarted:
			prompt = typed
		case plugin.CycleStarted:
			cycles[typed.ID] = typed
			if typed.ParentID != prompt.ID {
				t.Fatalf("cycle parent %q != prompt %q", typed.ParentID, prompt.ID)
			}
		case plugin.ModelRequestStarted:
			if _, ok := cycles[typed.ParentID]; !ok || typed.Purpose != "conversation" {
				t.Fatalf("request outside cycle: %+v", typed)
			}
		case plugin.ToolCallStarted:
			if _, ok := cycles[typed.ParentID]; !ok || typed.Model.Name != "model" {
				t.Fatalf("tool outside cycle: %+v", typed)
			}
		case plugin.PromptTurnEnded:
			if typed.ID != prompt.ID || typed.Cycles != 2 || typed.Err != nil {
				t.Fatalf("prompt end = %+v", typed)
			}
		}
	}
	want := "PromptTurnStarted CycleStarted ModelRequestStarted ModelRequestEnded ToolCallStarted ToolCallEnded CycleEnded CycleStarted ModelRequestStarted ModelRequestEnded CycleEnded PromptTurnEnded"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
}

func findToolError(t *testing.T, messages []session.Message, callID string) llm.ToolErrorMessage {
	t.Helper()
	for _, m := range messages {
		if e, ok := m.(llm.ToolErrorMessage); ok && e.ToolCallID == callID {
			return e
		}
	}
	t.Fatalf("no tool error for %s in %v", callID, messages)
	return llm.ToolErrorMessage{}
}

func findToolOutput(t *testing.T, messages []session.Message, callID string) llm.ToolOutputMessage {
	t.Helper()
	for _, m := range messages {
		if o, ok := m.(llm.ToolOutputMessage); ok && o.ToolCallID == callID {
			return o
		}
	}
	t.Fatalf("no tool output for %s in %v", callID, messages)
	return llm.ToolOutputMessage{}
}

// argTool echoes its arguments and counts invocations.
type argTool struct {
	calls    int
	lastArgs json.RawMessage
	// text makes results implement tool.ModelTextResult.
	text bool
}

func (a *argTool) Name() string                   { return "echo" }
func (a *argTool) Description() string            { return "echoes arguments" }
func (a *argTool) Parameters() *jsonschema.Schema { return &jsonschema.Schema{Type: "object"} }
func (a *argTool) Call(_ context.Context, args json.RawMessage) (any, error) {
	a.calls++
	a.lastArgs = args
	if a.text {
		return textResult{Echo: args}, nil
	}
	return map[string]any{"echo": args}, nil
}

type textResult struct {
	Echo json.RawMessage `json:"echo"`
}

func (textResult) Artifacts() []tool.Artifact { return nil }
func (r textResult) ModelText() string        { return "plain: " + string(r.Echo) }

type recordingPlugin struct {
	events []plugin.Event
	gate   func(plugin.ToolCall) (plugin.Decision, error)
	filter func(plugin.ToolCall, json.RawMessage) (json.RawMessage, error)
}

func (r *recordingPlugin) Name() string { return "recording" }
func (r *recordingPlugin) Observe(_ context.Context, e plugin.Event) {
	r.events = append(r.events, e)
}
func (r *recordingPlugin) GateToolCall(_ context.Context, c plugin.ToolCall) (plugin.Decision, error) {
	if r.gate == nil {
		return plugin.Allow(), nil
	}
	return r.gate(c)
}
func (r *recordingPlugin) FilterToolResult(_ context.Context, c plugin.ToolCall, res json.RawMessage) (json.RawMessage, error) {
	if r.filter == nil {
		return res, nil
	}
	return r.filter(c, res)
}
