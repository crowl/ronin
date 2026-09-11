package agentrun

import (
	"context"
	"fmt"
	"strings"

	"github.com/crowl/ronin/internal/agenttools"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/workflow"
)

// MCPSource supplies active external tools and their instructions.
type MCPSource interface {
	Tools() []runtime.Tool
	Instructions() []runtime.MCPInstruction
}

type Config struct {
	WorkingDir     string
	Model          llm.Model
	ReasoningLevel llm.ReasoningLevel
	MaxTurns       int
	MCP            MCPSource
	ResolveModel   func(llm.Model) (llm.Model, error)
}

// Runner owns agent execution policy and shared file mutation coordination.
// Each invocation gets an independent conversation and read history.
type Runner struct {
	cfg   Config
	tools *agenttools.Factory
}

func New(cfg Config) *Runner { return &Runner{cfg: cfg, tools: agenttools.NewFactory()} }

func (r *Runner) Run(ctx context.Context, req workflow.AgentRequest) (workflow.AgentResult, error) {
	model, level := r.cfg.Model, r.cfg.ReasoningLevel
	if req.Model.Provider != "" || req.Model.Name != "" {
		if r.cfg.ResolveModel == nil {
			return workflow.AgentResult{}, fmt.Errorf("model resolution is not configured")
		}
		var err error
		model, err = r.cfg.ResolveModel(req.Model)
		if err != nil {
			return workflow.AgentResult{}, err
		}
	}
	if req.ReasoningLevel != "" {
		level = req.ReasoningLevel
	}
	if !model.SupportsReasoning(level) {
		return workflow.AgentResult{}, fmt.Errorf("reasoning level %q is not supported by model %s", level, model)
	}
	cwd := r.cfg.WorkingDir
	managed := req.Workspace != ""
	if managed {
		cwd = req.Workspace
	}
	var external []runtime.Tool
	var instructions []runtime.MCPInstruction
	if !managed && !req.ReadOnly && r.cfg.MCP != nil {
		external = r.cfg.MCP.Tools()
		instructions = r.cfg.MCP.Instructions()
	}
	tools := r.tools.New(cwd, req.ReadOnly, managed, external)
	system, err := runtime.BuildSystemPrompt(runtime.SystemPromptInput{CWD: cwd, MCPInstructions: instructions})
	if err != nil {
		return workflow.AgentResult{}, fmt.Errorf("build agent system prompt: %w", err)
	}
	if req.ReadOnly {
		system += "\n\nThis agent is read-only. Do not modify files, run commands, or otherwise change repository or external state."
	}
	if strings.TrimSpace(req.System) != "" {
		system += "\n\nWorkflow agent instructions:\n" + strings.TrimSpace(req.System)
	}
	client, err := llm.LoadModelClient(model, level)
	if err != nil {
		return workflow.AgentResult{}, fmt.Errorf("load model client: %w", err)
	}
	if req.OutputSchema != nil {
		if err := validateWorkflowAgentOutputSchema(client, req.OutputSchema); err != nil {
			return workflow.AgentResult{}, fmt.Errorf("validate structured workflow agent output schema before running agent: %w", err)
		}
	}
	compactor := &runtime.DefaultCompactor{}
	conv, err := runtime.NewConversation(runtime.ConversationConfig{CWD: cwd, ModelClient: client, Compactor: compactor, Tools: tools, SystemPrompt: system, MaxTurns: r.cfg.MaxTurns, Session: session.Session{WorkingDir: cwd}})
	if err != nil {
		return workflow.AgentResult{}, err
	}
	text, err := runAgent(ctx, conv, req.Prompt, req.Progress)
	if err != nil {
		return workflow.AgentResult{}, err
	}
	result := workflow.AgentResult{Text: text}
	if req.OutputSchema != nil {
		outputCtx := llm.WithStructuredUsageRecorder(ctx, conv.RecordStructuredUsage)
		result.Output, err = structureWorkflowAgentOutput(outputCtx, client, text, req.OutputSchema)
		if err != nil {
			return workflow.AgentResult{}, fmt.Errorf("generate structured workflow agent output: %w", err)
		}
	}
	return result, nil
}
