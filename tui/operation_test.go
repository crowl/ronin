package tui

import (
	"context"
	"github.com/crowl/ronin/tui/internal/terminal"
	"testing"
)

func TestOperationCompletionQueuePolicy(t *testing.T) {
	for _, kind := range []operationKind{operationPrompt, operationWorkflow, operationCompaction, operationMCP, operationShell} {
		for _, cancelled := range []bool{false, true} {
			m := newTestModel(t)
			m.beginOperation(kind, "active")
			m.queueSteeringPrompt("next")
			update := m.completeOperation(cancelled)
			action, submitted := update.Action.(submitPromptAction)
			want := kind != operationShell && !(kind == operationWorkflow && cancelled)
			if submitted != want || (submitted && action.Prompt != "next") {
				t.Fatalf("kind=%d cancelled=%v action=%+v", kind, cancelled, update.Action)
			}
			if m.busy() || m.shellActive() || m.operation.label != "" || m.steeringPrompt != "" {
				t.Fatalf("completion left state: %+v", m.operation)
			}
			if m.completeOperation(cancelled).Action != nil {
				t.Fatal("completion submitted queue twice")
			}
		}
	}
}

func TestCancellationRetainsOperationUntilCompletion(t *testing.T) {
	for _, kind := range []operationKind{operationPrompt, operationWorkflow, operationCompaction, operationMCP, operationShell} {
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
		app.model.completeOperation(true)
		if app.cancelFunc != nil || app.model.busy() {
			t.Fatal("completion retained ownership")
		}
	}
}
