package telemetry

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const instrumentation = "github.com/crowl/ronin"

type identityKey struct{}
type scopeKey struct{}
type modelKey struct{}
type purposeKey struct{}
type model struct{ provider, name string }

// WithModel records the model responsible for operations within ctx.
func WithModel(ctx context.Context, provider, name string) context.Context {
	return context.WithValue(ctx, modelKey{}, model{provider, name})
}

// WithPurpose labels auxiliary model requests without recording their prompts.
func WithPurpose(ctx context.Context, purpose string) context.Context {
	return context.WithValue(ctx, purposeKey{}, purpose)
}
func modelAttrs(m model) []attribute.KeyValue {
	return []attribute.KeyValue{attribute.String("gen_ai.provider.name", m.provider), attribute.String("gen_ai.request.model", m.name)}
}

// Operation owns one span and its duration. End must be called exactly once.
type Operation struct {
	ctx           context.Context
	usageRecorded bool
	span          trace.Span
	start         time.Time
	kind          string
	attrs         []attribute.KeyValue
	scope         *scope
}
type counts struct {
	requests, tools, attempts, input, output, cached, written int64
	cost                                                      float64
	unknownUsage, unknownCost                                 int64
}
type scope struct {
	mu     sync.Mutex
	kind   string
	parent *scope
	counts map[model]counts
}

// StartScope starts a prompt turn or cycle. Prompt turns isolate direct counts
// from child agents; descendant spans remain linked through normal context.
func StartScope(ctx context.Context, kind string, attrs ...attribute.KeyValue) (context.Context, *Operation) {
	parent, _ := ctx.Value(scopeKey{}).(*scope)
	if kind == "prompt_turn" {
		parent = nil
		ctx = context.WithValue(ctx, identityKey{}, []attribute.KeyValue(nil))
	}
	s := &scope{kind: kind, parent: parent, counts: make(map[model]counts)}
	m, _ := ctx.Value(modelKey{}).(model)
	s.counts[m] = counts{}
	ctx = context.WithValue(ctx, scopeKey{}, s)
	ctx, op := Start(ctx, kind, attrs...)
	identities, _ := ctx.Value(identityKey{}).([]attribute.KeyValue)
	identities = append([]attribute.KeyValue{}, identities...)
	if kind == "prompt_turn" {
		identities = nil
	}
	identities = append(identities, attrs...)
	identities = append(identities, attribute.String("ronin."+kind+".id", op.span.SpanContext().SpanID().String()))
	ctx = context.WithValue(ctx, identityKey{}, identities)
	op.ctx = ctx
	op.Attributes(identities...)
	op.scope = s
	return ctx, op
}

// Start begins an execution span. Only bounded dimensions are used for metrics;
// arbitrary attributes supplied here are trace-only.
func Start(ctx context.Context, kind string, attrs ...attribute.KeyValue) (context.Context, *Operation) {
	m, _ := ctx.Value(modelKey{}).(model)
	dimensions := modelAttrs(m)
	if p, ok := ctx.Value(purposeKey{}).(string); ok {
		dimensions = append(dimensions, attribute.String("ronin.request.purpose", p))
	}
	if kind == "tool" {
		for _, a := range attrs {
			if a.Key == "gen_ai.tool.name" {
				dimensions = append(dimensions, a)
			}
		}
	}
	identities, _ := ctx.Value(identityKey{}).([]attribute.KeyValue)
	spanAttrs := append(append(append([]attribute.KeyValue{}, dimensions...), identities...), attrs...)
	ctx, span := otel.Tracer(instrumentation).Start(ctx, "ronin."+kind, trace.WithAttributes(spanAttrs...))
	op := &Operation{ctx: ctx, span: span, start: time.Now(), kind: kind, attrs: dimensions}
	if kind == "request" || kind == "tool" || kind == "http_attempt" {
		for s, _ := ctx.Value(scopeKey{}).(*scope); s != nil; s = s.parent {
			s.mu.Lock()
			n := s.counts[m]
			if kind == "request" {
				n.requests++
				n.unknownUsage++
				n.unknownCost++
			} else if kind == "http_attempt" {
				n.attempts++
			} else {
				n.tools++
			}
			s.counts[m] = n
			s.mu.Unlock()
		}
	}
	return ctx, op
}

// Attributes adds trace-only metadata.
func (o *Operation) Attributes(attrs ...attribute.KeyValue) { o.span.SetAttributes(attrs...) }

// Event adds a timestamped trace event.
func (o *Operation) Event(name string) { o.span.AddEvent(name) }

// End records an outcome without exporting potentially sensitive error text.
func (o *Operation) End(err error) {
	outcome := "success"
	if err != nil {
		outcome = "error"
		if errors.Is(err, context.Canceled) {
			outcome = "cancelled"
		}
		o.span.SetStatus(codes.Error, outcome)
	}
	o.span.SetAttributes(attribute.String("ronin.outcome", outcome))
	attrs := append(append([]attribute.KeyValue{}, o.attrs...), attribute.String("ronin.outcome", outcome))
	meter := otel.Meter(instrumentation)
	duration, _ := meter.Float64Histogram("ronin."+o.kind+".duration", metric.WithUnit("s"))
	duration.Record(o.ctx, time.Since(o.start).Seconds(), metric.WithAttributes(attrs...))
	if o.kind == "request" || o.kind == "tool" || o.kind == "http_attempt" {
		count, _ := meter.Int64Counter("ronin." + o.kind + ".count")
		count.Add(o.ctx, 1, metric.WithAttributes(attrs...))
	}
	if o.scope != nil {
		o.scope.mu.Lock()
		var total counts
		for m, n := range o.scope.counts {
			total.requests += n.requests
			total.tools += n.tools
			total.attempts += n.attempts
			total.input += n.input
			total.output += n.output
			total.cached += n.cached
			total.written += n.written
			total.cost += n.cost
			total.unknownUsage += n.unknownUsage
			total.unknownCost += n.unknownCost
			for name, value := range map[string]int64{"requests": n.requests, "tool_calls": n.tools, "http_attempts": n.attempts, "input_tokens": n.input, "output_tokens": n.output, "cache_read_tokens": n.cached, "cache_write_tokens": n.written} {
				h, _ := meter.Int64Histogram("ronin." + o.kind + "." + name)
				dimensions := modelAttrs(m)
				if name == "input_tokens" || name == "output_tokens" || name == "cache_read_tokens" || name == "cache_write_tokens" {
					if n.requests == n.unknownUsage && n.requests > 0 {
						continue
					}
					dimensions = append(dimensions, attribute.Bool("ronin.usage.complete", n.unknownUsage == 0))
				}
				h.Record(o.ctx, value, metric.WithAttributes(dimensions...))
			}
		}
		o.scope.mu.Unlock()
		o.Attributes(attribute.Int64("ronin.request.count", total.requests), attribute.Int64("ronin.tool.count", total.tools), attribute.Int64("ronin.http_attempt.count", total.attempts), attribute.Int64("gen_ai.usage.input_tokens", total.input), attribute.Int64("gen_ai.usage.output_tokens", total.output), attribute.Int64("gen_ai.usage.cache_read.input_tokens", total.cached), attribute.Int64("gen_ai.usage.cache_creation.input_tokens", total.written), attribute.Bool("ronin.usage.complete", total.unknownUsage == 0), attribute.Float64("ronin.cost.estimated", total.cost), attribute.Bool("ronin.cost.complete", total.unknownCost == 0))
	}
	o.span.End()
}

// Usage records provider-reported usage. Categories are disjoint: input excludes
// cache reads/writes. No measurements are emitted for unavailable usage.
func (o *Operation) Usage(input, output, cached, written int, cost float64, costAvailable bool) {
	if o.usageRecorded {
		return
	}
	o.usageRecorded = true
	m, _ := o.ctx.Value(modelKey{}).(model)
	for s, _ := o.ctx.Value(scopeKey{}).(*scope); s != nil; s = s.parent {
		s.mu.Lock()
		n := s.counts[m]
		n.input += int64(input)
		n.output += int64(output)
		n.cached += int64(cached)
		n.written += int64(written)
		n.unknownUsage--
		if costAvailable {
			n.cost += cost
			n.unknownCost--
		}
		s.counts[m] = n
		s.mu.Unlock()
	}
	o.Attributes(attribute.Bool("ronin.usage.available", true), attribute.Int("gen_ai.usage.input_tokens", input), attribute.Int("gen_ai.usage.output_tokens", output), attribute.Int("gen_ai.usage.cache_read.input_tokens", cached), attribute.Int("gen_ai.usage.cache_creation.input_tokens", written), attribute.Bool("ronin.cost.available", costAvailable))
	counter, _ := otel.Meter(instrumentation).Int64Counter("ronin.token.usage", metric.WithUnit("{token}"))
	for category, n := range map[string]int{"input": max(input-cached-written, 0), "output": output, "cache_read": cached, "cache_write": written} {
		if n > 0 {
			attrs := append(append([]attribute.KeyValue{}, o.attrs...), attribute.String("ronin.token.category", category))
			counter.Add(o.ctx, int64(n), metric.WithAttributes(attrs...))
		}
	}
	if costAvailable {
		o.Attributes(attribute.Float64("ronin.cost.estimated", cost))
		c, _ := otel.Meter(instrumentation).Float64Counter("ronin.cost.estimated", metric.WithUnit("USD"))
		c.Add(o.ctx, cost, metric.WithAttributes(o.attrs...))
	}
}
