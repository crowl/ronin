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

type Config struct {
	Conversation   Conversation
	WorkflowRunner WorkflowRunner
	Commands       []Command
	Input          *os.File
	Output         *os.File
}

func Run(ctx context.Context, cfg Config) error {
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

	renderer, err := render.New(term)
	if err != nil {
		return fmt.Errorf("create renderer: %w", err)
	}

	application, err := newApp(appConfig{
		Terminal:       term,
		Conversation:   cfg.Conversation,
		WorkflowRunner: cfg.WorkflowRunner,
		Renderer:       renderer,
		Commands:       cfg.Commands,
	})
	if err != nil {
		return fmt.Errorf("create app: %w", err)
	}

	if err := term.Start(); err != nil {
		return fmt.Errorf("start terminal: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = term.Stop()
			panic(p)
		}
	}()
	defer func() {
		_ = term.Stop()
	}()

	if err := application.Run(ctx); err != nil {
		if errors.Is(err, errExitRequested) {
			return nil
		}
		return fmt.Errorf("run app: %w", err)
	}

	return nil
}
