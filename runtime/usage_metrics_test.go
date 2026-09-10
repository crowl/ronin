package runtime_test

import (
	"context"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestUsageMetricsAccumulateAcrossRequestsAndPrompts(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	old := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(old); _ = provider.Shutdown(context.Background()) })
	usage := llm.Usage{InputTokens: 100, OutputTokens: 20, CachedTokens: 30, CacheWriteTokens: 10}
	model := llm.Model{Provider: "test", Name: "model", Pricing: llm.ModelPricing{Input: 1, Output: 2, CacheRead: .5, CacheWrite: 1.5, HasInput: true, HasOutput: true, HasCacheRead: true, HasCacheWrite: true}}
	client := &fakeModelClient{model: model, eventBatches: [][]llm.PredictionEvent{
		{llm.BlockEnded{Block: llm.ToolCallBlock{ID: "call", Name: "missing"}}, llm.PredictionFinished{Usage: usage}},
		{llm.PredictionFinished{Usage: usage}},
		{llm.PredictionFinished{Usage: usage}},
	}}
	conv, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		events, errs := conv.Prompt(t.Context(), "test")
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		var data metricdata.ResourceMetrics
		if err := reader.Collect(t.Context(), &data); err != nil {
			t.Fatal(err)
		}
		tokens := map[string]int64{}
		var cost float64
		for _, scope := range data.ScopeMetrics {
			for _, metric := range scope.Metrics {
				switch metric.Name {
				case "ronin.token.usage":
					sum := metric.Data.(metricdata.Sum[int64])
					if !sum.IsMonotonic || sum.Temporality != metricdata.CumulativeTemporality {
						t.Fatalf("not a cumulative counter: %+v", sum)
					}
					for _, point := range sum.DataPoints {
						category, _ := point.Attributes.Value("ronin.token.category")
						tokens[category.AsString()] += point.Value
					}
				case "ronin.cost.estimated":
					for _, point := range metric.Data.(metricdata.Sum[float64]).DataPoints {
						cost += point.Value
					}
				}
			}
		}
		requests := int64(i + 2)
		for category, perRequest := range map[string]int64{"input": 60, "output": 20, "cache_read": 30, "cache_write": 10} {
			if tokens[category] != requests*perRequest {
				t.Fatalf("prompt %d: %s = %d, want %d", i, category, tokens[category], requests*perRequest)
			}
		}
		wantCost := float64(requests) * llm.EstimateCost(model, usage).Total
		if diff := cost - wantCost; diff < -1e-12 || diff > 1e-12 {
			t.Fatalf("cost = %g, want %g", cost, wantCost)
		}
	}
}
