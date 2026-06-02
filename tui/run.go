package tui

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/crowl/ronin/agent"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/tui/internal/render"
	"github.com/crowl/ronin/tui/internal/terminal"
)

type Agent interface {
	CWD() string
	Model() llm.Model
	ReasoningLevel() llm.ReasoningLevel
	ContextUsage() llm.Usage
	NewConversation() error
	CompactConversation() error
	SwitchModel(llm.Model) error
	SwitchReasoningLevel(llm.ReasoningLevel) error
	Prompt(context.Context, string) (<-chan agent.Event, <-chan error)
}

type Config struct {
	Agent    Agent
	Commands []Command
	Input    *os.File
	Output   *os.File
	Theme    Theme
}

func Run(ctx context.Context, cfg Config) error {
	if cfg.Agent == nil {
		return fmt.Errorf("agent is required")
	}
	if cfg.Input == nil {
		return fmt.Errorf("input is required")
	}
	if cfg.Output == nil {
		return fmt.Errorf("output is required")
	}

	theme := cfg.Theme
	if theme.Empty() {
		theme = DefaultTheme()
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
		Terminal: term,
		Agent:    cfg.Agent,
		Renderer: renderer,
		Commands: cfg.Commands,
		Theme:    theme,
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
