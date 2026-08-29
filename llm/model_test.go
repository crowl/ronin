package llm_test

import (
	"math"
	"reflect"
	"testing"

	"github.com/crowl/ronin/llm"
)

func TestNearestReasoningLevel(t *testing.T) {
	supported := llm.NewReasoningSet(llm.ReasoningLevelOff, llm.ReasoningLevelHigh)
	got, ok := llm.NearestReasoningLevel(llm.ReasoningLevelMedium, supported)
	if !ok || got != llm.ReasoningLevelHigh {
		t.Fatalf("NearestReasoningLevel() = %q, %v, want high, true", got, ok)
	}
}

func TestEstimateCost(t *testing.T) {
	model := llm.Model{Pricing: llm.ModelPricing{
		Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 3,
		HasInput: true, HasOutput: true, HasCacheRead: true, HasCacheWrite: true,
	}}
	cost := llm.EstimateCost(model, llm.Usage{InputTokens: 1_000_000, OutputTokens: 100_000, CachedTokens: 200_000, CacheWriteTokens: 100_000})
	if !cost.Available || math.Abs(cost.Total-2.74) > 1e-9 {
		t.Fatalf("EstimateCost() = %#v, want total 2.74", cost)
	}

	unpriced := llm.EstimateCost(llm.Model{}, llm.Usage{InputTokens: 1})
	if unpriced.Available {
		t.Fatalf("EstimateCost() = %#v, want unavailable", unpriced)
	}
}

func TestModelReasoningLevels(t *testing.T) {
	model := llm.Model{SupportedReasoning: llm.NewReasoningSet(llm.ReasoningLevelOff, llm.ReasoningLevelHigh)}
	if got, want := model.ReasoningLevels(), []llm.ReasoningLevel{llm.ReasoningLevelOff, llm.ReasoningLevelHigh}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ReasoningLevels() = %v, want %v", got, want)
	}
}
