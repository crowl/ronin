//go:build unix || darwin || linux

package tui

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func (app *app) watchResize(ctx context.Context) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(signals)
		for {
			select {
			case <-ctx.Done():
				return
			case <-signals:
				select {
				case app.events <- terminalResized{}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}
