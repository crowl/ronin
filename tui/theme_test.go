package tui_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/tui"
)

func TestStyle(t *testing.T) {
	tests := []struct {
		name  string
		style tui.Style
		want  string
	}{
		{
			name:  "empty",
			style: tui.Style{},
			want:  "x",
		},
		{
			name:  "named foreground",
			style: tui.Style{FG: "red"},
			want:  "\x1b[31mx\x1b[0m",
		},
		{
			name:  "bright foreground",
			style: tui.Style{FG: "brightWhite"},
			want:  "\x1b[97mx\x1b[0m",
		},
		{
			name:  "named background",
			style: tui.Style{BG: "blue"},
			want:  "\x1b[44mx\x1b[0m",
		},
		{
			name:  "bright background",
			style: tui.Style{BG: "brightBlue"},
			want:  "\x1b[104mx\x1b[0m",
		},
		{
			name:  "256 foreground",
			style: tui.Style{FG: "color-234"},
			want:  "\x1b[38;5;234mx\x1b[0m",
		},
		{
			name:  "256 background",
			style: tui.Style{BG: "color-234"},
			want:  "\x1b[48;5;234mx\x1b[0m",
		},
		{
			name:  "combined attributes",
			style: tui.Style{FG: "brightWhite", BG: "color-234", Bold: true, Italic: true, Reverse: true},
			want:  "\x1b[1;3;7;97;48;5;234mx\x1b[0m",
		},
		{
			name:  "invalid color ignored",
			style: tui.Style{FG: "nope"},
			want:  "x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.style.Apply("x")
			if got != tt.want {
				t.Fatalf("style apply\ngot:  %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestBuiltInThemesUseTerminalPalette(t *testing.T) {
	themes := map[string]tui.Theme{
		"default": tui.DefaultTheme(),
		"dark":    tui.DarkTheme(),
		"light":   tui.LightTheme(),
	}

	for name, theme := range themes {
		t.Run(name, func(t *testing.T) {
			assertNoFixedPaletteColors(t, reflect.ValueOf(theme), "theme")
		})
	}
}

func assertNoFixedPaletteColors(t *testing.T, value reflect.Value, path string) {
	t.Helper()

	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}

	switch value.Kind() {
	case reflect.Struct:
		if style, ok := value.Interface().(tui.Style); ok {
			if strings.HasPrefix(string(style.FG), "color-") {
				t.Fatalf("%s.FG uses fixed 256-color palette: %q", path, style.FG)
			}
			if strings.HasPrefix(string(style.BG), "color-") {
				t.Fatalf("%s.BG uses fixed 256-color palette: %q", path, style.BG)
			}
			return
		}
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			assertNoFixedPaletteColors(t, value.Field(i), path+"."+field.Name)
		}
	}
}

func TestBoxStyleTextTheme(t *testing.T) {
	fallback := tui.TextTheme{
		Normal:   tui.Style{FG: "white"},
		Strong:   tui.Style{Bold: true},
		Emphasis: tui.Style{Italic: true},
	}
	box := tui.BoxStyle{
		Body:     tui.Style{BG: "color-234"},
		Strong:   tui.Style{FG: "brightWhite"},
		Emphasis: tui.Style{FG: "brightBlue"},
	}

	got := box.TextTheme(fallback)
	if got.Normal.Apply("x") != "\x1b[37;48;5;234mx\x1b[0m" {
		t.Fatalf("normal style\ngot: %q", got.Normal.Apply("x"))
	}
	if got.Strong.Apply("x") != "\x1b[1;97;48;5;234mx\x1b[0m" {
		t.Fatalf("strong style\ngot: %q", got.Strong.Apply("x"))
	}
	if got.Emphasis.Apply("x") != "\x1b[3;94;48;5;234mx\x1b[0m" {
		t.Fatalf("emphasis style\ngot: %q", got.Emphasis.Apply("x"))
	}
}
