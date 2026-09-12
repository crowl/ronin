package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/workflow"
)

func TestRunnerRunUsesDefaultModelAndTools(t *testing.T) {
	model, client := registerFakeModel(t, "default", "done")
	mcp := fakeMCP{tools: []runtime.Tool{fakeTool{name: "mcp__search"}}, instructions: []runtime.MCPInstruction{{Server: "docs", Content: "Prefer docs."}}}
	runner := New(Config{WorkingDir: t.TempDir(), Model: model, ReasoningLevel: llm.ReasoningLevelLow, MaxTurns: 3, MCP: mcp})

	var progress []workflow.AgentEvent
	result, err := runner.Run(t.Context(), workflow.AgentRequest{Prompt: "hello", Progress: func(event workflow.AgentEvent) {
		progress = append(progress, event)
	}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != "done" || result.Output != nil {
		t.Fatalf("Run() = %#v", result)
	}
	if client.level != llm.ReasoningLevelLow {
		t.Fatalf("reasoning level = %q, want low", client.level)
	}

	req := client.request(t)
	want := []string{"code_map", "code_find", "read_file", "edit_file", "write_file", "shell", "mcp__search", "conversation_history"}
	if got := toolNames(req.Tools); !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	if !strings.Contains(req.SystemPrompt, "Prefer docs.") {
		t.Fatalf("system prompt missing MCP instructions:\n%s", req.SystemPrompt)
	}
	if strings.Contains(req.SystemPrompt, "read-only") || strings.Contains(req.SystemPrompt, "Workflow agent instructions") {
		t.Fatalf("system prompt has unexpected suffixes:\n%s", req.SystemPrompt)
	}
	if !reflect.DeepEqual(progress, []workflow.AgentEvent{workflow.AgentTextDelta{Text: "done"}}) {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestRunnerRunAppliesRequestOverrides(t *testing.T) {
	defaultModel, defaultClient := registerFakeModel(t, "default", "default")
	overrideModel, overrideClient := registerFakeModel(t, "override", "override")
	resolve := func(requested llm.Model) (llm.Model, error) {
		if requested.Name == "override" {
			return overrideModel, nil
		}
		return llm.Model{}, fmt.Errorf("unknown %s", requested)
	}

	t.Run("resolves requested model and reasoning level", func(t *testing.T) {
		runner := New(Config{WorkingDir: t.TempDir(), Model: defaultModel, ReasoningLevel: llm.ReasoningLevelOff, MaxTurns: 3, ResolveModel: resolve})
		result, err := runner.Run(t.Context(), workflow.AgentRequest{
			Prompt:         "hello",
			Model:          llm.Model{Provider: overrideModel.Provider, Name: "override"},
			ReasoningLevel: llm.ReasoningLevelHigh,
			System:         "Focus on tests.",
		})
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if result.Text != "override" {
			t.Fatalf("Run() text = %q, want override", result.Text)
		}
		if overrideClient.level != llm.ReasoningLevelHigh {
			t.Fatalf("reasoning level = %q, want high", overrideClient.level)
		}
		if defaultClient.calls.Load() != 0 {
			t.Fatal("default model was used despite override")
		}
		if got := overrideClient.request(t).SystemPrompt; !strings.HasSuffix(got, "Workflow agent instructions:\nFocus on tests.") {
			t.Fatalf("system prompt missing workflow instructions:\n%s", got)
		}
	})

	t.Run("fails when model resolution is not configured", func(t *testing.T) {
		runner := New(Config{WorkingDir: t.TempDir(), Model: defaultModel, ReasoningLevel: llm.ReasoningLevelOff})
		_, err := runner.Run(t.Context(), workflow.AgentRequest{Prompt: "hello", Model: overrideModel})
		if err == nil || !strings.Contains(err.Error(), "model resolution is not configured") {
			t.Fatalf("Run() error = %v", err)
		}
	})

	t.Run("propagates resolution failures", func(t *testing.T) {
		runner := New(Config{WorkingDir: t.TempDir(), Model: defaultModel, ReasoningLevel: llm.ReasoningLevelOff, ResolveModel: resolve})
		_, err := runner.Run(t.Context(), workflow.AgentRequest{Prompt: "hello", Model: llm.Model{Provider: "fake", Name: "missing"}})
		if err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("Run() error = %v", err)
		}
	})

	t.Run("rejects unsupported reasoning level", func(t *testing.T) {
		runner := New(Config{WorkingDir: t.TempDir(), Model: defaultModel, ReasoningLevel: llm.ReasoningLevelOff})
		_, err := runner.Run(t.Context(), workflow.AgentRequest{Prompt: "hello", ReasoningLevel: llm.ReasoningLevel("extreme")})
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("Run() error = %v", err)
		}
	})
}

func TestRunnerRunRestrictsTools(t *testing.T) {
	model, client := registerFakeModel(t, "restricted", "ok")
	mcp := fakeMCP{tools: []runtime.Tool{fakeTool{name: "mcp__search"}}, instructions: []runtime.MCPInstruction{{Server: "docs", Content: "Prefer docs."}}}

	t.Run("read-only agents lose mutation, shell, and MCP tools", func(t *testing.T) {
		runner := New(Config{WorkingDir: t.TempDir(), Model: model, ReasoningLevel: llm.ReasoningLevelOff, MaxTurns: 3, MCP: mcp})
		if _, err := runner.Run(t.Context(), workflow.AgentRequest{Prompt: "hello", ReadOnly: true}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		req := client.request(t)
		if got := toolNames(req.Tools); !reflect.DeepEqual(got, []string{"read_file", "code_map", "code_find", "conversation_history"}) {
			t.Fatalf("tools = %v", got)
		}
		if !strings.Contains(req.SystemPrompt, "This agent is read-only.") || strings.Contains(req.SystemPrompt, "Prefer docs.") {
			t.Fatalf("system prompt = %q", req.SystemPrompt)
		}
	})

	t.Run("managed workspaces use the workspace root without shell or MCP", func(t *testing.T) {
		workspace := t.TempDir()
		runner := New(Config{WorkingDir: t.TempDir(), Model: model, ReasoningLevel: llm.ReasoningLevelOff, MaxTurns: 3, MCP: mcp})
		if _, err := runner.Run(t.Context(), workflow.AgentRequest{Prompt: "hello", Workspace: workspace}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		req := client.request(t)
		if got := toolNames(req.Tools); !reflect.DeepEqual(got, []string{"code_map", "code_find", "read_file", "edit_file", "write_file", "conversation_history"}) {
			t.Fatalf("tools = %v", got)
		}
		if !strings.Contains(req.SystemPrompt, workspace) || strings.Contains(req.SystemPrompt, "Prefer docs.") {
			t.Fatalf("system prompt = %q", req.SystemPrompt)
		}
	})
}

func TestRunnerRunStructuresOutput(t *testing.T) {
	schema := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"summary": {Type: "string"}}, Required: []string{"summary"}}

	t.Run("converts the report using the schema", func(t *testing.T) {
		model, client := registerFakeModel(t, "structured", "report")
		client.structured = json.RawMessage(`{"summary":"report"}`)
		runner := New(Config{WorkingDir: t.TempDir(), Model: model, ReasoningLevel: llm.ReasoningLevelOff, MaxTurns: 3})
		result, err := runner.Run(t.Context(), workflow.AgentRequest{Prompt: "hello", OutputSchema: schema})
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if result.Text != "report" || string(result.Output) != `{"summary":"report"}` {
			t.Fatalf("Run() = %#v", result)
		}
	})

	t.Run("rejects an unsupported schema before running the agent", func(t *testing.T) {
		model, client := registerFakeModel(t, "structured-invalid", "report")
		client.schemaErr = errors.New("uniqueItems is not permitted")
		runner := New(Config{WorkingDir: t.TempDir(), Model: model, ReasoningLevel: llm.ReasoningLevelOff, MaxTurns: 3})
		_, err := runner.Run(t.Context(), workflow.AgentRequest{Prompt: "hello", OutputSchema: schema})
		if err == nil || !strings.Contains(err.Error(), "uniqueItems") {
			t.Fatalf("Run() error = %v", err)
		}
		if client.calls.Load() != 0 {
			t.Fatal("agent ran despite invalid schema")
		}
	})
}

// registerFakeModel registers a model unique to the calling test whose client
// replies to every prompt with reply.
func registerFakeModel(t *testing.T, name, reply string) (llm.Model, *fakeClient) {
	t.Helper()
	model := llm.Model{
		Provider:           "agentrun-test",
		Name:               t.Name() + "/" + name,
		ContextWindow:      100_000,
		SupportedReasoning: llm.NewReasoningSet(llm.ReasoningLevelOff, llm.ReasoningLevelLow, llm.ReasoningLevelHigh),
	}
	client := &fakeClient{model: model, reply: reply}
	if err := llm.RegisterModel(model, func(level llm.ReasoningLevel) (llm.ModelClient, error) {
		client.level = level
		return client, nil
	}); err != nil {
		t.Fatalf("RegisterModel(%s) error = %v", model, err)
	}
	return model, client
}

type fakeClient struct {
	model      llm.Model
	level      llm.ReasoningLevel
	reply      string
	structured json.RawMessage
	schemaErr  error

	mu       sync.Mutex
	calls    atomic.Int32
	requests []llm.PredictNextRequest
}

func (f *fakeClient) Model() llm.Model                   { return f.model }
func (f *fakeClient) ReasoningLevel() llm.ReasoningLevel { return f.level }
func (f *fakeClient) SetReasoningLevel(level llm.ReasoningLevel) error {
	f.level = level
	return nil
}

func (f *fakeClient) PredictNext(_ context.Context, req llm.PredictNextRequest) (<-chan llm.PredictionEvent, <-chan error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	f.calls.Add(1)

	events := make(chan llm.PredictionEvent, 4)
	events <- llm.PredictionStarted{}
	events <- llm.TextDelta{Text: f.reply}
	events <- llm.BlockEnded{Block: llm.TextBlock{Text: f.reply}}
	events <- llm.PredictionFinished{StopReason: llm.StopReasonEndTurn}
	close(events)
	errs := make(chan error)
	close(errs)
	return events, errs
}

func (f *fakeClient) PredictNextStructured(context.Context, llm.PredictNextStructuredRequest) (*llm.StructuredResult, error) {
	return &llm.StructuredResult{JSON: f.structured}, nil
}

func (f *fakeClient) ValidateStructuredOutputSchema(*jsonschema.Schema) error { return f.schemaErr }

// request returns the most recent prediction request.
func (f *fakeClient) request(t *testing.T) llm.PredictNextRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("model was not called")
	}
	return f.requests[len(f.requests)-1]
}

type fakeMCP struct {
	tools        []runtime.Tool
	instructions []runtime.MCPInstruction
}

func (f fakeMCP) Tools() []runtime.Tool                  { return f.tools }
func (f fakeMCP) Instructions() []runtime.MCPInstruction { return f.instructions }

type fakeTool struct{ name string }

func (t fakeTool) Name() string                                     { return t.name }
func (fakeTool) Description() string                                { return "fake" }
func (fakeTool) Parameters() *jsonschema.Schema                     { return nil }
func (fakeTool) Call(context.Context, json.RawMessage) (any, error) { return nil, nil }

func toolNames(tools []llm.Tool) []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name()
	}
	return names
}
