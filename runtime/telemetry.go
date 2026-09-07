package runtime

import (
	"context"
	"errors"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// The forwarding goroutine owns both output channels and drains the provider on
// cancellation. This keeps observation independent of UI consumption.
func predictObserved(ctx context.Context, client llm.ModelClient, req llm.PredictNextRequest) (<-chan llm.PredictionEvent, <-chan error) {
	ctx = telemetry.WithModel(ctx, client.Model().Provider, client.Model().Name)
	ctx = telemetry.WithPurpose(ctx, "conversation")
	ctx, op := telemetry.Start(ctx, "request", attribute.Bool("ronin.usage.available", false))
	source, sourceErr := client.PredictNext(ctx, req)
	events := make(chan llm.PredictionEvent, 32)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		finished, first := false, false
		for event := range source {
			switch e := event.(type) {
			case llm.TextDelta, llm.ThinkingDelta:
				if !first {
					op.Event("ronin.first_output")
					first = true
				}
			case llm.PredictionFinished:
				finished = true
				cost := llm.EstimateCost(client.Model(), e.Usage)
				op.Usage(e.Usage.InputTokens, e.Usage.OutputTokens, e.Usage.CachedTokens, e.Usage.CacheWriteTokens, cost.Total, cost.Available)
				op.Attributes(attribute.String("gen_ai.response.finish_reason", string(e.StopReason)))
			}
			select {
			case events <- event:
			case <-ctx.Done():
			}
		}
		err := <-sourceErr
		if err == nil {
			err = ctx.Err()
		}
		observationErr := err
		if observationErr == nil && !finished {
			observationErr = errors.New("prediction ended without completion")
		}
		op.End(observationErr)
		if err != nil {
			errs <- err
		}
	}()
	return events, errs
}

type toolOperationKey struct{}
type toolObservation struct {
	op  *telemetry.Operation
	err error
}
