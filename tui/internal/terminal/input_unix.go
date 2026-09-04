//go:build linux || darwin

package terminal

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// The key reader is the sole reader of this descriptor. Polling bounds
// cancellation latency without abandoning a blocking read or closing stdin.
func (t *Terminal) readInput(ctx context.Context) ([]byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		timeout := 25
		if deadline, ok := ctx.Deadline(); ok {
			timeout = max(1, min(timeout, int(time.Until(deadline).Milliseconds())))
		}
		fds := []unix.PollFd{{Fd: int32(t.input.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, timeout)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		var buffer [bufferLen]byte
		n, err = t.input.Read(buffer[:])
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), buffer[:n]...), nil
	}
}
