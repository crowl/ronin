package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/crowl/ronin/tui/internal/render"
	"github.com/crowl/ronin/tui/internal/terminal"
)

var errExitRequested = errors.New("exit requested")

type terminalIO interface {
	ReadKey(ctx context.Context) (terminal.Key, error)
	Write(data string) error
	Size() (terminal.Size, error)
}

type renderTarget interface {
	Render(render.Request) error
}

type appConfig struct {
	Terminal terminalIO
	Agent    Agent
	Renderer renderTarget
	Commands []Command
	Theme    Theme
}

const (
	defaultInitialBoxesCapacity = 1024
	defaultEventsBufferLen      = 64
)

func newApp(cfg appConfig) (*app, error) {
	model, err := newAppModel(cfg.Commands, cfg.Theme)
	if err != nil {
		return nil, err
	}

	return &app{
		agent:    cfg.Agent,
		events:   make(chan event, defaultEventsBufferLen),
		terminal: cfg.Terminal,
		renderer: cfg.Renderer,
		model:    model,
	}, nil
}

type app struct {
	agent  Agent
	events chan event

	terminal terminalIO
	renderer renderTarget
	model    *appModel

	size       terminal.Size
	sizeCached bool

	cancelFunc context.CancelFunc

	renderRequested     bool
	renderTimer         *time.Timer
	renderTimerActive   bool
	lastRenderStartedAt time.Time
}

func (app *app) Run(ctx context.Context) error {
	app.watchResize(ctx)
	app.startKeyReader(ctx)
	app.startWorkingTicker(ctx)

	app.requestRender()

	for {
		var renderTimerC <-chan time.Time
		if app.renderTimer != nil && app.renderTimerActive {
			renderTimerC = app.renderTimer.C
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-app.events:
			if err := app.handleAppEvent(ctx, event); err != nil {
				return err
			}
		case <-renderTimerC:
			app.renderTimerActive = false
			if err := app.render(); err != nil {
				return err
			}
		}
	}
}

func (app *app) startKeyReader(ctx context.Context) {
	go func() {
		for {
			key, err := app.terminal.ReadKey(ctx)
			if err != nil {
				select {
				case app.events <- terminalReadFailed{Err: err}:
				case <-ctx.Done():
				}
				return
			}

			select {
			case app.events <- terminalKeyRead{Key: key}:
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (app *app) startWorkingTicker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case app.events <- workingTick{}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

func (app *app) handleAppEvent(ctx context.Context, event event) error {
	switch typedEvent := event.(type) {
	case terminalKeyRead:
		update, err := app.model.handleKey(typedEvent.Key)
		if err != nil {
			return err
		}
		return app.applyUpdate(ctx, update)
	case terminalReadFailed:
		return typedEvent.Err
	case terminalResized:
		size, err := app.terminal.Size()
		if err != nil {
			return fmt.Errorf("failed to get terminal size: %w", err)
		}
		app.size = size
		app.sizeCached = true
		app.requestRender()
	case workingTick:
		return app.applyUpdate(ctx, app.model.tickWorking())
	case renderRequested:
		return app.render()
	case agentEventReceived:
		update, err := app.model.handleAgentEvent(typedEvent.Event, time.Now())
		if err != nil {
			update = app.model.handleAgentError(err)
		}
		return app.applyUpdate(ctx, update)
	case agentErrorReceived:
		return app.applyUpdate(ctx, app.model.handleAgentError(typedEvent.Err))
	case agentPromptDone:
		app.cancelFunc = nil
		update, _ := app.model.finishPrompt()
		return app.applyUpdate(ctx, update)
	}
	return nil
}

func (app *app) handleKey(ctx context.Context, key terminal.Key) error {
	update, err := app.model.handleKey(key)
	if err != nil {
		return err
	}
	return app.applyUpdate(ctx, update)
}

func (app *app) applyUpdate(ctx context.Context, update modelUpdate) error {
	if update.Action == nil {
		update.Action = noAction{}
	}

	switch action := update.Action.(type) {
	case noAction:
	case exitAction:
		return errExitRequested
	case cancelPromptAction:
		if app.cancelFunc != nil {
			app.cancelFunc()
		}
	case submitPromptAction:
		app.submitPrompt(ctx, action.Prompt)
	case runCommandAction:
		if err := app.runCommand(action.Item, action.Command); err != nil {
			return err
		}
	}

	if update.Render {
		app.requestRender()
	}
	return nil
}

func (app *app) submitPrompt(ctx context.Context, prompt string) {
	if app.model.working {
		app.model.queueSteeringPrompt(prompt)
		app.requestRender()
		return
	}

	app.model.startPrompt(prompt)

	promptCtx, cancel := context.WithCancel(ctx)

	app.cancelFunc = cancel
	app.requestRender()

	go func() {
		defer cancel()
		defer func() {
			select {
			case app.events <- agentPromptDone{}:
			case <-ctx.Done():
			}
		}()

		eventsCh, errCh := app.agent.Prompt(promptCtx, prompt)

		for event := range eventsCh {
			select {
			case app.events <- agentEventReceived{Event: event}:
			case <-ctx.Done():
				return
			}
		}

		select {
		case err := <-errCh:
			if err != nil {
				select {
				case app.events <- agentErrorReceived{Err: err}:
				case <-ctx.Done():
				}
			}
		case <-promptCtx.Done():
			return
		}
	}()
}

func (app *app) runCommand(item menuItem, command Command) error {
	var err error
	switch typedCommand := command.(type) {
	case StartNewConversation:
		err = app.agent.NewConversation()
		if err != nil {
			err = fmt.Errorf("failed to start new conversation: %w", err)
		}
	case CompactConversation:
		err = app.agent.CompactConversation()
		if err != nil {
			err = fmt.Errorf("failed to compact conversation: %w", err)
		}
	case SwitchModel:
		err = app.agent.SwitchModel(typedCommand.Model)
		if err != nil {
			err = fmt.Errorf("failed to switch model: %w", err)
		}
	case SwitchReasoningLevel:
		err = app.agent.SwitchReasoningLevel(typedCommand.Level)
		if err != nil {
			err = fmt.Errorf("failed to switch reasoning level: %w", err)
		}
	case InvokeSkill:
		_ = typedCommand.Skill
	case Exit:
		return errExitRequested
	}

	app.model.recordCommand(item, err)
	return nil
}

func (app *app) requestRender() {
	if app.renderRequested {
		return
	}

	app.renderRequested = true

	delay := time.Duration(0)
	if !app.lastRenderStartedAt.IsZero() {
		elapsed := time.Since(app.lastRenderStartedAt)
		if elapsed < renderInterval {
			delay = renderInterval - elapsed
		}
	}

	if delay == 0 {
		select {
		case app.events <- renderRequested{}:
			return
		default:
		}
	}

	if app.renderTimerActive {
		return
	}

	if app.renderTimer == nil {
		app.renderTimer = time.NewTimer(delay)
	} else {
		app.renderTimer.Reset(delay)
	}

	app.renderTimerActive = true
}

const renderInterval = time.Second / 60

func (app *app) render() error {
	app.renderRequested = false
	app.lastRenderStartedAt = time.Now()

	size := app.size
	if !app.sizeCached {
		var err error
		size, err = app.terminal.Size()
		if err != nil {
			return fmt.Errorf("failed to get terminal size: %w", err)
		}
		app.size = size
		app.sizeCached = true
	}

	lines, err := app.model.lines(size.Width, app.agent, time.Now())
	if err != nil {
		return fmt.Errorf("failed to get lines: %w", err)
	}

	err = app.renderer.Render(render.Request{
		Lines:  lines,
		Width:  size.Width,
		Height: size.Height,
		Force:  false,
	})
	if err != nil {
		return fmt.Errorf("failed to render: %w", err)
	}

	return nil
}
