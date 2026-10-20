package antigravity

import (
	"encoding/json"

	"github.com/crowl/ronin/jsonschema"
)

// Antigravity speaks Cloud Code Assist: a standard Gemini GenerateContent
// request wrapped in an envelope that names the backend model and the session.
// The envelope's `project` field is deliberately absent — the Limes proxy owns
// the credential and injects the project the credential is bound to.
type envelope struct {
	Model       string          `json:"model"`
	RequestType string          `json:"requestType,omitempty"`
	UserAgent   string          `json:"userAgent,omitempty"`
	RequestID   string          `json:"requestId,omitempty"`
	Request     generateRequest `json:"request"`
}

type generateRequest struct {
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	Contents          []content         `json:"contents"`
	Tools             []toolDeclaration `json:"tools,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
	SessionID         string            `json:"sessionId,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
}

type functionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type functionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type toolDeclaration struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations"`
}

type functionDeclaration struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Parameters  *jsonschema.Schema `json:"parameters,omitempty"`
}

type generationConfig struct {
	MaxOutputTokens    int                `json:"maxOutputTokens,omitempty"`
	ThinkingConfig     *thinkingConfig    `json:"thinkingConfig,omitempty"`
	ResponseMimeType   string             `json:"responseMimeType,omitempty"`
	ResponseJSONSchema *jsonschema.Schema `json:"responseJsonSchema,omitempty"`
}

// streamChunk is one SSE payload. Cloud Code Assist nests the Gemini reply
// under "response"; some endpoints return it bare, so both are accepted.
type streamChunk struct {
	Response *generateResponse `json:"response"`
	generateResponse
}

type generateResponse struct {
	Candidates    []candidate    `json:"candidates"`
	UsageMetadata *usageMetadata `json:"usageMetadata"`
}

type candidate struct {
	Content      content `json:"content"`
	FinishReason string  `json:"finishReason"`
}

type usageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
}

// reply returns the Gemini payload from either envelope shape.
func (chunk streamChunk) reply() generateResponse {
	if chunk.Response != nil {
		return *chunk.Response
	}
	return chunk.generateResponse
}
