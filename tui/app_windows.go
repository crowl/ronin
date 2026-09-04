//go:build windows

package tui

import (
	"context"
	"time"
)

func (app *app) watchResize(ctx context.Context) {
	app.workers.Go(func() {
		var lastWidth, lastHeight int
		if sz, err := app.terminal.Size(); err == nil {
			lastWidth = sz.Width
			lastHeight = sz.Height
		}

		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sz, err := app.terminal.Size()
				if err != nil {
					continue
				}
				if sz.Width != lastWidth || sz.Height != lastHeight {
					lastWidth = sz.Width
					lastHeight = sz.Height
					select {
					case app.events <- terminalResized{}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	})
}
