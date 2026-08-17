package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/tool"
)

// Tool runs a named startup-catalog workflow for the primary conversation.
type Tool struct {
	catalog    *Catalog
	workingDir string
	agent      AgentFunc
}

// NewTool creates a run_workflow tool backed by catalog.
func NewTool(catalog *Catalog, workingDir string, agent AgentFunc) *Tool {
	return &Tool{catalog: catalog, workingDir: workingDir, agent: agent}
}

type toolArgs struct {
	Name  string `json:"name" jsonschema:"Name of an available workflow."`
	Input string `json:"input" jsonschema:"Prompt to expose to the workflow as ronin.input."`
}

func (a toolArgs) Validate() error {
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("workflow name is required")
	}
	if len(a.Input) > 1<<20 {
		return fmt.Errorf("workflow input exceeds the 1 MiB limit")
	}
	return nil
}

type ToolResult struct {
	Name    string `json:"name"`
	Input   string `json:"input"`
	Status  Status `json:"status"`
	Summary string `json:"summary"`
}

func (r ToolResult) Artifacts() []tool.Artifact {
	return []tool.Artifact{tool.TextArtifact{Text: r.Summary}}
}

func (t *Tool) Name() string { return "run_workflow" }

func (t *Tool) Description() string {
	return "Run a named workflow loaded from Ronin's global workflow catalog. Use when the user asks to invoke a workflow with a prompt developed in the conversation."
}

func (t *Tool) Parameters() *jsonschema.Schema {
	schema := jsonschema.FromType[toolArgs]()
	if names := t.names(); len(names) > 0 {
		enum := make([]any, len(names))
		for i, name := range names {
			enum[i] = name
		}
		schema.Properties["name"].Enum = enum
	}
	return schema
}

func (t *Tool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	return tool.CallTyped(ctx, raw, t.call)
}

func (t *Tool) CallIncremental(ctx context.Context, raw json.RawMessage, emit func(tool.Artifact) error) (any, error) {
	args, err := tool.DecodeArgs[toolArgs](raw)
	if err != nil {
		return nil, err
	}
	return t.run(ctx, args, func(event Event) {
		if artifact := workflowEventArtifact(event); artifact != nil {
			_ = emit(artifact)
		}
	})
}

func (t *Tool) CallTitle(raw json.RawMessage) (string, error) {
	args, err := tool.DecodeArgs[toolArgs](raw)
	if err != nil {
		return "", err
	}
	return "run workflow " + args.Name, nil
}

func (t *Tool) call(ctx context.Context, args toolArgs) (ToolResult, error) {
	return t.run(ctx, args, nil)
}

func (t *Tool) run(ctx context.Context, args toolArgs, emit func(Event)) (ToolResult, error) {
	item, ok := t.catalog.Lookup(args.Name)
	if !ok {
		return ToolResult{}, fmt.Errorf("workflow %q is unavailable", args.Name)
	}
	result := Run(ctx, item, t.workingDir, args.Input, t.agent, emit)
	toolResult := ToolResult(result)
	if result.Status == StatusFailed {
		return toolResult, fmt.Errorf("workflow %q failed: %s", result.Name, result.Summary)
	}
	if result.Status == StatusCancelled {
		return toolResult, context.Canceled
	}
	return toolResult, nil
}

func workflowEventArtifact(event Event) tool.Artifact {
	switch event := event.(type) {
	case Started:
		return tool.TextArtifact{Text: "Workflow " + event.Name + " started\n"}
	case Log:
		return tool.TextArtifact{Text: event.Text + "\n"}
	case AgentStarted:
		return tool.TextArtifact{Text: fmt.Sprintf("Agent %d started\n", event.Invocation)}
	case AgentEventReceived:
		switch progress := event.Event.(type) {
		case AgentThinkingDelta:
			return tool.TextArtifact{Text: progress.Text}
		case AgentTextDelta:
			return tool.TextArtifact{Text: progress.Text}
		case AgentToolStarted:
			return tool.TextArtifact{Text: progress.Title + "\n"}
		case AgentToolOutput:
			return progress.Artifact
		case AgentToolFailed:
			return tool.TextArtifact{Text: progress.Error + "\n"}
		}
	case AgentFinished:
		return tool.TextArtifact{Text: fmt.Sprintf("Agent %d finished\n", event.Invocation)}
	case Finished:
		return tool.TextArtifact{Text: fmt.Sprintf("Workflow %s: %s\n", event.Result.Status, event.Result.Summary)}
	}
	return nil
}

func (t *Tool) names() []string {
	items := t.catalog.Workflows()
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = item.Name
	}
	return names
}
