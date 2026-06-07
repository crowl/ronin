//go:build windows

package terminal

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

type rawState struct {
	handle syscall.Handle
	oldIn  uint32
	oldOut uint32
}

type coord struct {
	X int16
	Y int16
}

type smallRect struct {
	Left   int16
	Top    int16
	Right  int16
	Bottom int16
}

type consoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}

const (
	enableVirtualTerminalInput      = 0x0200
	enableVirtualTerminalProcessing = 0x0004
	enableProcessedInput            = 0x0001
	enableLineInput                 = 0x0002
	enableEchoInput                 = 0x0004
)

func makeRaw(fd uintptr) (*rawState, error) {
	inHandle, err := syscall.GetStdHandle(syscall.STD_INPUT_HANDLE)
	if err != nil {
		return nil, err
	}
	outHandle, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return nil, err
	}

	var oldIn, oldOut uint32
	r1, _, _ := syscall.SyscallN(procGetConsoleMode.Addr(), uintptr(inHandle), uintptr(unsafe.Pointer(&oldIn)))
	if r1 == 0 {
		return nil, fmt.Errorf("failed to get console input mode")
	}
	r2, _, _ := syscall.SyscallN(procGetConsoleMode.Addr(), uintptr(outHandle), uintptr(unsafe.Pointer(&oldOut)))
	if r2 == 0 {
		return nil, fmt.Errorf("failed to get console output mode")
	}

	rawIn := oldIn &^ (enableEchoInput | enableLineInput | enableProcessedInput)
	rawIn |= enableVirtualTerminalInput

	rawOut := oldOut | enableVirtualTerminalProcessing

	syscall.SyscallN(procSetConsoleMode.Addr(), uintptr(inHandle), uintptr(rawIn))
	syscall.SyscallN(procSetConsoleMode.Addr(), uintptr(outHandle), uintptr(rawOut))

	return &rawState{
		handle: inHandle,
		oldIn:  oldIn,
		oldOut: oldOut,
	}, nil
}

func (s *rawState) Restore() error {
	if s == nil {
		return nil
	}
	outHandle, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return err
	}
	syscall.SyscallN(procSetConsoleMode.Addr(), uintptr(s.handle), uintptr(s.oldIn))
	syscall.SyscallN(procSetConsoleMode.Addr(), uintptr(outHandle), uintptr(s.oldOut))
	return nil
}

func size(fd uintptr) (int, int, error) {
	outHandle, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return 0, 0, err
	}
	var info consoleScreenBufferInfo
	r1, _, _ := syscall.SyscallN(procGetConsoleScreenBufferInfo.Addr(), uintptr(outHandle), uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		return 80, 24, fmt.Errorf("failed to get console screen buffer info")
	}
	width := int(info.Window.Right - info.Window.Left + 1)
	height := int(info.Window.Bottom - info.Window.Top + 1)
	return width, height, nil
}
