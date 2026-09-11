package tui

import "context"

// operationContext binds cancellation to the foreground worker. The worker must
// drain its output before sending its completion event; cancellation alone does
// not release ownership or permit another operation to start.
func (app *app) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	app.cancelFunc = cancel
	return ctx, cancel
}

func (app *app) cancelOperation() {
	if app.cancelFunc != nil {
		app.cancelFunc()
	}
}

func (app *app) releaseOperation() {
	app.cancelOperation()
	app.cancelFunc = nil
}
