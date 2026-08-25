package tui

import (
	"fmt"
	"strings"

	"github.com/crowl/ronin/tui/internal/terminal"
)

type style struct {
	color   string
	bold    bool
	italic  bool
	reverse bool
}

var (
	strongStyle   = style{bold: true}
	emphasisStyle = style{italic: true}
	cursorStyle   = style{reverse: true}
	selectedStyle = style{reverse: true}
	errorStyle    = style{color: "red"}
	addedStyle    = style{color: "green"}
	removedStyle  = style{color: "red"}
)

type textStyles struct {
	normal   style
	muted    style
	strong   style
	emphasis style
	code     style
}

func defaultTextStyles() textStyles {
	return textStyles{
		strong:   strongStyle,
		emphasis: emphasisStyle,
		code:     strongStyle,
	}
}

func thinkingTextStyles() textStyles {
	return textStyles{
		normal:   emphasisStyle,
		muted:    emphasisStyle,
		strong:   style{bold: true, italic: true},
		emphasis: emphasisStyle,
		code:     style{bold: true, italic: true},
	}
}

func (s style) apply(value string) string {
	start := s.start()
	if start == "" {
		return value
	}
	return start + value + terminal.SGRReset
}

func (s style) start() string {
	var params []string
	if s.bold {
		params = append(params, "1")
	}
	if s.italic {
		params = append(params, "3")
	}
	if s.reverse {
		params = append(params, "7")
	}
	switch s.color {
	case "red":
		params = append(params, "31")
	case "green":
		params = append(params, "32")
	}
	if len(params) == 0 {
		return ""
	}
	return fmt.Sprintf(terminal.SGRFormat, strings.Join(params, ";"))
}

func (s style) empty() bool {
	return s.color == "" && !s.bold && !s.italic && !s.reverse
}
