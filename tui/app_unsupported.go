//go:build !(unix || darwin || linux)

package tui

import "context"

func (app *app) watchResize(ctx context.Context) {}
