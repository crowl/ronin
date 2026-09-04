//go:build !linux && !darwin && !windows

package terminal

import (
	"context"
	"errors"
)

func (t *Terminal) readInput(context.Context) ([]byte, error) {
	return nil, errors.New("terminal input is unsupported on this platform")
}
