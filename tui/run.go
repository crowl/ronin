package tui

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/tui/internal/render"
	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/workflow"
)

type WorkflowRunner interface {
	Run(context.Context, workflow.Workflow, string, func(workflow.Event)) workflow.Result
}

type Conversation interface {
	CWD() string
	Model() llm.Model
	ReasoningLevel() llm.ReasoningLevel
	ContextUsage() llm.Usage
	Messages() []llm.Message
	RecordWorkflowResult(llm.WorkflowResultMessage) error
	NewConversation() error
	CompactConversation(context.Context) error
	SwitchModel(llm.Model) error
	SwitchReasoningLevel(llm.ReasoningLevel) error
	Prompt(context.Context, string) (<-chan runtime.Event, <-chan error)
	ToolCallTitle(name string, arguments []byte) string
}

type HistoryConversation interface {
	RewindPoints() []runtime.RewindPoint
	Rewind(context.Context, runtime.RewindPoint) error
	Fork(context.Context, runtime.RewindPoint) error
}

type MCPActivator interface {
	ActivateMCP(context.Context, string) (bool, error)
}

type Config struct {
	Conversation   Conversation
	WorkflowRunner WorkflowRunner
	MCPActivator   MCPActivator
	Commands       []Command
	Input          *os.File
	Output         *os.File
}

func Run(ctx context.Context, cfg Config) (runErr error) {
	if cfg.Conversation == nil {
		return fmt.Errorf("conversation is required")
	}
	if cfg.Input == nil {
		return fmt.Errorf("input is required")
	}
	if cfg.Output == nil {
		return fmt.Errorf("output is required")
	}

	term, err := terminal.New(terminal.Config{
		Input:  cfg.Input,
		Output: cfg.Output,
	})
	if err != nil {
		return fmt.Errorf("create terminal: %w", err)
	}

	defer func() { runErr = errors.Join(runErr, term.Stop()) }()

	renderer, err := render.New(term)
	if err != nil {
		return fmt.Errorf("create renderer: %w", err)
	}

	application, err := newApp(appConfig{
		Terminal:       term,
		Conversation:   cfg.Conversation,
		WorkflowRunner: cfg.WorkflowRunner,
		MCPActivator:   cfg.MCPActivator,
		Renderer:       renderer,
		Commands:       cfg.Commands,
	})
	if err != nil {
		return fmt.Errorf("create app: %w", err)
	}

	if err := term.Start(); err != nil {
		return fmt.Errorf("start terminal: %w", err)
	}

	if err := application.Run(ctx); err != nil {
		if errors.Is(err, errExitRequested) {
			return nil
		}
		return fmt.Errorf("run app: %w", err)
	}

	return nil
}
