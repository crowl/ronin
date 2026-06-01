package text

import (
	"testing"
)

func TestText(t *testing.T) {
	t.Run("visible len ignores ansi escapes", func(t *testing.T) {
		got := VisibleLen("\x1b[1mhello\x1b[22m")
		if got != 5 {
			t.Fatalf("visible length\ngot:  %d\nwant: 5", got)
		}
	})

	t.Run("visible len expands tabs", func(t *testing.T) {
		got := VisibleLen("a\tb")
		if got != 5 {
			t.Fatalf("visible length\ngot:  %d\nwant: 5", got)
		}
	})

	t.Run("fill pads using visible length", func(t *testing.T) {
		got := Fill("\x1b[1mhi\x1b[22m", 4)
		if VisibleLen(got) != 4 {
			t.Fatalf("filled visible length\ngot:  %d\nwant: 4\nvalue: %q", VisibleLen(got), got)
		}
	})
}
