package telemetry_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crowl/ronin/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func providers(t *testing.T) (*tracetest.InMemoryExporter, *sdkmetric.ManualReader) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	oldT, oldM := otel.GetTracerProvider(), otel.GetMeterProvider()
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		otel.SetTracerProvider(oldT)
		otel.SetMeterProvider(oldM)
		_ = tp.Shutdown(context.Background())
		_ = mp.Shutdown(context.Background())
	})
	return exporter, reader
}
func attr(attrs []attribute.KeyValue, key string) attribute.Value {
	for _, a := range attrs {
		if string(a.Key) == key {
			return a.Value
		}
	}
	return attribute.Value{}
}

func TestScopesAndModelAccounting(t *testing.T) {
	exporter, reader := providers(t)
	ctx := telemetry.WithModel(context.Background(), "provider", "model-a")
	ctx, prompt := telemetry.StartScope(ctx, "prompt_turn", attribute.String("ronin.session.id", "secret-session"))
	cycleCtx, cycle := telemetry.StartScope(ctx, "cycle")
	requestCtx, request := telemetry.Start(cycleCtx, "request")
	_, attempt := telemetry.Start(requestCtx, "http_attempt")
	attempt.End(nil)
	request.Usage(100, 20, 30, 10, 0.5, true)
	request.End(nil)
	_, tool := telemetry.Start(cycleCtx, "tool", attribute.String("gen_ai.tool.name", "read_file"), attribute.String("gen_ai.tool.call.id", "call-1"))
	tool.End(errors.New("secret error"))
	// Child agent operations must not inflate the parent's direct counters.
	childCtx, child := telemetry.StartScope(telemetry.WithModel(cycleCtx, "provider", "model-b"), "prompt_turn")
	_, childRequest := telemetry.Start(childCtx, "request")
	childRequest.End(context.Canceled)
	child.End(context.Canceled)
	cycle.End(nil)
	prompt.End(nil)
	spans := exporter.GetSpans()
	var promptSpan, cycleSpan tracetest.SpanStub
	for _, s := range spans {
		if s.Name == "ronin.prompt_turn" && attr(s.Attributes, "gen_ai.request.model").AsString() == "model-a" {
			promptSpan = s
		}
		if s.Name == "ronin.cycle" {
			cycleSpan = s
		}
		if strings.Contains(s.Status.Description, "secret") {
			t.Fatal("error content leaked")
		}
	}
	if cycleSpan.Parent.SpanID() != promptSpan.SpanContext.SpanID() {
		t.Fatal("cycle parent mismatch")
	}
	for _, s := range []tracetest.SpanStub{promptSpan, cycleSpan} {
		if attr(s.Attributes, "ronin.request.count").AsInt64() != 1 || attr(s.Attributes, "ronin.tool.count").AsInt64() != 1 || attr(s.Attributes, "ronin.http_attempt.count").AsInt64() != 1 {
			t.Fatalf("bad counts: %v", s.Attributes)
		}
		if attr(s.Attributes, "gen_ai.usage.input_tokens").AsInt64() != 100 || !attr(s.Attributes, "ronin.usage.complete").AsBool() {
			t.Fatalf("bad usage: %v", s.Attributes)
		}
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	var tokens int64
	for _, sm := range data.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					if _, ok := p.Attributes.Value("ronin.session.id"); ok {
						t.Fatal("session metric label")
					}
					if _, ok := p.Attributes.Value("gen_ai.tool.call.id"); ok {
						t.Fatal("call ID metric label")
					}
					if m.Name == "ronin.token.usage" {
						tokens += p.Value
					}
				}
			}
		}
	}
	if tokens != 120 {
		t.Fatalf("tokens=%d, want 120", tokens)
	}
}
