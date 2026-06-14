package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"

	_ "embed"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
)

var (
	//go:embed prompt_tool_output_summary.tmpl
	toolOutputSummaryPromptTemplateText string

	toolOutputSummaryPromptTemplate = template.Must(template.New("prompt.tool_output_summary").Parse(toolOutputSummaryPromptTemplateText))
)

const (
	defaultToolOutputSummaryMinBytes         = 16_000
	defaultToolOutputSummaryMaxSummaryTokens = 1_000
)

type ToolOutputSummarizer interface {
	SummarizeToolOutput(context.Context, ToolOutputSummaryRequest) (ToolOutputSummary, error)
}

type ToolOutputSummaryRequest struct {
	ToolName      string
	ToolCallID    string
	ToolArguments string
	ToolOutput    string
	ToolError     string
	Origin        string
	WasError      bool
}

type ToolOutputSummary struct {
	Summary string
	Omitted bool
}

type ToolOutputSummarizationPolicy struct {
	Enabled            bool
	MinBytes           int
	MinEstimatedTokens int
	MaxSummaryTokens   int
	SummarizeErrors    bool
	ExcludedTools      map[string]bool
}

func DefaultToolOutputSummarizationPolicy() ToolOutputSummarizationPolicy {
	return ToolOutputSummarizationPolicy{
		Enabled:          false,
		MinBytes:         defaultToolOutputSummaryMinBytes,
		MaxSummaryTokens: defaultToolOutputSummaryMaxSummaryTokens,
		SummarizeErrors:  false,
		ExcludedTools: map[string]bool{
			"read_file":  true,
			"edit_file":  true,
			"write_file": true,
		},
	}
}

type DefaultToolOutputSummarizerConfig struct {
	ModelClient llm.ModelClient
	Now         func() time.Time
}

func NewDefaultToolOutputSummarizer(cfg DefaultToolOutputSummarizerConfig) (*DefaultToolOutputSummarizer, error) {
	if cfg.ModelClient == nil {
		return nil, fmt.Errorf("model client is required")
	}
	return &DefaultToolOutputSummarizer{modelClient: cfg.ModelClient, now: cfg.Now}, nil
}

type DefaultToolOutputSummarizer struct {
	modelClient llm.ModelClient
	now         func() time.Time
}

type modelToolOutputSummary struct {
	Summary      string   `json:"summary" jsonschema:"Concise summary of the tool output relevant to the originating intent."`
	KeyFacts     []string `json:"key_facts" jsonschema:"Exact actionable facts, preserving paths, line numbers, symbols, test names, commands, exit codes, and errors."`
	Warnings     []string `json:"warnings" jsonschema:"Caveats such as truncation, uncertainty, or details that may need raw output inspection."`
	OmittedKinds []string `json:"omitted_kinds" jsonschema:"Broad categories of raw output intentionally omitted from the summary."`
}

func (s *DefaultToolOutputSummarizer) SummarizeToolOutput(ctx context.Context, req ToolOutputSummaryRequest) (ToolOutputSummary, error) {
	var prompt bytes.Buffer
	if err := toolOutputSummaryPromptTemplate.Execute(&prompt, req); err != nil {
		return ToolOutputSummary{}, fmt.Errorf("render tool output summary prompt: %w", err)
	}

	raw, err := s.modelClient.PredictNextStructured(ctx, llm.PredictNextStructuredRequest{
		SystemPrompt: "You summarize coding tool output into precise structured JSON.",
		Messages: []llm.Message{llm.UserMessage{
			Timestamp: s.nowTime(),
			Text:      prompt.String(),
		}},
		Schema:    jsonschema.FromType[modelToolOutputSummary](),
		MaxTokens: defaultToolOutputSummaryMaxSummaryTokens,
	})
	if err != nil {
		return ToolOutputSummary{}, fmt.Errorf("generate tool output summary: %w", err)
	}

	var summary modelToolOutputSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return ToolOutputSummary{}, fmt.Errorf("decode tool output summary: %w", err)
	}
	if strings.TrimSpace(summary.Summary) == "" && len(summary.KeyFacts) == 0 && len(summary.Warnings) == 0 {
		return ToolOutputSummary{}, fmt.Errorf("tool output summary is empty")
	}

	return ToolOutputSummary{Summary: renderToolOutputSummary(summary), Omitted: len(summary.OmittedKinds) > 0}, nil
}

func (s *DefaultToolOutputSummarizer) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func renderToolOutputSummary(summary modelToolOutputSummary) string {
	var b strings.Builder
	if strings.TrimSpace(summary.Summary) != "" {
		b.WriteString(strings.TrimSpace(summary.Summary))
		b.WriteString("\n")
	}
	writeSummaryList(&b, "Key facts", summary.KeyFacts)
	writeSummaryList(&b, "Warnings", summary.Warnings)
	writeSummaryList(&b, "Omitted raw output categories", summary.OmittedKinds)
	return strings.TrimSpace(b.String())
}

func writeSummaryList(b *strings.Builder, title string, values []string) {
	if len(values) == 0 {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString(title)
	b.WriteString(":\n")
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(value)
		b.WriteString("\n")
	}
}

func (p ToolOutputSummarizationPolicy) normalized() ToolOutputSummarizationPolicy {
	if p.MinBytes <= 0 {
		p.MinBytes = defaultToolOutputSummaryMinBytes
	}
	if p.MaxSummaryTokens <= 0 {
		p.MaxSummaryTokens = defaultToolOutputSummaryMaxSummaryTokens
	}
	return p
}
