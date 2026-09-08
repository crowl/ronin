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
	PredictNextStructured(context.Context, PredictNextStructuredRequest) (*StructuredResult, error)
}

type StructuredOutputSchemaValidator interface {
	ValidateStructuredOutputSchema(*jsonschema.Schema) error
}

type PredictNextRequest struct {
	// CacheKey is an opaque, stable identity for one conversation, not prompt content.
	CacheKey     string
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
	MaxTokens    int
}

// StructuredResult retains provider usage even when output validation fails.
// A nil Usage means the provider did not report usage, not a free request.
type StructuredResult struct {
	JSON  json.RawMessage
	Usage *Usage
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
