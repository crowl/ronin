package telemetry

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"

	"github.com/crowl/ronin/plugin"
)

// Plugin exports execution events as OpenTelemetry traces and metrics. It
// rebuilds span parentage from event operation identifiers, so spans nest as
// workflow > prompt turn > cycle > request/tool, and request > HTTP attempt.
//
// Start configures the global providers from the OTEL_* environment (see
// Setup); Observe works against whatever global providers are installed, so
// tests can inject in-memory exporters without calling Start.
type Plugin struct {
	mu       sync.Mutex
	open     map[string]*span
	shutdown func(context.Context) error
}

type span struct {
	ctx context.Context
	op  *Operation
}

// NewPlugin returns an OpenTelemetry plugin that is inactive until Start.
func NewPlugin() *Plugin {
	return &Plugin{open: make(map[string]*span)}
}

func (p *Plugin) Name() string { return "opentelemetry" }

// Start configures exporters. A failure leaves the global no-op providers in
// place; the Host then drops this plugin.
func (p *Plugin) Start(ctx context.Context) error {
	shutdown, err := Setup(ctx)
	if err != nil {
		return err
	}
	p.shutdown = shutdown
	return nil
}

// Close flushes pending telemetry within ctx's deadline.
func (p *Plugin) Close(ctx context.Context) error {
	if p.shutdown == nil {
		return nil
	}
	return p.shutdown(ctx)
}

func (p *Plugin) Observe(_ context.Context, event plugin.Event) {
	switch e := event.(type) {
	case plugin.WorkflowStarted:
		ctx, op := Start(p.parent(e.ParentID), "workflow", attribute.String("ronin.workflow.name", e.Name))
		p.begin(e.ID, ctx, op)
	case plugin.WorkflowEnded:
		p.end(e.ID, e.Err)
	case plugin.PromptTurnStarted:
		ctx := withModel(p.parent(e.ParentID), e.Model)
		ctx, op := StartScope(ctx, "prompt_turn", attribute.String("ronin.session.id", e.SessionID))
		p.begin(e.ID, ctx, op)
	case plugin.PromptTurnEnded:
		if s := p.lookup(e.ID); s != nil {
			s.op.Attributes(attribute.Int("ronin.cycle.count", e.Cycles))
		}
		p.end(e.ID, e.Err)
	case plugin.CycleStarted:
		ctx := withModel(p.parent(e.ParentID), e.Model)
		ctx, op := StartScope(ctx, "cycle", attribute.Int("ronin.cycle.index", e.Index))
		p.begin(e.ID, ctx, op)
	case plugin.CycleEnded:
		p.end(e.ID, e.Err)
	case plugin.ModelRequestStarted:
		ctx := WithPurpose(withModel(p.parent(e.ParentID), e.Model), e.Purpose)
		ctx, op := Start(ctx, "request", attribute.Bool("ronin.usage.available", false))
		p.begin(e.ID, ctx, op)
	case plugin.ModelRequestFirstOutput:
		if s := p.lookup(e.ID); s != nil {
			s.op.Event("ronin.first_output")
		}
	case plugin.ModelRequestEnded:
		if s := p.lookup(e.ID); s != nil {
			if u := e.Usage; u != nil {
				s.op.Usage(u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.Cost, u.CostAvailable)
			}
			if e.StopReason != "" {
				s.op.Attributes(attribute.String("gen_ai.response.finish_reason", e.StopReason))
			}
		}
		p.end(e.ID, e.Err)
	case plugin.HTTPAttemptStarted:
		ctx, op := Start(p.parent(e.ParentID), "http_attempt", attribute.Int("ronin.request.attempt", e.Attempt))
		p.begin(e.ID, ctx, op)
	case plugin.HTTPAttemptEnded:
		if s := p.lookup(e.ID); s != nil && e.StatusCode != 0 {
			s.op.Attributes(attribute.Int("http.response.status_code", e.StatusCode))
		}
		p.end(e.ID, e.Err)
	case plugin.ToolCallStarted:
		ctx, op := p.startTool(e.ParentID, e.Model, e.Call)
		p.begin(e.ID, ctx, op)
	case plugin.ToolCallDenied:
		_, op := p.startTool(e.ParentID, e.Model, e.Call)
		op.Attributes(attribute.Bool("ronin.tool.rejected", e.Rejected), attribute.Bool("ronin.tool.denied", !e.Rejected))
		op.End(e.Err)
	case plugin.ToolCallEnded:
		if s := p.lookup(e.ID); s != nil && e.Err == nil {
			s.op.Attributes(attribute.Int("ronin.tool.result.size", e.ResultSize))
		}
		p.end(e.ID, e.Err)
	case plugin.ContextCompacted:
		if s := p.lookup(e.ParentID); s != nil {
			s.op.Event("ronin.context_compacted")
		}
	case plugin.SessionSaveFailed:
		if s := p.lookup(e.ParentID); s != nil {
			s.op.Event("ronin.session_save_failed")
		}
	}
}

func (p *Plugin) startTool(parentID string, model plugin.Model, call plugin.ToolCall) (context.Context, *Operation) {
	ctx := withModel(p.parent(parentID), model)
	ctx, op := Start(ctx, "tool", attribute.String("gen_ai.tool.name", call.Name), attribute.String("gen_ai.tool.call.id", call.ID))
	op.Attributes(attribute.Int("ronin.tool.arguments.size", len(call.Arguments)))
	return ctx, op
}

func withModel(ctx context.Context, m plugin.Model) context.Context {
	return WithModel(ctx, m.Provider, m.Name)
}

// parent returns the context of an open operation, or a root context when the
// parent is unknown (for example when it ended before its child was reported).
func (p *Plugin) parent(id string) context.Context {
	if s := p.lookup(id); s != nil {
		return s.ctx
	}
	return context.Background()
}

func (p *Plugin) lookup(id string) *span {
	if id == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.open[id]
}

func (p *Plugin) begin(id string, ctx context.Context, op *Operation) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.open[id] = &span{ctx: ctx, op: op}
}

func (p *Plugin) end(id string, err error) {
	p.mu.Lock()
	s, ok := p.open[id]
	delete(p.open, id)
	p.mu.Unlock()
	if ok {
		s.op.End(err)
	}
}
