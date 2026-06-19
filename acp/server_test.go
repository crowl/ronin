package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"
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

func TestSessionNewAdvertisesSlashCommands(t *testing.T) {
	conv := &fakeConversation{cwd: "/repo"}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/repo"}}`,
	}, "\n") + "\n"

	out := runServer(t, conv, input)
	messages := parseMessages(t, out)
	if len(messages) != 3 {
		t.Fatalf("got %d messages, want 3: %s", len(messages), out)
	}
	result := messages[0]["result"].(map[string]any)
	caps := result["agentCapabilities"].(map[string]any)
	if _, ok := caps["slashCommands"]; ok {
		t.Fatalf("slash commands should not be advertised in initialize: %#v", caps["slashCommands"])
	}

	update := messages[2]["params"].(map[string]any)["update"].(map[string]any)
	if update["sessionUpdate"] != "available_commands_update" {
		t.Fatalf("sessionUpdate = %v, want available_commands_update", update["sessionUpdate"])
	}
	entries := update["availableCommands"].([]any)
	got := make(map[string]map[string]any, len(entries))
	for _, entry := range entries {
		e := entry.(map[string]any)
		got[e["name"].(string)] = e
	}
	for _, want := range []string{"compact", "new"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing slash command %q in %#v", want, got)
		}
	}
	if _, ok := got["exit"]; ok {
		t.Fatalf("unexpected exit command advertised")
	}
	if _, ok := got["model"]; ok {
		t.Fatalf("model should be exposed as a config option, not a slash command")
	}
	if _, ok := got["reasoning"]; ok {
		t.Fatalf("reasoning should be exposed as a config option, not a slash command")
	}
}

func TestSessionNewIncludesConfigOptions(t *testing.T) {
	model := registerSlashCommandTestModel(t)
	conv := &fakeConversation{cwd: "/repo", model: model, reasoningLevel: llm.ReasoningLevelMedium}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/repo"}}`,
	}, "\n") + "\n"

	out := runServer(t, conv, input)
	messages := parseMessages(t, out)
	result := messages[1]["result"].(map[string]any)
	options := result["configOptions"].([]any)
	got := configOptionsByID(options)

	modelOption := got["model"]
	if modelOption["category"] != "model" || modelOption["type"] != "select" || modelOption["currentValue"] != model.String() {
		t.Fatalf("model option = %#v", modelOption)
	}
	reasoningOption := got["reasoning"]
	if reasoningOption["category"] != "thought_level" || reasoningOption["type"] != "select" || reasoningOption["currentValue"] != "medium" {
		t.Fatalf("reasoning option = %#v", reasoningOption)
	}
}

func TestSlashCommandsExecute(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		conv   *fakeConversation
		check  func(*testing.T, *fakeConversation)
	}{
		{
			name:   "new",
			prompt: "/new",
			conv:   &fakeConversation{cwd: "/repo"},
			check: func(t *testing.T, conv *fakeConversation) {
				t.Helper()
				if !conv.newCalled {
					t.Fatalf("NewConversation was not called")
				}
			},
		},
		{
			name:   "compact",
			prompt: "/compact",
			conv:   &fakeConversation{cwd: "/repo"},
			check: func(t *testing.T, conv *fakeConversation) {
				t.Helper()
				if !conv.compactCalled {
					t.Fatalf("CompactConversation was not called")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := runServer(t, tt.conv, strings.Join([]string{
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
				`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/repo"}}`,
				`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"ronin-session","prompt":[{"type":"text","text":` + strconv.Quote(tt.prompt) + `}]}}`,
			}, "\n")+"\n")

			messages := parseMessages(t, out)
			last := messages[len(messages)-1]
			result := last["result"].(map[string]any)
			if result["stopReason"] != "end_turn" {
				t.Fatalf("stopReason = %v, want end_turn", result["stopReason"])
			}
			tt.check(t, tt.conv)
		})
	}
}

func TestSetConfigOption(t *testing.T) {
	model := registerSlashCommandTestModel(t)
	tests := []struct {
		name   string
		params string
		conv   *fakeConversation
		check  func(*testing.T, *fakeConversation, map[string]any)
	}{
		{
			name:   "model",
			params: `{"sessionId":"ronin-session","configId":"model","value":"` + model.String() + `"}`,
			conv:   &fakeConversation{cwd: "/repo"},
			check: func(t *testing.T, conv *fakeConversation, result map[string]any) {
				t.Helper()
				if conv.switchedModel != model {
					t.Fatalf("switched model = %v, want %v", conv.switchedModel, model)
				}
				got := configOptionsByID(result["configOptions"].([]any))["model"]
				if got["currentValue"] != model.String() {
					t.Fatalf("current model = %v, want %s", got["currentValue"], model.String())
				}
			},
		},
		{
			name:   "reasoning",
			params: `{"sessionId":"ronin-session","configId":"reasoning","value":"high"}`,
			conv:   &fakeConversation{cwd: "/repo"},
			check: func(t *testing.T, conv *fakeConversation, result map[string]any) {
				t.Helper()
				if conv.switchedReasoning != llm.ReasoningLevelHigh {
					t.Fatalf("switched reasoning = %v, want high", conv.switchedReasoning)
				}
				got := configOptionsByID(result["configOptions"].([]any))["reasoning"]
				if got["currentValue"] != "high" {
					t.Fatalf("current reasoning = %v, want high", got["currentValue"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := runServer(t, tt.conv, strings.Join([]string{
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
				`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/repo"}}`,
				`{"jsonrpc":"2.0","id":3,"method":"session/set_config_option","params":` + tt.params + `}`,
			}, "\n")+"\n")

			messages := parseMessages(t, out)
			last := messages[len(messages)-1]
			if last["error"] != nil {
				t.Fatalf("session/set_config_option returned error: %v", last["error"])
			}
			tt.check(t, tt.conv, last["result"].(map[string]any))
		})
	}
}

func TestUnknownSlashCommandDoesNotCallPrompt(t *testing.T) {
	conv := &fakeConversation{cwd: "/repo"}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/repo"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"ronin-session","prompt":[{"type":"text","text":"/does-not-exist"}]}}`,
	}, "\n") + "\n"

	out := runServer(t, conv, input)
	messages := parseMessages(t, out)
	last := messages[len(messages)-1]
	errObj := last["error"].(map[string]any)
	if errObj["code"] != float64(errInvalidParams) {
		t.Fatalf("error code = %v, want %d", errObj["code"], errInvalidParams)
	}
	if conv.prompt != "" {
		t.Fatalf("Prompt called unexpectedly: %q", conv.prompt)
	}
}

func TestNormalPromptPreservesWhitespace(t *testing.T) {
	conv := &fakeConversation{cwd: "/repo"}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/repo"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"ronin-session","prompt":[{"type":"text","text":" hi "}]}}`,
	}, "\n") + "\n"

	out := runServer(t, conv, input)
	messages := parseMessages(t, out)
	last := messages[len(messages)-1]
	result := last["result"].(map[string]any)
	if result["stopReason"] != "end_turn" {
		t.Fatalf("stopReason = %v, want end_turn", result["stopReason"])
	}
	if conv.prompt != " hi " {
		t.Fatalf("prompt = %q, want %q", conv.prompt, " hi ")
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
	if len(messages) != 8 {
		t.Fatalf("got %d messages, want 8: %s", len(messages), out)
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

func TestSessionNewSupportsMultipleSessions(t *testing.T) {
	convs := []*fakeConversation{
		{cwd: "/repo", id: "sess_a"},
		{cwd: "/repo", id: "sess_b"},
	}
	var next int
	newConversation := func() (Conversation, error) {
		conv := convs[next]
		next++
		return conv, nil
	}

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/repo"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/new","params":{"cwd":"/repo"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":"sess_a","prompt":[{"type":"text","text":"to a"}]}}`,
		`{"jsonrpc":"2.0","id":5,"method":"session/prompt","params":{"sessionId":"sess_b","prompt":[{"type":"text","text":"to b"}]}}`,
	}, "\n") + "\n"

	out := runServerWithFactory(t, newConversation, "/repo", input)
	messages := parseMessages(t, out)

	newResults := map[string]bool{}
	for _, msg := range messages {
		result, ok := msg["result"].(map[string]any)
		if !ok {
			continue
		}
		if id, ok := result["sessionId"].(string); ok {
			newResults[id] = true
		}
	}
	if !newResults["sess_a"] || !newResults["sess_b"] {
		t.Fatalf("expected both sessions created, got %v: %s", newResults, out)
	}
	if convs[0].prompt != "to a" {
		t.Fatalf("session a prompt = %q, want %q", convs[0].prompt, "to a")
	}
	if convs[1].prompt != "to b" {
		t.Fatalf("session b prompt = %q, want %q", convs[1].prompt, "to b")
	}
}

func TestInitializeAdvertisesLoadSessionWhenSupported(t *testing.T) {
	tests := []struct {
		name            string
		load            func(string) (Conversation, bool, error)
		wantLoadSession bool
	}{
		{name: "without load support", load: nil, wantLoadSession: false},
		{
			name:            "with load support",
			load:            func(string) (Conversation, bool, error) { return nil, false, nil },
			wantLoadSession: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := runServerFull(t, Config{
				NewConversation:  func() (Conversation, error) { return &fakeConversation{cwd: "/repo", id: "sess_a"}, nil },
				LoadConversation: tt.load,
				CWD:              "/repo",
			}, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`+"\n")

			messages := parseMessages(t, out)
			caps := messages[0]["result"].(map[string]any)["agentCapabilities"].(map[string]any)
			if got := caps["loadSession"]; got != tt.wantLoadSession {
				t.Fatalf("loadSession = %v, want %v", got, tt.wantLoadSession)
			}
		})
	}
}

func TestInitializeAdvertisesResumeCapability(t *testing.T) {
	tests := []struct {
		name       string
		load       func(string) (Conversation, bool, error)
		wantResume bool
	}{
		{name: "without load support", load: nil, wantResume: false},
		{
			name:       "with load support",
			load:       func(string) (Conversation, bool, error) { return nil, false, nil },
			wantResume: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := runServerFull(t, Config{
				NewConversation:  func() (Conversation, error) { return &fakeConversation{cwd: "/repo", id: "sess_a"}, nil },
				LoadConversation: tt.load,
				CWD:              "/repo",
			}, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`+"\n")

			messages := parseMessages(t, out)
			caps := messages[0]["result"].(map[string]any)["agentCapabilities"].(map[string]any)
			sessionCaps := caps["sessionCapabilities"].(map[string]any)
			_, gotResume := sessionCaps["resume"]
			if gotResume != tt.wantResume {
				t.Fatalf("resume capability = %v, want %v (caps: %#v)", gotResume, tt.wantResume, sessionCaps)
			}
		})
	}
}

func TestSessionResumeDoesNotReplayHistory(t *testing.T) {
	resumed := &fakeConversation{
		cwd: "/repo",
		id:  "sess_old",
		messages: []llm.Message{
			llm.UserMessage{Text: "earlier question"},
			llm.AssistantMessage{Blocks: []llm.AssistantBlock{llm.TextBlock{Text: "earlier answer"}}},
		},
	}
	cfg := Config{
		NewConversation:  func() (Conversation, error) { return &fakeConversation{cwd: "/repo", id: "sess_new"}, nil },
		LoadConversation: func(string) (Conversation, bool, error) { return resumed, true, nil },
		CWD:              "/repo",
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/resume","params":{"sessionId":"sess_old","cwd":"/repo"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"sess_old","prompt":[{"type":"text","text":"next"}]}}`,
	}, "\n") + "\n"

	out := runServerFull(t, cfg, input)
	messages := parseMessages(t, out)

	var resumeResult map[string]any
	for _, msg := range messages {
		if id, ok := msg["id"].(float64); ok && id == 2 {
			if _, hasErr := msg["error"]; hasErr {
				t.Fatalf("session/resume returned error: %v", msg["error"])
			}
			resumeResult, _ = msg["result"].(map[string]any)
		}
		if msg["method"] == "session/update" {
			update := msg["params"].(map[string]any)["update"].(map[string]any)
			switch update["sessionUpdate"] {
			case "user_message_chunk", "agent_message_chunk":
				t.Fatalf("resume replayed history update %q", update["sessionUpdate"])
			}
		}
	}
	if resumeResult == nil {
		t.Fatalf("session/resume result was not returned: %s", out)
	}
	if got := resumeResult["configOptions"].([]any); len(got) == 0 {
		t.Fatalf("session/resume configOptions missing: %#v", resumeResult)
	}
	if _, ok := resumeResult["sessionId"]; ok {
		t.Fatalf("session/resume returned sessionId unexpectedly: %#v", resumeResult)
	}
	if got := configOptionsByID(resumeResult["configOptions"].([]any)); got["model"]["id"] != "model" || got["reasoning"]["id"] != "reasoning" {
		t.Fatalf("session/resume configOptions missing entries: %#v", got)
	}
	if resumed.prompt != "next" {
		t.Fatalf("resumed session prompt = %q, want %q", resumed.prompt, "next")
	}
}

func TestSessionCloseFreesSessionAndCancelsTurn(t *testing.T) {
	conv := &fakeConversation{cwd: "/repo", id: "sess_a", block: make(chan struct{})}
	cfg := Config{
		NewConversation: func() (Conversation, error) { return conv, nil },
		CWD:             "/repo",
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/repo"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"sess_a","prompt":[{"type":"text","text":"hi"}]}}`,
		`{"jsonrpc":"2.0","id":4,"method":"session/close","params":{"sessionId":"sess_a"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"session/prompt","params":{"sessionId":"sess_a","prompt":[{"type":"text","text":"again"}]}}`,
	}, "\n") + "\n"

	out := runServerFull(t, cfg, input)
	messages := parseMessages(t, out)

	results := map[float64]map[string]any{}
	errorsByID := map[float64]map[string]any{}
	for _, msg := range messages {
		id, ok := msg["id"].(float64)
		if !ok {
			continue
		}
		if result, ok := msg["result"].(map[string]any); ok {
			results[id] = result
		}
		if errObj, ok := msg["error"].(map[string]any); ok {
			errorsByID[id] = errObj
		}
	}

	if promptResult, ok := results[3]; !ok || promptResult["stopReason"] != "cancelled" {
		t.Fatalf("prompt stopReason = %v, want cancelled; out %s", results[3], out)
	}
	if closeResult, ok := results[4]; !ok || len(closeResult) != 0 {
		t.Fatalf("session/close result = %v, want empty object", results[4])
	}
	if _, ok := errorsByID[5]; !ok {
		t.Fatalf("expected prompt to closed session to error; out %s", out)
	}
}

func TestSessionCloseUnknownSessionErrors(t *testing.T) {
	cfg := Config{
		NewConversation: func() (Conversation, error) { return &fakeConversation{cwd: "/repo", id: "sess_a"}, nil },
		CWD:             "/repo",
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/close","params":{"sessionId":"sess_missing"}}`,
	}, "\n") + "\n"

	out := runServerFull(t, cfg, input)
	messages := parseMessages(t, out)
	last := messages[len(messages)-1]
	errObj := last["error"].(map[string]any)
	if errObj["code"] != float64(errInvalidParams) {
		t.Fatalf("error code = %v, want %d", errObj["code"], errInvalidParams)
	}
}

func TestSessionDeleteRemovesPersistedSession(t *testing.T) {
	var deleted []string
	cfg := Config{
		NewConversation: func() (Conversation, error) { return &fakeConversation{cwd: "/repo", id: "sess_a"}, nil },
		DeleteSession:   func(id string) error { deleted = append(deleted, id); return nil },
		CWD:             "/repo",
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/delete","params":{"sessionId":"sess_old"}}`,
	}, "\n") + "\n"

	out := runServerFull(t, cfg, input)
	messages := parseMessages(t, out)
	last := messages[len(messages)-1]
	if last["error"] != nil {
		t.Fatalf("session/delete returned error: %v", last["error"])
	}
	result, ok := last["result"].(map[string]any)
	if !ok || len(result) != 0 {
		t.Fatalf("session/delete result = %v, want empty object", last["result"])
	}
	if len(deleted) != 1 || deleted[0] != "sess_old" {
		t.Fatalf("deleted = %v, want [sess_old]", deleted)
	}
}

func TestSessionDeleteUnsupportedReturnsMethodNotFound(t *testing.T) {
	cfg := Config{
		NewConversation: func() (Conversation, error) { return &fakeConversation{cwd: "/repo", id: "sess_a"}, nil },
		CWD:             "/repo",
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/delete","params":{"sessionId":"sess_old"}}`,
	}, "\n") + "\n"

	out := runServerFull(t, cfg, input)
	messages := parseMessages(t, out)
	last := messages[len(messages)-1]
	errObj := last["error"].(map[string]any)
	if errObj["code"] != float64(errMethodNotFound) {
		t.Fatalf("error code = %v, want %d", errObj["code"], errMethodNotFound)
	}
}

func TestInitializeAdvertisesCloseAndDeleteCapabilities(t *testing.T) {
	cfg := Config{
		NewConversation: func() (Conversation, error) { return &fakeConversation{cwd: "/repo", id: "sess_a"}, nil },
		DeleteSession:   func(string) error { return nil },
		CWD:             "/repo",
	}
	out := runServerFull(t, cfg, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`+"\n")
	messages := parseMessages(t, out)
	caps := messages[0]["result"].(map[string]any)["agentCapabilities"].(map[string]any)
	sessionCaps := caps["sessionCapabilities"].(map[string]any)
	if _, ok := sessionCaps["close"]; !ok {
		t.Fatalf("close capability missing: %#v", sessionCaps)
	}
	if _, ok := sessionCaps["delete"]; !ok {
		t.Fatalf("delete capability missing: %#v", sessionCaps)
	}
}

func TestPromptEmitsSessionInfoUpdate(t *testing.T) {
	updated := time.Date(2025, 10, 29, 14, 22, 15, 0, time.UTC)
	conv := &fakeConversation{cwd: "/repo", id: "sess_a", title: "Implement auth", updatedAt: updated}
	cfg := Config{
		NewConversation: func() (Conversation, error) { return conv, nil },
		CWD:             "/repo",
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/repo"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"sess_a","prompt":[{"type":"text","text":"hi"}]}}`,
	}, "\n") + "\n"

	out := runServerFull(t, cfg, input)
	messages := parseMessages(t, out)

	var info map[string]any
	for _, msg := range messages {
		if msg["method"] != "session/update" {
			continue
		}
		update := msg["params"].(map[string]any)["update"].(map[string]any)
		if update["sessionUpdate"] == "session_info_update" {
			info = update
		}
	}
	if info == nil {
		t.Fatalf("no session_info_update emitted: %s", out)
	}
	if info["title"] != "Implement auth" {
		t.Fatalf("title = %v, want %q", info["title"], "Implement auth")
	}
	if info["updatedAt"] != "2025-10-29T14:22:15Z" {
		t.Fatalf("updatedAt = %v, want RFC3339", info["updatedAt"])
	}
}

func TestSessionListReturnsSessions(t *testing.T) {
	updated := time.Date(2025, 10, 29, 14, 22, 15, 0, time.UTC)
	cfg := Config{
		NewConversation: func() (Conversation, error) { return &fakeConversation{cwd: "/repo", id: "sess_a"}, nil },
		ListSessions: func(cwd string) ([]SessionSummary, error) {
			return []SessionSummary{
				{SessionID: "sess_a", CWD: "/repo", Title: "First", UpdatedAt: updated},
				{SessionID: "sess_b", CWD: "/repo"},
			}, nil
		},
		CWD: "/repo",
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/list","params":{}}`,
	}, "\n") + "\n"

	out := runServerFull(t, cfg, input)
	messages := parseMessages(t, out)

	caps := messages[0]["result"].(map[string]any)["agentCapabilities"].(map[string]any)
	if _, ok := caps["sessionCapabilities"].(map[string]any)["list"]; !ok {
		t.Fatalf("list capability missing: %#v", caps["sessionCapabilities"])
	}

	result := messages[1]["result"].(map[string]any)
	sessions := result["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2: %s", len(sessions), out)
	}
	first := sessions[0].(map[string]any)
	if first["sessionId"] != "sess_a" || first["cwd"] != "/repo" || first["title"] != "First" {
		t.Fatalf("first session = %#v", first)
	}
	if first["updatedAt"] != "2025-10-29T14:22:15Z" {
		t.Fatalf("updatedAt = %v, want RFC3339", first["updatedAt"])
	}
	second := sessions[1].(map[string]any)
	if _, ok := second["updatedAt"]; ok {
		t.Fatalf("second session should omit updatedAt: %#v", second)
	}
	if _, ok := result["nextCursor"]; ok {
		t.Fatalf("unexpected nextCursor: %#v", result["nextCursor"])
	}
}

func TestSessionListRejectsRelativeCWD(t *testing.T) {
	cfg := Config{
		NewConversation: func() (Conversation, error) { return &fakeConversation{cwd: "/repo", id: "sess_a"}, nil },
		ListSessions:    func(string) ([]SessionSummary, error) { return nil, nil },
		CWD:             "/repo",
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/list","params":{"cwd":"relative/path"}}`,
	}, "\n") + "\n"

	out := runServerFull(t, cfg, input)
	messages := parseMessages(t, out)
	last := messages[len(messages)-1]
	errObj := last["error"].(map[string]any)
	if errObj["code"] != float64(errInvalidParams) {
		t.Fatalf("error code = %v, want %d", errObj["code"], errInvalidParams)
	}
}

func TestSessionListUnsupportedReturnsMethodNotFound(t *testing.T) {
	cfg := Config{
		NewConversation: func() (Conversation, error) { return &fakeConversation{cwd: "/repo", id: "sess_a"}, nil },
		CWD:             "/repo",
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/list","params":{}}`,
	}, "\n") + "\n"

	out := runServerFull(t, cfg, input)
	messages := parseMessages(t, out)
	last := messages[len(messages)-1]
	errObj := last["error"].(map[string]any)
	if errObj["code"] != float64(errMethodNotFound) {
		t.Fatalf("error code = %v, want %d", errObj["code"], errMethodNotFound)
	}
}

func TestSessionResumeUnsupportedReturnsMethodNotFound(t *testing.T) {
	cfg := Config{
		NewConversation: func() (Conversation, error) { return &fakeConversation{cwd: "/repo", id: "sess_a"}, nil },
		CWD:             "/repo",
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/resume","params":{"sessionId":"sess_old","cwd":"/repo"}}`,
	}, "\n") + "\n"

	out := runServerFull(t, cfg, input)
	messages := parseMessages(t, out)
	last := messages[len(messages)-1]
	errObj := last["error"].(map[string]any)
	if errObj["code"] != float64(errMethodNotFound) {
		t.Fatalf("error code = %v, want %d", errObj["code"], errMethodNotFound)
	}
}

func TestSessionLoadReplaysHistory(t *testing.T) {
	loaded := &fakeConversation{
		cwd: "/repo",
		id:  "sess_old",
		messages: []llm.Message{
			llm.UserMessage{Text: "first question"},
			llm.AssistantMessage{Blocks: []llm.AssistantBlock{
				llm.TextBlock{Text: "an answer"},
				llm.ToolCallBlock{ID: "call-1", Name: "shell", Arguments: json.RawMessage(`{"command":"ls"}`)},
			}},
			llm.ToolOutputMessage{ToolName: "shell", ToolCallID: "call-1", ToolOutput: "file.go"},
		},
	}
	cfg := Config{
		NewConversation:  func() (Conversation, error) { return &fakeConversation{cwd: "/repo", id: "sess_new"}, nil },
		LoadConversation: func(string) (Conversation, bool, error) { return loaded, true, nil },
		CWD:              "/repo",
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"sess_old","cwd":"/repo"}}`,
	}, "\n") + "\n"

	out := runServerFull(t, cfg, input)
	messages := parseMessages(t, out)

	var updates []string
	var loadResolved bool
	for _, msg := range messages {
		if msg["method"] == "session/update" {
			update := msg["params"].(map[string]any)["update"].(map[string]any)
			updates = append(updates, update["sessionUpdate"].(string))
			continue
		}
		if id, ok := msg["id"].(float64); ok && id == 2 {
			if _, hasErr := msg["error"]; hasErr {
				t.Fatalf("session/load returned error: %v", msg["error"])
			}
			loadResolved = true
			result, ok := msg["result"].(map[string]any)
			if !ok || len(result) == 0 {
				t.Fatalf("session/load result = %#v, want configOptions", msg["result"])
			}
			if _, ok := result["sessionId"]; ok {
				t.Fatalf("session/load returned sessionId unexpectedly: %#v", result)
			}
			got := configOptionsByID(result["configOptions"].([]any))
			if got["model"]["id"] != "model" || got["reasoning"]["id"] != "reasoning" {
				t.Fatalf("session/load configOptions missing entries: %#v", got)
			}
		}
	}
	if !loadResolved {
		t.Fatalf("session/load was not resolved: %s", out)
	}
	want := []string{"user_message_chunk", "agent_message_chunk", "tool_call", "tool_call_update", "available_commands_update"}
	if len(updates) != len(want) {
		t.Fatalf("updates = %v, want %v", updates, want)
	}
	for i, w := range want {
		if updates[i] != w {
			t.Fatalf("update[%d] = %q, want %q (all: %v)", i, updates[i], w, updates)
		}
	}
}

func TestSessionLoadUnsupportedReturnsMethodNotFound(t *testing.T) {
	cfg := Config{
		NewConversation: func() (Conversation, error) { return &fakeConversation{cwd: "/repo", id: "sess_a"}, nil },
		CWD:             "/repo",
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"sess_old","cwd":"/repo"}}`,
	}, "\n") + "\n"

	out := runServerFull(t, cfg, input)
	messages := parseMessages(t, out)
	last := messages[len(messages)-1]
	errObj := last["error"].(map[string]any)
	if errObj["code"] != float64(errMethodNotFound) {
		t.Fatalf("error code = %v, want %d", errObj["code"], errMethodNotFound)
	}
}

func runServer(t *testing.T, conv *fakeConversation, input string) string {
	t.Helper()
	if conv.id == "" {
		conv.id = "ronin-session"
	}
	reader, writer := io.Pipe()
	var out strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Config{
			NewConversation: func() (Conversation, error) { return conv, nil },
			CWD:             conv.cwd,
			Input:           reader,
			Output:          &out,
		})
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

func runServerWithFactory(t *testing.T, newConversation func() (Conversation, error), cwd, input string) string {
	t.Helper()
	return runServerFull(t, Config{NewConversation: newConversation, CWD: cwd}, input)
}

func runServerFull(t *testing.T, cfg Config, input string) string {
	t.Helper()
	reader, writer := io.Pipe()
	var out strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	cfg.Input = reader
	cfg.Output = &out

	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, cfg)
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

func configOptionsByID(options []any) map[string]map[string]any {
	byID := make(map[string]map[string]any, len(options))
	for _, option := range options {
		opt := option.(map[string]any)
		byID[opt["id"].(string)] = opt
	}
	return byID
}

type fakeConversation struct {
	cwd               string
	id                string
	title             string
	updatedAt         time.Time
	messages          []llm.Message
	model             llm.Model
	reasoningLevel    llm.ReasoningLevel
	events            []runtime.Event
	block             chan struct{}
	prompt            string
	newCalled         bool
	compactCalled     bool
	switchedModel     llm.Model
	switchedReasoning llm.ReasoningLevel
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

func (f *fakeConversation) CWD() string                 { return f.cwd }
func (f *fakeConversation) SessionID() string           { return f.id }
func (f *fakeConversation) SessionTitle() string        { return f.title }
func (f *fakeConversation) SessionUpdatedAt() time.Time { return f.updatedAt }
func (f *fakeConversation) Messages() []llm.Message     { return f.messages }
func (f *fakeConversation) Model() llm.Model {
	if f.model.Provider != "" || f.model.Name != "" {
		return f.model
	}
	return llm.Model{}
}
func (f *fakeConversation) ReasoningLevel() llm.ReasoningLevel {
	if f.reasoningLevel != "" {
		return f.reasoningLevel
	}
	return llm.ReasoningLevelOff
}
func (f *fakeConversation) ContextUsage() llm.Usage                            { return llm.Usage{} }
func (f *fakeConversation) ToolCallTitle(name string, arguments []byte) string { return name }

func (f *fakeConversation) NewConversation() error {
	f.newCalled = true
	return nil
}
func (f *fakeConversation) CompactConversation(context.Context) error {
	f.compactCalled = true
	return nil
}
func (f *fakeConversation) SwitchModel(model llm.Model) error {
	f.switchedModel = model
	f.model = model
	return nil
}
func (f *fakeConversation) SwitchReasoningLevel(level llm.ReasoningLevel) error {
	f.switchedReasoning = level
	f.reasoningLevel = level
	return nil
}

func registerSlashCommandTestModel(t *testing.T) llm.Model {
	t.Helper()
	slashCommandTestModelOnce.Do(func() {
		slashCommandTestModelErr = llm.RegisterModel(slashCommandTestModel, func(llm.ReasoningLevel) (llm.ModelClient, error) {
			return fakeModelClient{}, nil
		})
	})
	if slashCommandTestModelErr != nil {
		t.Fatalf("RegisterModel() error = %v", slashCommandTestModelErr)
	}
	return slashCommandTestModel
}

var (
	slashCommandTestModel     = llm.Model{Provider: "acp-test", Name: "slash-command"}
	slashCommandTestModelOnce sync.Once
	slashCommandTestModelErr  error
)

type fakeModelClient struct{}

func (fakeModelClient) Model() llm.Model                           { return slashCommandTestModel }
func (fakeModelClient) ReasoningLevel() llm.ReasoningLevel         { return llm.ReasoningLevelOff }
func (fakeModelClient) SetReasoningLevel(llm.ReasoningLevel) error { return nil }
func (fakeModelClient) PredictNext(context.Context, llm.PredictNextRequest) (<-chan llm.PredictionEvent, <-chan error) {
	events := make(chan llm.PredictionEvent)
	errs := make(chan error, 1)
	close(events)
	close(errs)
	return events, errs
}
func (fakeModelClient) PredictNextStructured(context.Context, llm.PredictNextStructuredRequest) (json.RawMessage, error) {
	return nil, nil
}

type fakeTool struct{ name string }

func (f fakeTool) Name() string                                       { return f.name }
func (f fakeTool) Description() string                                { return "" }
func (f fakeTool) Parameters() *jsonschema.Schema                     { return nil }
func (f fakeTool) Call(context.Context, json.RawMessage) (any, error) { return nil, nil }
