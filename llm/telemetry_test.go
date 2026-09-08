package llm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/crowl/ronin/llm"
)

type observedClient struct {
	fakeModelClient
	result *llm.StructuredResult
	err    error
}

func (c *observedClient) PredictNextStructured(context.Context, llm.PredictNextStructuredRequest) (*llm.StructuredResult, error) {
	return c.result, c.err
}

func TestStructuredObservationPreservesErrorsAndUsage(t *testing.T) {
	providerErr := errors.New("invalid output")
	recorderErr := errors.New("persistence failed")
	client := &observedClient{result: &llm.StructuredResult{Usage: &llm.Usage{InputTokens: 100}}, err: providerErr}
	calls := 0
	ctx := llm.WithStructuredUsageRecorder(t.Context(), func(_ context.Context, record llm.StructuredUsage) error {
		calls++
		if record.Purpose != "compaction" || record.Usage == nil || record.Usage.InputTokens != 100 {
			t.Fatalf("record = %+v", record)
		}
		return recorderErr
	})
	_, err := llm.PredictStructuredObserved(ctx, client, llm.PredictNextStructuredRequest{}, "compaction")
	if calls != 1 || !errors.Is(err, providerErr) || !errors.Is(err, recorderErr) {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestStructuredObservationRejectsMissingResult(t *testing.T) {
	_, err := llm.PredictStructuredObserved(t.Context(), &observedClient{}, llm.PredictNextStructuredRequest{}, "compaction")
	if err == nil {
		t.Fatal("nil result accepted")
	}
}
