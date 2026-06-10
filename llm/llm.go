package llm

import (
	"context"
	"encoding/json"

	"github.com/crowl/ronin/jsonschema"
)

type ModelClient interface {
	Model() Model
	ReasoningLevel() ReasoningLevel
	SetReasoningLevel(ReasoningLevel) error
	PredictNext(context.Context, PredictNextRequest) (<-chan PredictionEvent, <-chan error)
	PredictNextStructured(context.Context, PredictNextStructuredRequest) (json.RawMessage, error)
}

type PredictNextRequest struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
	MaxTokens    int
}

type PredictNextStructuredRequest struct {
	SystemPrompt string
	Messages     []Message
	Schema       *jsonschema.Schema
	MaxTokens    int
}

type Tool interface {
	Name() string
	Description() string
	Parameters() *jsonschema.Schema
}
