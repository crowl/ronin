//go:build windows

package terminal

import (
	"context"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

var cancelSynchronousIO = windows.NewLazySystemDLL("kernel32.dll").NewProc("CancelSynchronousIo")

func (t *Terminal) readInput(ctx context.Context) ([]byte, error) {
	// Pin the sole reader so cancellation targets exactly its synchronous read.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	thread, err := windows.OpenThread(windows.THREAD_TERMINATE, false, windows.GetCurrentThreadId())
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(thread)
	if err := cancelSynchronousIO.Find(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			// Retry covers cancellation just before ReadFile enters the kernel.
			_, _, _ = cancelSynchronousIO.Call(uintptr(thread))
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() { close(done); <-stopped }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var buffer [bufferLen]byte
	var n uint32
	err = windows.ReadFile(windows.Handle(t.input.Fd()), buffer[:], &n, nil)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), buffer[:n]...), nil
}
