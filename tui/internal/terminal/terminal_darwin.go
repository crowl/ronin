//go:build darwin

package terminal

import (
	"fmt"
	"syscall"
	"unsafe"
)

type rawState struct {
	fd  uintptr
	old syscall.Termios
}

type winsize struct {
	Rows uint16
	Cols uint16
}

func makeRaw(fd uintptr) (*rawState, error) {
	var old syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGETA), uintptr(unsafe.Pointer(&old))); errno != 0 {
		return nil, fmt.Errorf("get terminal mode: %w", errno)
	}
	raw := old
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCSETA), uintptr(unsafe.Pointer(&raw))); errno != 0 {
		return nil, fmt.Errorf("set raw terminal mode: %w", errno)
	}
	return &rawState{fd: fd, old: old}, nil
}

func (s *rawState) Restore() error {
	if s == nil {
		return nil
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, s.fd, uintptr(syscall.TIOCSETA), uintptr(unsafe.Pointer(&s.old))); errno != 0 {
		return fmt.Errorf("restore terminal mode: %w", errno)
	}
	return nil
}

func size(fd uintptr) (int, int, error) {
	var ws winsize
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws))); errno != 0 {
		return 0, 0, fmt.Errorf("get terminal size: %w", errno)
	}
	if ws.Cols == 0 || ws.Rows == 0 {
		return 80, 24, nil
	}
	return int(ws.Cols), int(ws.Rows), nil
}
