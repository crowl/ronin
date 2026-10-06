package tui

import (
	"context"
	"testing"

	"github.com/crowl/ronin/tui/internal/terminal"
)

func TestOperationCompletionQueuePolicy(t *testing.T) {
	for _, kind := range []operationKind{operationPrompt, operationCompaction, operationMCP, operationShell} {
		m := newTestModel(t)
		m.beginOperation(kind, "active")
		m.queueSteeringPrompt("next")
		update := m.completeOperation()
		action, submitted := update.Action.(submitPromptAction)
		want := kind != operationShell
		if submitted != want || (submitted && action.Prompt != "next") {
			t.Fatalf("kind=%d action=%+v", kind, update.Action)
		}
		if m.busy() || m.shellActive() || m.operation.label != "" || m.steeringPrompt != "" {
			t.Fatalf("completion left state: %+v", m.operation)
		}
		if m.completeOperation().Action != nil {
			t.Fatal("completion submitted queue twice")
		}
	}
}

func TestCancellationRetainsOperationUntilCompletion(t *testing.T) {
	for _, kind := range []operationKind{operationPrompt, operationCompaction, operationMCP, operationShell} {
		app := newTestApp(t, testAppConfig{})
		app.model.beginOperation(kind, "active")
		ctx, _ := app.operationContext(t.Context())
		if err := app.handleKey(t.Context(), terminal.Key{Type: terminal.KeyEscape}); err != nil {
			t.Fatal(err)
		}
		if ctx.Err() != context.Canceled || !app.model.busy() || app.model.operation.kind != kind {
			t.Fatal("cancellation released foreground ownership")
		}
		app.releaseOperation()
		app.model.completeOperation()
		if app.cancelFunc != nil || app.model.busy() {
			t.Fatal("completion retained ownership")
		}
	}
}
