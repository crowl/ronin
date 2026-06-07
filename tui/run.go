package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

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
	Messages() []llm.Message
	NewConversation() error
	CompactConversation(context.Context) error
	SwitchModel(llm.Model) error
	SwitchReasoningLevel(llm.ReasoningLevel) error
	Prompt(context.Context, string) (<-chan agent.Event, <-chan error)
	ToolCallTitle(name string, arguments []byte) string
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

	term, err := terminal.New(terminal.Config{
		Input:  cfg.Input,
		Output: cfg.Output,
	})
	if err != nil {
		return fmt.Errorf("create terminal: %w", err)
	}

	theme := resolveTheme(cfg.Theme, term)

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

const backgroundColorTimeout = 100 * time.Millisecond

type backgroundColorReader interface {
	BackgroundColor(time.Duration) (terminal.RGB, bool, error)
}

func resolveTheme(configured Theme, term backgroundColorReader) Theme {
	if !configured.Empty() {
		return configured
	}

	if term != nil {
		color, ok, err := term.BackgroundColor(backgroundColorTimeout)
		if err == nil && ok {
			if terminalColorIsLight(color) {
				return LightTheme()
			}
			return DarkTheme()
		}
	}

	return DefaultTheme()
}

func terminalColorIsLight(color terminal.RGB) bool {
	luminance := 0.299*float64(color.R) + 0.587*float64(color.G) + 0.114*float64(color.B)
	return luminance >= 128
}
