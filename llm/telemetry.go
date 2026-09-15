package llm

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/crowl/ronin/plugin"
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
// The request is reported to plugins as a model request with the given purpose.
func PredictStructuredObserved(ctx context.Context, client ModelClient, req PredictNextStructuredRequest, purpose string) (raw json.RawMessage, err error) {
	host := plugin.FromContext(ctx)
	model := plugin.Model{Provider: client.Model().Provider, Name: client.Model().Name}
	ctx, op := plugin.Begin(ctx)
	host.Publish(ctx, plugin.ModelRequestStarted{Operation: op, Model: model, Purpose: purpose})
	ended := plugin.ModelRequestEnded{Operation: op, Model: model, Purpose: purpose}
	defer func() {
		ended.Err = err
		host.Publish(ctx, ended)
	}()
	result, err := client.PredictNextStructured(ctx, req)
	observation := StructuredUsage{Purpose: purpose, Model: client.Model()}
	if result != nil {
		raw = result.JSON
		if result.Usage != nil {
			u := *result.Usage
			u.Cost = EstimateCost(client.Model(), u)
			observation.Usage = &u
			ended.Usage = &plugin.Usage{
				InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
				CacheReadTokens: u.CachedTokens, CacheWriteTokens: u.CacheWriteTokens,
				Cost: u.Cost.Total, CostAvailable: u.Cost.Available,
			}
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
