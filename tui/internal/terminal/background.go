package terminal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type RGB struct {
	R int
	G int
	B int
}

func (t *Terminal) BackgroundColor(timeout time.Duration) (RGB, bool, error) {
	if t.input == nil || t.output == nil {
		return RGB{}, false, nil
	}
	if timeout <= 0 {
		return RGB{}, false, nil
	}

	deadline := time.Now().Add(timeout)
	if err := t.input.SetReadDeadline(deadline); err != nil {
		return RGB{}, false, nil
	}
	defer func() { _ = t.input.SetReadDeadline(time.Time{}) }()

	if err := t.Write(BackgroundColorQuery); err != nil {
		return RGB{}, false, fmt.Errorf("query background color: %w", err)
	}

	var response []byte
	buf := make([]byte, 256)
	for {
		n, err := t.input.Read(buf)
		if n > 0 {
			response = append(response, buf[:n]...)
			if color, ok := parseBackgroundColorResponse(response); ok {
				return color, true, nil
			}
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return RGB{}, false, nil
			}
			return RGB{}, false, fmt.Errorf("read background color response: %w", err)
		}
	}
}

func parseBackgroundColorResponse(data []byte) (RGB, bool) {
	const prefix = Escape + "]11;"
	start := bytes.LastIndex(data, []byte(prefix))
	if start < 0 {
		return RGB{}, false
	}

	valueStart := start + len(prefix)
	valueEnd := -1
	if bel := bytes.IndexByte(data[valueStart:], '\a'); bel >= 0 {
		valueEnd = valueStart + bel
	}
	if st := bytes.Index(data[valueStart:], []byte(Escape+"\\")); st >= 0 && (valueEnd < 0 || valueStart+st < valueEnd) {
		valueEnd = valueStart + st
	}
	if valueEnd < 0 {
		return RGB{}, false
	}

	return parseBackgroundColorValue(string(data[valueStart:valueEnd]))
}

func parseBackgroundColorValue(value string) (RGB, bool) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "rgb:") {
		return parseRGBColor(strings.TrimPrefix(value, "rgb:"))
	}
	if strings.HasPrefix(value, "#") {
		return parseHexColor(strings.TrimPrefix(value, "#"))
	}
	return RGB{}, false
}

func parseRGBColor(value string) (RGB, bool) {
	parts := strings.Split(value, "/")
	if len(parts) != 3 {
		return RGB{}, false
	}

	r, ok := parseRGBComponent(parts[0])
	if !ok {
		return RGB{}, false
	}
	g, ok := parseRGBComponent(parts[1])
	if !ok {
		return RGB{}, false
	}
	b, ok := parseRGBComponent(parts[2])
	if !ok {
		return RGB{}, false
	}
	return RGB{R: r, G: g, B: b}, true
}

func parseHexColor(value string) (RGB, bool) {
	if len(value) != 6 {
		return RGB{}, false
	}
	r, ok := parseHexByte(value[0:2])
	if !ok {
		return RGB{}, false
	}
	g, ok := parseHexByte(value[2:4])
	if !ok {
		return RGB{}, false
	}
	b, ok := parseHexByte(value[4:6])
	if !ok {
		return RGB{}, false
	}
	return RGB{R: r, G: g, B: b}, true
}

func parseRGBComponent(value string) (int, bool) {
	if value == "" || len(value) > 4 {
		return 0, false
	}
	n, err := strconv.ParseUint(value, 16, 16)
	if err != nil {
		return 0, false
	}
	maxValue := (1 << (4 * len(value))) - 1
	return int((n*255 + uint64(maxValue)/2) / uint64(maxValue)), true
}

func parseHexByte(value string) (int, bool) {
	n, err := strconv.ParseUint(value, 16, 8)
	if err != nil {
		return 0, false
	}
	return int(n), true
}
