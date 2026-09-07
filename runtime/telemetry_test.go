package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestExecutionTelemetry(t *testing.T) {
	for _, name := range []string{"shell", "missing"} {
		t.Run(name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			old := otel.GetTracerProvider()
			otel.SetTracerProvider(provider)
			t.Cleanup(func() { otel.SetTracerProvider(old); _ = provider.Shutdown(context.Background()) })
			args := json.RawMessage(`{"command":"false"}`)
			client := &fakeModelClient{model: llm.Model{Provider: "test-provider", Name: "test-model"}, eventBatches: [][]llm.PredictionEvent{
				{llm.BlockEnded{Block: llm.ToolCallBlock{ID: "call-123", Name: name, Arguments: args}}, llm.PredictionFinished{Usage: llm.Usage{InputTokens: 20, OutputTokens: 5}}},
				{llm.PredictionFinished{Usage: llm.Usage{InputTokens: 30, OutputTokens: 5}}},
			}}
			conv, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client, Tools: []runtime.Tool{fakeTool{name: "shell", err: errors.New("command failed")}}})
			if err != nil {
				t.Fatal(err)
			}
			events, errs := conv.Prompt(t.Context(), "test")
			_ = collectEvents(events)
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
			spans := exporter.GetSpans()
			var prompt, tool tracetest.SpanStub
			var cycles, requests int
			for _, s := range spans {
				if telemetryAttr(s.Attributes, "gen_ai.request.model").AsString() != "test-model" {
					t.Fatalf("missing model on %s", s.Name)
				}
				switch s.Name {
				case "ronin.prompt_turn":
					prompt = s
				case "ronin.cycle":
					cycles++
				case "ronin.request":
					requests++
				case "ronin.tool":
					tool = s
				}
			}
			if cycles != 2 || requests != 2 {
				t.Fatalf("cycles=%d requests=%d", cycles, requests)
			}
			if telemetryAttr(prompt.Attributes, "ronin.request.count").AsInt64() != 2 || telemetryAttr(prompt.Attributes, "ronin.tool.count").AsInt64() != 1 {
				t.Fatalf("bad turn counts: %v", prompt.Attributes)
			}
			if telemetryAttr(prompt.Attributes, "gen_ai.usage.input_tokens").AsInt64() != 50 {
				t.Fatal("usage not aggregated")
			}
			if telemetryAttr(tool.Attributes, "gen_ai.tool.name").AsString() != name || telemetryAttr(tool.Attributes, "gen_ai.tool.call.id").AsString() != "call-123" || telemetryAttr(tool.Attributes, "ronin.tool.arguments.size").AsInt64() != int64(len(args)) {
				t.Fatalf("tool identity: %v", tool.Attributes)
			}
			for _, a := range tool.Attributes {
				if a.Key == "gen_ai.tool.call.arguments" {
					t.Fatal("argument contents exported")
				}
			}
			if telemetryAttr(tool.Attributes, "ronin.outcome").AsString() != "error" {
				t.Fatal("recoverable tool failure marked successful")
			}
			if name == "missing" && !telemetryAttr(tool.Attributes, "ronin.tool.rejected").AsBool() {
				t.Fatal("missing rejected flag")
			}
			if tool.SpanContext.TraceID() != prompt.SpanContext.TraceID() {
				t.Fatal("tool outside prompt trace")
			}
		})
	}
}

func telemetryAttr(attrs []attribute.KeyValue, key string) attribute.Value {
	for _, a := range attrs {
		if string(a.Key) == key {
			return a.Value
		}
	}
	return attribute.Value{}
}
