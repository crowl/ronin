//go:build !darwin && !linux && !windows

package terminal

import "fmt"

type rawState struct{}

func makeRaw(fd uintptr) (*rawState, error) {
	return nil, fmt.Errorf("raw terminal mode is unsupported on this platform")
}

func (s *rawState) Restore() error { return nil }

func size(fd uintptr) (int, int, error) {
	return 0, 0, fmt.Errorf("terminal size is unsupported on this platform")
}
