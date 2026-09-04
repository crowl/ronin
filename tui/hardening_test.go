package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tui/internal/terminal"
)

func TestUnknownToolLifecycle(t *testing.T) {
	app := newTestApp(t, testAppConfig{})
	for _, event := range []runtime.Event{runtime.ToolExecutionFailed{CallID: "missing", Error: errors.New("unknown tool")}, runtime.ToolExecutionEnded{CallID: "missing"}} {
		if _, err := app.model.handleConversationEvent(event, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	b := app.model.boxes[0].(toolCallBox)
	if b.Error != "unknown tool" || b.EndedAt.IsZero() {
		t.Fatalf("box=%+v", b)
	}
}

func TestCommandsRejectedWhileBusy(t *testing.T) {
	for _, command := range []Command{StartNewConversation{}, CompactConversation{}, SwitchModel{}, SwitchReasoningLevel{}, RewindConversation{}, ForkConversation{}, InvokeWorkflow{}, ActivateMCP{}} {
		app := newTestApp(t, testAppConfig{})
		app.model.working = true
		if err := app.runCommand(t.Context(), menuItem{Value: "test"}, command); err != nil {
			t.Fatal(err)
		}
		if !app.model.working || len(app.model.boxes) != 2 {
			t.Fatalf("command %T was not rejected", command)
		}
	}
}

func TestBusyRenderingUsesSnapshot(t *testing.T) {
	app := newTestApp(t, testAppConfig{})
	app.conversation = &snapshotOnlyConversation{}
	app.model.working = true
	if err := app.render(); err != nil {
		t.Fatal(err)
	}
}

type snapshotOnlyConversation struct{ fakeConversation }

func (*snapshotOnlyConversation) ContextUsage() llm.Usage { panic("live usage read while busy") }

func TestAppExitJoinsReader(t *testing.T) {
	app := newTestApp(t, testAppConfig{})
	term := &cancelTerminal{done: make(chan struct{})}
	app.terminal = term
	app.events <- terminalKeyRead{Key: terminal.Key{Type: terminal.KeyCtrlC}}
	if err := app.Run(context.Background()); !errors.Is(err, errExitRequested) {
		t.Fatal(err)
	}
	select {
	case <-term.done:
	default:
		t.Fatal("reader outlived app")
	}
}

type cancelTerminal struct{ done chan struct{} }

func (t *cancelTerminal) ReadKey(ctx context.Context) (terminal.Key, error) {
	defer close(t.done)
	<-ctx.Done()
	return terminal.Key{}, ctx.Err()
}
func (*cancelTerminal) Write(string) error { return nil }
func (*cancelTerminal) Size() (terminal.Size, error) {
	return terminal.Size{Width: 80, Height: 24}, nil
}

func TestToolDisplayBudget(t *testing.T) {
	b := toolCallBox{}
	b.addDisplayArtifact(tool.TextArtifact{Text: strings.Repeat("x", maxToolDisplayBytes+1)})
	if !b.DisplayTruncated || b.DisplayBytes > maxToolDisplayBytes {
		t.Fatalf("budget=%d truncated=%v", b.DisplayBytes, b.DisplayTruncated)
	}
	before := b.Revision
	b.addDisplayArtifact(tool.TextArtifact{Text: "ignored"})
	if b.Revision != before {
		t.Fatal("truncated box retained more data")
	}
}

func BenchmarkToolCacheSignature(b *testing.B) {
	box := toolCallBox{}
	box.addDisplayArtifact(tool.TextArtifact{Text: strings.Repeat("x", maxToolDisplayBytes)})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = boxSignature(box, 80, false, time.Time{})
	}
}
