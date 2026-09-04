package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
	"unicode/utf8"
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

	pendingInput []byte
	stopped      bool
}

func (t *Terminal) Start() error {
	_, err := t.output.WriteString(KeyboardEnhancePush + PasteEnable + CursorHide)
	if err != nil {
		return fmt.Errorf("write terminal state: %w", err)
	}

	return nil
}

func (t *Terminal) Stop() error {
	if t.stopped {
		return nil
	}
	t.stopped = true
	var errs []error
	if t.output != nil {
		_, err := t.output.WriteString(CRLF + AutoWrapEnable + PasteDisable + KeyboardEnhancePop + CursorShow)
		if err != nil {
			errs = append(errs, fmt.Errorf("write terminal state: %w", err))
		}
	}

	if t.raw != nil {
		if err := t.raw.Restore(); err != nil {
			errs = append(errs, fmt.Errorf("restore terminal state: %w", err))
		}
	}

	return errors.Join(errs...)
}

func (t *Terminal) ReadKey(ctx context.Context) (Key, error) {
	if len(t.pendingKeys) > 0 {
		key := t.pendingKeys[0]
		t.pendingKeys = t.pendingKeys[1:]
		return key, nil
	}

	for {
		data := t.pendingInput
		if len(data) > 0 {
			n := keySequenceLength(data)
			if n > 0 {
				keys := ParseKeys(data[:n])
				t.pendingInput = data[n:]
				if len(keys) > 0 {
					return keys[0], nil
				}
				continue
			}
		}
		readCtx := ctx
		cancel := func() {}
		if len(data) > 0 && data[0] == 27 && !bytes.HasPrefix(data, []byte(PasteStart)) {
			readCtx, cancel = context.WithTimeout(ctx, pendingEscapeInputTimeout)
		}
		more, err := t.readInput(readCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return Key{}, ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) && len(data) > 0 {
				t.pendingInput = data[1:]
				return Key{Type: KeyEscape}, nil
			}
			return Key{}, err
		}
		t.pendingInput = append(t.pendingInput, more...)
		if len(t.pendingInput) > 1<<20 {
			t.pendingInput = nil
			return Key{}, errors.New("terminal input exceeds 1 MiB")
		}
	}
}

func keySequenceLength(data []byte) int {
	if bytes.HasPrefix(data, []byte(PasteStart)) {
		if end := bytes.Index(data, []byte(PasteEnd)); end >= 0 {
			return end + len(PasteEnd)
		}
		return 0
	}
	if data[0] == 27 {
		if len(data) == 1 {
			return 0
		}
		if data[1] == '[' {
			for i := 2; i < len(data); i++ {
				if isANSIFinalByte(data[i]) {
					return i + 1
				}
			}
			return 0
		}
		if data[1] == 'O' {
			if len(data) < 3 {
				return 0
			}
			return 3
		}
		return 1
	}
	if !utf8.FullRune(data) {
		return 0
	}
	_, n := utf8.DecodeRune(data)
	return n
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
