package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"strings"
	"sync"
	"testing"
)

func TestValidateWorkflowAgentOutputSchema(t *testing.T) {
	client := &fakeStructuredClient{schemaValidationErr: errors.New("uniqueItems is not permitted")}
	if err := validateWorkflowAgentOutputSchema(client, &jsonschema.Schema{Type: "object"}); err == nil || !strings.Contains(err.Error(), "uniqueItems") {
		t.Fatalf("validateWorkflowAgentOutputSchema() error = %v", err)
	}
	if len(client.requests) != 0 {
		t.Fatalf("structured calls = %d, want 0", len(client.requests))
	}
}

func TestStructureWorkflowAgentOutput(t *testing.T) {
	schema, err := jsonschema.FromRaw([]byte(`{
		"type":"object",
		"additionalProperties":false,
		"required":["id","commit_message"],
		"properties":{
			"id":{"type":"string","pattern":"^[a-z]+-[a-z-]+$"},
			"commit_message":{"type":"string","pattern":"^fix: [a-z].+[^.]$"}
		}
	}`))
	if err != nil {
		t.Fatalf("FromRaw() error = %v", err)
	}

	t.Run("retries only conversion and returns corrected output", func(t *testing.T) {
		client := &fakeStructuredClient{outputs: []json.RawMessage{
			json.RawMessage(`{"id":"string","commit_message":"string"}`),
			json.RawMessage(`{"id":"planner-output","commit_message":"fix: validate planner output"}`),
		}}

		got, err := structureWorkflowAgentOutput(t.Context(), client, "authoritative report", schema)
		if err != nil {
			t.Fatalf("structureWorkflowAgentOutput() error = %v", err)
		}
		if string(got) != `{"id":"planner-output","commit_message":"fix: validate planner output"}` {
			t.Fatalf("output = %s", got)
		}
		if len(client.requests) != 2 {
			t.Fatalf("structured calls = %d, want 2", len(client.requests))
		}
		if got := client.requests[0].Messages[0].(llm.UserMessage).Text; got != "authoritative report" {
			t.Fatalf("first prompt = %q", got)
		}
		correction := client.requests[1].Messages[0].(llm.UserMessage).Text
		for _, want := range []string{"authoritative report", `"id":"string"`, "$.id", "Correct only the JSON conversion"} {
			if !strings.Contains(correction, want) {
				t.Fatalf("correction prompt missing %q:\n%s", want, correction)
			}
		}
	})

	t.Run("bounds corrections and reports invalid output", func(t *testing.T) {
		client := &fakeStructuredClient{outputs: []json.RawMessage{
			json.RawMessage(`{"id":"string","commit_message":"string"}`),
			json.RawMessage(`{"id":"string","commit_message":"string"}`),
			json.RawMessage(`{"id":"string","commit_message":"string"}`),
		}}

		_, err := structureWorkflowAgentOutput(t.Context(), client, "expensive report", schema)
		if err == nil {
			t.Fatal("structureWorkflowAgentOutput() error = nil")
		}
		if len(client.requests) != 3 {
			t.Fatalf("structured calls = %d, want 3", len(client.requests))
		}
		for _, want := range []string{"after 2 correction attempts", "$.id", `"commit_message":"string"`, "original agent report (the agent was not rerun): expensive report"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error missing %q: %v", want, err)
			}
		}
	})
}

type fakeStructuredClient struct {
	mu                  sync.Mutex
	outputs             []json.RawMessage
	requests            []llm.PredictNextStructuredRequest
	schemaValidationErr error
}

func (f *fakeStructuredClient) Model() llm.Model {
	return llm.Model{Provider: "fake", Name: "structured"}
}
func (f *fakeStructuredClient) ReasoningLevel() llm.ReasoningLevel         { return llm.ReasoningLevelOff }
func (f *fakeStructuredClient) SetReasoningLevel(llm.ReasoningLevel) error { return nil }
func (f *fakeStructuredClient) PredictNext(context.Context, llm.PredictNextRequest) (<-chan llm.PredictionEvent, <-chan error) {
	events := make(chan llm.PredictionEvent)
	errs := make(chan error)
	close(events)
	close(errs)
	return events, errs
}
func (f *fakeStructuredClient) ValidateStructuredOutputSchema(*jsonschema.Schema) error {
	return f.schemaValidationErr
}
func (f *fakeStructuredClient) PredictNextStructured(_ context.Context, req llm.PredictNextStructuredRequest) (*llm.StructuredResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if len(f.outputs) == 0 {
		return nil, errors.New("no structured output configured")
	}
	output := f.outputs[0]
	f.outputs = f.outputs[1:]
	return &llm.StructuredResult{JSON: output}, nil
}
