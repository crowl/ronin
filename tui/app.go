package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/tui/internal/render"
	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/workflow"
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
	Terminal       terminalIO
	Conversation   Conversation
	WorkflowRunner WorkflowRunner
	Renderer       renderTarget
	Commands       []Command
	Theme          Theme
}

const (
	defaultInitialBoxesCapacity   = 1024
	defaultEventsBufferLen        = 64
	newConversationStartedMessage = "New session started"
)

func newApp(cfg appConfig) (*app, error) {
	model, err := newAppModel(cfg.Commands, cfg.Theme)
	if err != nil {
		return nil, err
	}
	model.populateInitialBoxes(cfg.Conversation)

	return &app{
		conversation:   cfg.Conversation,
		workflowRunner: cfg.WorkflowRunner,
		events:         make(chan event, defaultEventsBufferLen),
		terminal:       cfg.Terminal,
		renderer:       cfg.Renderer,
		model:          model,
	}, nil
}

type app struct {
	conversation   Conversation
	workflowRunner WorkflowRunner
	events         chan event

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
	case conversationEventReceived:
		update, err := app.model.handleConversationEvent(typedEvent.Event, time.Now())
		if err != nil {
			update = app.model.handleConversationError(err)
		}
		return app.applyUpdate(ctx, update)
	case conversationErrorReceived:
		return app.applyUpdate(ctx, app.model.handleConversationError(typedEvent.Err))
	case conversationPromptDone:
		app.cancelFunc = nil
		update, _ := app.model.finishPrompt()
		return app.applyUpdate(ctx, update)
	case conversationCompactionDone:
		app.cancelFunc = nil
		return app.applyUpdate(ctx, app.model.finishCompaction(typedEvent.Err))
	case workflowEventReceived:
		return app.applyUpdate(ctx, app.model.handleWorkflowEvent(typedEvent.Event, time.Now()))
	case workflowDone:
		app.cancelFunc = nil
		return app.applyUpdate(ctx, app.model.finishWorkflow(typedEvent.Err))
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
		if err := app.runCommand(ctx, action.Item, action.Command); err != nil {
			return err
		}
	case runWorkflowAction:
		app.runWorkflow(ctx, action.Workflow, action.Input)
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
			case app.events <- conversationPromptDone{}:
			case <-ctx.Done():
			}
		}()

		eventsCh, errCh := app.conversation.Prompt(promptCtx, prompt)

		for event := range eventsCh {
			select {
			case app.events <- conversationEventReceived{Event: event}:
			case <-ctx.Done():
				return
			}
		}

		select {
		case err := <-errCh:
			if err != nil {
				select {
				case app.events <- conversationErrorReceived{Err: err}:
				case <-ctx.Done():
				}
			}
		case <-promptCtx.Done():
			return
		}
	}()
}

func (app *app) runCommand(ctx context.Context, item menuItem, command Command) error {
	var err error
	switch typedCommand := command.(type) {
	case CompactConversation:
		app.compactConversation(ctx, item)
		return nil
	case StartNewConversation:
		err = app.conversation.NewConversation()
		if err != nil {
			err = fmt.Errorf("failed to start new conversation: %w", err)
			break
		}
		app.model.startNewConversation()
		return nil
	case SwitchModel:
		err = app.conversation.SwitchModel(typedCommand.Model)
		if err != nil {
			err = fmt.Errorf("failed to switch model: %w", err)
		}
	case SwitchReasoningLevel:
		err = app.conversation.SwitchReasoningLevel(typedCommand.Level)
		if err != nil {
			err = fmt.Errorf("failed to switch reasoning level: %w", err)
		}
	case SwitchTheme:
		if !app.model.setTheme(typedCommand.Theme) {
			err = fmt.Errorf("theme %q is unavailable", typedCommand.Name)
		}
	case InvokeSkill:
		_ = typedCommand.Skill
	case InvokeWorkflow:
		app.model.enterWorkflowInput(typedCommand.Workflow)
	case Exit:
		return errExitRequested
	}

	app.model.recordCommand(item, err)
	return nil
}

func (app *app) runWorkflow(ctx context.Context, item workflow.Workflow, input string) {
	app.model.startWorkflow(item, input)
	if app.workflowRunner == nil {
		app.model.handleWorkflowEvent(workflow.Finished{Result: workflow.Result{Name: item.Name, Input: input, Status: workflow.StatusFailed, Summary: "workflow runner is not configured"}}, time.Now())
		_ = app.applyUpdate(ctx, app.model.finishWorkflow(nil))
		return
	}

	workflowCtx, cancel := context.WithCancel(ctx)
	app.cancelFunc = cancel
	app.requestRender()
	go func() {
		defer cancel()
		result := app.workflowRunner.Run(workflowCtx, item, input, func(event workflow.Event) {
			select {
			case app.events <- workflowEventReceived{Event: event}:
			case <-ctx.Done():
			}
		})
		message := llm.WorkflowResultMessage{
			Timestamp: time.Now(), Name: result.Name, Input: result.Input,
			Status: llm.WorkflowStatus(result.Status), Summary: result.Summary,
		}
		err := app.conversation.RecordWorkflowResult(message)
		select {
		case app.events <- workflowDone{Err: err}:
		case <-ctx.Done():
		}
	}()
}

func (app *app) compactConversation(ctx context.Context, item menuItem) {
	app.model.startCompaction(item)

	compactCtx, cancel := context.WithCancel(ctx)
	app.cancelFunc = cancel
	app.requestRender()

	go func() {
		defer cancel()
		err := app.conversation.CompactConversation(compactCtx)
		if err != nil {
			err = fmt.Errorf("failed to compact conversation: %w", err)
		}
		select {
		case app.events <- conversationCompactionDone{Err: err}:
		case <-ctx.Done():
		}
	}()
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

	lines, err := app.model.lines(size.Width, app.conversation, time.Now())
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
