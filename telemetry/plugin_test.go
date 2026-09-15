package telemetry_test

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/crowl/ronin/plugin"
	"github.com/crowl/ronin/telemetry"
)

func TestPluginRebuildsSpanTreeFromEvents(t *testing.T) {
	exporter, _ := providers(t)
	p := telemetry.NewPlugin()
	ctx := plugin.NewContext(context.Background(), plugin.NewHost(p))
	model := plugin.Model{Provider: "provider", Name: "model-a"}

	wfCtx, wf := plugin.Begin(ctx)
	p.Observe(ctx, plugin.WorkflowStarted{Operation: wf, Name: "flow.lua"})
	turnCtx, turn := plugin.Begin(wfCtx)
	p.Observe(ctx, plugin.PromptTurnStarted{Operation: turn, SessionID: "s", Model: model})
	cycleCtx, cycle := plugin.Begin(turnCtx)
	p.Observe(ctx, plugin.CycleStarted{Operation: cycle, Index: 1, Model: model})
	reqCtx, req := plugin.Begin(cycleCtx)
	p.Observe(ctx, plugin.ModelRequestStarted{Operation: req, Model: model, Purpose: "conversation"})
	_, attempt := plugin.Begin(reqCtx)
	p.Observe(ctx, plugin.HTTPAttemptStarted{Operation: attempt, Attempt: 1})
	p.Observe(ctx, plugin.HTTPAttemptEnded{Operation: attempt, Attempt: 1, StatusCode: 200})
	p.Observe(ctx, plugin.ModelRequestEnded{Operation: req, Model: model, Purpose: "conversation", StopReason: "tool_use", Usage: &plugin.Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 10, Cost: 0.5, CostAvailable: true}})
	call := plugin.ToolCall{ID: "call-1", Name: "shell", Arguments: []byte(`{"command":"ls"}`)}
	_, tool := plugin.Begin(cycleCtx)
	p.Observe(ctx, plugin.ToolCallStarted{Operation: tool, Call: call, Model: model})
	p.Observe(ctx, plugin.ToolCallEnded{Operation: tool, Call: call, Model: model, ResultSize: 42})
	_, denied := plugin.Begin(cycleCtx)
	p.Observe(ctx, plugin.ToolCallDenied{Operation: denied, Call: plugin.ToolCall{ID: "call-2", Name: "shell"}, Model: model, Err: &plugin.DeniedError{Plugin: "policy", Reason: "secret reason"}})
	p.Observe(ctx, plugin.CycleEnded{Operation: cycle})
	p.Observe(ctx, plugin.PromptTurnEnded{Operation: turn, Cycles: 1})
	p.Observe(ctx, plugin.WorkflowEnded{Operation: wf, Err: errors.New("secret failure")})

	byName := map[string][]tracetest.SpanStub{}
	for _, s := range exporter.GetSpans() {
		byName[s.Name] = append(byName[s.Name], s)
	}
	one := func(name string) tracetest.SpanStub {
		t.Helper()
		if len(byName[name]) != 1 {
			t.Fatalf("%s spans = %d", name, len(byName[name]))
		}
		return byName[name][0]
	}
	workflow, prompt, cycleSpan, request := one("ronin.workflow"), one("ronin.prompt_turn"), one("ronin.cycle"), one("ronin.request")
	attemptSpan := one("ronin.http_attempt")
	if prompt.Parent.SpanID() != workflow.SpanContext.SpanID() || cycleSpan.Parent.SpanID() != prompt.SpanContext.SpanID() || request.Parent.SpanID() != cycleSpan.SpanContext.SpanID() || attemptSpan.Parent.SpanID() != request.SpanContext.SpanID() {
		t.Fatal("span parentage does not follow operation IDs")
	}
	if attr(attemptSpan.Attributes, "http.response.status_code").AsInt64() != 200 || attr(request.Attributes, "gen_ai.response.finish_reason").AsString() != "tool_use" {
		t.Fatal("request details missing")
	}
	if attr(prompt.Attributes, "gen_ai.usage.input_tokens").AsInt64() != 100 || attr(prompt.Attributes, "ronin.tool.count").AsInt64() != 2 || attr(prompt.Attributes, "ronin.cycle.count").AsInt64() != 1 {
		t.Fatalf("prompt aggregates: %v", prompt.Attributes)
	}
	tools := byName["ronin.tool"]
	if len(tools) != 2 {
		t.Fatalf("tool spans = %d", len(tools))
	}
	for _, s := range tools {
		if s.Parent.SpanID() != cycleSpan.SpanContext.SpanID() {
			t.Fatal("tool outside cycle")
		}
		switch attr(s.Attributes, "gen_ai.tool.call.id").AsString() {
		case "call-1":
			if attr(s.Attributes, "ronin.tool.result.size").AsInt64() != 42 || attr(s.Attributes, "ronin.outcome").AsString() != "success" {
				t.Fatalf("executed tool: %v", s.Attributes)
			}
		case "call-2":
			if !attr(s.Attributes, "ronin.tool.denied").AsBool() || attr(s.Attributes, "ronin.outcome").AsString() != "error" {
				t.Fatalf("denied tool: %v", s.Attributes)
			}
		}
		if s.Status.Description == "secret reason" {
			t.Fatal("denial reason exported")
		}
	}
	if workflow.Status.Description == "secret failure" || attr(workflow.Attributes, "ronin.workflow.name").AsString() != "flow.lua" {
		t.Fatalf("workflow span: %v %v", workflow.Status, workflow.Attributes)
	}
}

func TestPluginIgnoresUnknownOperations(t *testing.T) {
	exporter, _ := providers(t)
	p := telemetry.NewPlugin()
	p.Observe(context.Background(), plugin.CycleEnded{Operation: plugin.Operation{ID: "missing"}})
	p.Observe(context.Background(), plugin.ModelRequestFirstOutput{Operation: plugin.Operation{ID: "missing"}})
	p.Observe(context.Background(), plugin.ContextCompacted{ParentID: "missing"})
	if n := len(exporter.GetSpans()); n != 0 {
		t.Fatalf("spans = %d", n)
	}
}
