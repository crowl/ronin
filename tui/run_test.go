package tui

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/crowl/ronin/tui/internal/terminal"
)

func TestResolveTheme(t *testing.T) {
	explicitTheme := Theme{Text: TextTheme{Normal: Style{FG: "red"}}}

	tests := []struct {
		name       string
		configured Theme
		reader     *fakeBackgroundColorReader
		want       Theme
	}{
		{
			name:       "uses explicit theme",
			configured: explicitTheme,
			reader:     &fakeBackgroundColorReader{color: terminal.RGB{R: 255, G: 255, B: 255}, ok: true},
			want:       explicitTheme,
		},
		{
			name:   "uses light theme for light terminal background",
			reader: &fakeBackgroundColorReader{color: terminal.RGB{R: 255, G: 255, B: 255}, ok: true},
			want:   LightTheme(),
		},
		{
			name:   "uses dark theme for dark terminal background",
			reader: &fakeBackgroundColorReader{color: terminal.RGB{R: 0, G: 0, B: 0}, ok: true},
			want:   DarkTheme(),
		},
		{
			name:   "uses default theme when background is unavailable",
			reader: &fakeBackgroundColorReader{},
			want:   DefaultTheme(),
		},
		{
			name:   "uses default theme when background query fails",
			reader: &fakeBackgroundColorReader{err: errors.New("query failed")},
			want:   DefaultTheme(),
		},
		{
			name: "uses default theme when terminal is nil",
			want: DefaultTheme(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reader backgroundColorReader
			if tt.reader != nil {
				reader = tt.reader
			}

			got := resolveTheme(tt.configured, reader)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("theme\ngot:  %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}

func TestResolveThemeDoesNotQueryBackgroundForExplicitTheme(t *testing.T) {
	reader := &fakeBackgroundColorReader{color: terminal.RGB{R: 255, G: 255, B: 255}, ok: true}
	explicitTheme := Theme{Text: TextTheme{Normal: Style{FG: "red"}}}

	got := resolveTheme(explicitTheme, reader)
	if !reflect.DeepEqual(got, explicitTheme) {
		t.Fatalf("theme\ngot:  %#v\nwant: %#v", got, explicitTheme)
	}
	if reader.calls != 0 {
		t.Fatalf("background color queries\ngot:  %d\nwant: 0", reader.calls)
	}
}

func TestResolveThemeQueriesBackgroundWithTimeout(t *testing.T) {
	reader := &fakeBackgroundColorReader{color: terminal.RGB{R: 255, G: 255, B: 255}, ok: true}

	resolveTheme(Theme{}, reader)

	if reader.timeout != backgroundColorTimeout {
		t.Fatalf("timeout\ngot:  %s\nwant: %s", reader.timeout, backgroundColorTimeout)
	}
}

func TestTerminalColorIsLight(t *testing.T) {
	tests := []struct {
		name  string
		color terminal.RGB
		want  bool
	}{
		{name: "black", color: terminal.RGB{R: 0, G: 0, B: 0}, want: false},
		{name: "white", color: terminal.RGB{R: 255, G: 255, B: 255}, want: true},
		{name: "light gray", color: terminal.RGB{R: 248, G: 249, B: 250}, want: true},
		{name: "dark gray", color: terminal.RGB{R: 33, G: 37, B: 41}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := terminalColorIsLight(tt.color)
			if got != tt.want {
				t.Fatalf("is light\ngot:  %t\nwant: %t", got, tt.want)
			}
		})
	}
}

type fakeBackgroundColorReader struct {
	color   terminal.RGB
	ok      bool
	err     error
	calls   int
	timeout time.Duration
}

func (r *fakeBackgroundColorReader) BackgroundColor(timeout time.Duration) (terminal.RGB, bool, error) {
	r.calls++
	r.timeout = timeout
	return r.color, r.ok, r.err
}
