package llm

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/crowl/ronin/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// StructuredUsage describes one auxiliary request. Nil usage means unknown.
type StructuredUsage struct {
	Purpose string
	Model   Model
	Usage   *Usage
}

type structuredUsageRecorderKey struct{}

// WithStructuredUsageRecorder installs a synchronous recorder for auxiliary
// requests owned by this context. The recorder must not initiate model calls.
func WithStructuredUsageRecorder(ctx context.Context, record func(context.Context, StructuredUsage) error) context.Context {
	return context.WithValue(ctx, structuredUsageRecorderKey{}, record)
}

// PredictStructuredObserved records usage once, including billed responses whose
// output is invalid. Auxiliary usage stays separate from conversation messages.
func PredictStructuredObserved(ctx context.Context, client ModelClient, req PredictNextStructuredRequest, purpose string) (raw json.RawMessage, err error) {
	ctx = telemetry.WithModel(ctx, client.Model().Provider, client.Model().Name)
	ctx = telemetry.WithPurpose(ctx, purpose)
	ctx, op := telemetry.Start(ctx, "request", attribute.Bool("ronin.usage.available", false))
	defer func() { op.End(err) }()
	result, err := client.PredictNextStructured(ctx, req)
	observation := StructuredUsage{Purpose: purpose, Model: client.Model()}
	if result != nil {
		raw = result.JSON
		if result.Usage != nil {
			u := *result.Usage
			u.Cost = EstimateCost(client.Model(), u)
			observation.Usage = &u
			op.Usage(u.InputTokens, u.OutputTokens, u.CachedTokens, u.CacheWriteTokens, u.Cost.Total, u.Cost.Available)
		}
	}
	if record, ok := ctx.Value(structuredUsageRecorderKey{}).(func(context.Context, StructuredUsage) error); ok {
		err = errors.Join(err, record(ctx, observation))
	}
	if err == nil && result == nil {
		err = errors.New("structured prediction returned no result")
	}
	return raw, err
}
