package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

type Config struct {
	Input  *os.File
	Output *os.File
}

func New(cfg Config) (*Terminal, error) {
	if cfg.Input == nil {
		return nil, fmt.Errorf("input is required")
	}
	if cfg.Output == nil {
		return nil, fmt.Errorf("output is required")
	}
	if !isCharDevice(cfg.Input) {
		return nil, fmt.Errorf("terminal stdin is required")
	}
	if !isCharDevice(cfg.Output) {
		return nil, fmt.Errorf("terminal stdout is required")
	}

	raw, err := makeRaw(cfg.Input.Fd())
	if err != nil {
		return nil, fmt.Errorf("make terminal raw: %w", err)
	}

	return &Terminal{
		input:  cfg.Input,
		output: cfg.Output,
		raw:    raw,
	}, nil
}

const bufferLen = 4096

type Terminal struct {
	input  *os.File
	output *os.File

	raw *rawState

	pendingKeys []Key

	buffer [bufferLen]byte
}

func (t *Terminal) Start() error {
	_, err := t.output.WriteString(KeyboardEnhancePush + PasteEnable + CursorHide)
	if err != nil {
		return fmt.Errorf("write terminal state: %w", err)
	}

	return nil
}

func (t *Terminal) Stop() error {
	if t.output != nil {
		_, err := t.output.WriteString(CRLF + AutoWrapEnable + PasteDisable + KeyboardEnhancePop + CursorShow)
		if err != nil {
			return fmt.Errorf("write terminal state: %w", err)
		}
	}

	if t.raw != nil {
		if err := t.raw.Restore(); err != nil {
			return fmt.Errorf("restore terminal state: %w", err)
		}
	}

	return nil
}

func (t *Terminal) ReadKey(ctx context.Context) (Key, error) {
	if len(t.pendingKeys) > 0 {
		key := t.pendingKeys[0]
		t.pendingKeys = t.pendingKeys[1:]
		return key, nil
	}

	data, err := t.readInput(ctx)
	if err != nil {
		return Key{}, fmt.Errorf("read from terminal: %w", err)
	}

	data, err = t.readPendingEscapeInput(ctx, data)
	if err != nil {
		return Key{}, fmt.Errorf("read pending escape input: %w", err)
	}

	for bytes.HasPrefix(data, []byte(PasteStart)) && !bytes.Contains(data, []byte(PasteEnd)) {
		more, err := t.readInput(ctx)
		if err != nil {
			return Key{}, fmt.Errorf("read from terminal: %w", err)
		}
		data = append(data, more...)
	}

	keys := ParseKeys(data)
	if len(keys) == 0 {
		return Key{Type: KeyUnknown}, nil
	}

	if len(keys) > 1 {
		t.pendingKeys = append(t.pendingKeys, keys[1:]...)
	}

	return keys[0], nil
}

func (t *Terminal) Write(data string) error {
	if t.output == nil {
		return io.ErrClosedPipe
	}

	if _, err := t.output.WriteString(data); err != nil {
		return fmt.Errorf("write to terminal: %w", err)
	}

	return nil
}

type Size struct {
	Width  int
	Height int
}

func (t *Terminal) Size() (Size, error) {
	width, height, err := size(t.output.Fd())
	if err != nil {
		return Size{}, fmt.Errorf("get terminal size: %w", err)
	}
	return Size{
		Width:  width,
		Height: height,
	}, nil
}

type readResult struct {
	n   int
	err error
}

func (t *Terminal) readInput(ctx context.Context) ([]byte, error) {
	ch := make(chan readResult, 1)

	go func() {
		n, err := t.input.Read(t.buffer[:])
		ch <- readResult{n: n, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		return append([]byte(nil), t.buffer[:res.n]...), nil
	}
}

const pendingEscapeInputTimeout = 50 * time.Millisecond

func (t *Terminal) readPendingEscapeInput(ctx context.Context, data []byte) ([]byte, error) {
	for shouldReadPendingEscapeInput(data) {
		readCtx, cancel := context.WithTimeout(ctx, pendingEscapeInputTimeout)
		more, err := t.readInput(readCtx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return data, nil
			}
			return nil, err
		}
		data = append(data, more...)
	}
	return data, nil
}

func shouldReadPendingEscapeInput(data []byte) bool {
	if len(data) == 0 || data[0] != Escape[0] {
		return false
	}
	if len(data) == 1 {
		return true
	}
	if bytes.HasPrefix(data, []byte(PasteStart)) && !bytes.Contains(data, []byte(PasteEnd)) {
		return true
	}
	if data[1] == '[' {
		for i := 2; i < len(data); i++ {
			if isANSIFinalByte(data[i]) {
				return false
			}
		}
		return true
	}
	if data[1] == 'O' {
		return len(data) < 3
	}
	return false
}

func isANSIFinalByte(b byte) bool {
	return b >= 0x40 && b <= 0x7e
}

func MoveUp(n int) string {
	return fmt.Sprintf(MoveUpLines, n)
}

func MoveDown(n int) string {
	return fmt.Sprintf(MoveDownLines, n)
}

func isCharDevice(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
