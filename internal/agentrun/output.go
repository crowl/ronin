package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/workflow"
	"strings"
	"time"
)

func validateWorkflowAgentOutputSchema(client llm.ModelClient, schema *jsonschema.Schema) error {
	if validator, ok := client.(llm.StructuredOutputSchemaValidator); ok {
		return validator.ValidateStructuredOutputSchema(schema)
	}
	return nil
}

const maxStructuredOutputCorrectionAttempts = 2
const maxInvalidStructuredOutputBytes = 4096

func structureWorkflowAgentOutput(ctx context.Context, client llm.ModelClient, report string, schema *jsonschema.Schema) (json.RawMessage, error) {
	prompt := report
	var lastOutput json.RawMessage
	var lastValidationErr error

	for attempt := 0; attempt <= maxStructuredOutputCorrectionAttempts; attempt++ {
		raw, err := llm.PredictStructuredObserved(ctx, client, llm.PredictNextStructuredRequest{
			SystemPrompt: "Convert the supplied agent report into JSON matching the requested schema. Preserve its decisions exactly and do not add new work. Return substantive values from the report, never schema examples or placeholders.",
			Messages: []llm.Message{llm.UserMessage{
				Timestamp: time.Now(),
				Text:      prompt,
			}},
			Schema: schema,
		}, "workflow_output")
		if err != nil {
			return nil, err
		}
		if err := jsonschema.Validate(schema, raw); err == nil {
			return raw, nil
		} else {
			lastOutput = append(lastOutput[:0], raw...)
			lastValidationErr = err
		}
		if attempt < maxStructuredOutputCorrectionAttempts {
			prompt = structuredOutputCorrectionPrompt(report, raw, lastValidationErr)
		}
	}

	return nil, fmt.Errorf(
		"structured conversion remained invalid after %d correction attempts: %v; invalid output: %s; original agent report (the agent was not rerun): %s",
		maxStructuredOutputCorrectionAttempts,
		lastValidationErr,
		boundedStructuredOutput(lastOutput),
		boundedStructuredOutput([]byte(report)),
	)
}

func structuredOutputCorrectionPrompt(report string, invalid json.RawMessage, validationErr error) string {
	return "Original agent report (authoritative; preserve its decisions):\n\n" + report +
		"\n\nThe prior JSON conversion was invalid:\n\n" + boundedStructuredOutput(invalid) +
		"\n\nValidation failures:\n\n" + validationErr.Error() +
		"\n\nCorrect only the JSON conversion. Use substantive values from the original report; do not use placeholder values such as `string`, and do not add new work."
}

func boundedStructuredOutput(raw []byte) string {
	if len(raw) <= maxInvalidStructuredOutputBytes {
		return string(raw)
	}
	return string(raw[:maxInvalidStructuredOutputBytes]) + fmt.Sprintf("... [truncated %d bytes]", len(raw)-maxInvalidStructuredOutputBytes)
}

type prompter interface {
	Prompt(context.Context, string) (<-chan runtime.Event, <-chan error)
}

func runAgent(ctx context.Context, conv prompter, prompt string, progress func(workflow.AgentEvent)) (string, error) {
	var output strings.Builder
	events, errs := conv.Prompt(ctx, prompt)
	for event := range events {
		switch event := event.(type) {
		case runtime.AssistantThinkingDeltaReceived:
			if progress != nil {
				progress(workflow.AgentThinkingDelta{Text: event.Text})
			}
		case runtime.AssistantMessageDeltaReceived:
			output.WriteString(event.Text)
			if progress != nil {
				progress(workflow.AgentTextDelta{Text: event.Text})
			}
		case runtime.ToolExecutionStarted:
			if progress != nil {
				progress(workflow.AgentToolStarted{ID: event.CallID, Title: event.CallTitle})
			}
		case runtime.ToolExecutionOutputDeltaReceived:
			if progress != nil {
				progress(workflow.AgentToolOutput{ID: event.CallID, Artifact: event.Artifact})
			}
		case runtime.ToolExecutionResultReceived:
			if progress != nil {
				for _, artifact := range event.Artifacts {
					progress(workflow.AgentToolOutput{ID: event.CallID, Artifact: artifact})
				}
			}
		case runtime.ToolExecutionFailed:
			if progress != nil {
				progress(workflow.AgentToolFailed{ID: event.CallID, Error: event.Error.Error()})
			}
		case runtime.ToolExecutionEnded:
			if progress != nil {
				progress(workflow.AgentToolEnded{ID: event.CallID})
			}
		}
	}
	if err, ok := <-errs; ok && err != nil {
		return "", err
	}
	return output.String(), nil
}
