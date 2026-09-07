package llm

import (
	"context"
	"encoding/json"

	"github.com/crowl/ronin/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// PredictStructuredObserved records a structured request at its call boundary.
// ModelClient does not expose structured-response usage, so it remains unknown.
func PredictStructuredObserved(ctx context.Context, client ModelClient, req PredictNextStructuredRequest, purpose string) (raw json.RawMessage, err error) {
	ctx = telemetry.WithModel(ctx, client.Model().Provider, client.Model().Name)
	ctx = telemetry.WithPurpose(ctx, purpose)
	ctx, op := telemetry.Start(ctx, "request", attribute.Bool("ronin.usage.available", false))
	defer func() { op.End(err) }()
	return client.PredictNextStructured(ctx, req)
}
