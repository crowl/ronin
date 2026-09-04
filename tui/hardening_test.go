package tui

import (
	"context"
	"errors"
	"github.com/crowl/ronin/tui/internal/terminal"
	"testing"
)

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
