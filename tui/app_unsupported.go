//go:build !(unix || darwin || linux || windows)

package tui

import "context"

func (app *app) watchResize(ctx context.Context) {}
