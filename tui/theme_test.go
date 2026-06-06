package tui_test

import (
	"os"
	"reflect"
	"strconv"
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
			name:  "hex foreground",
			style: tui.Style{FG: "#DEE2E6"},
			want:  "\x1b[38;2;222;226;230mx\x1b[0m",
		},
		{
			name:  "hex background",
			style: tui.Style{BG: "#212529"},
			want:  "\x1b[48;2;33;37;41mx\x1b[0m",
		},
		{
			name:  "invalid hex color ignored",
			style: tui.Style{FG: "#GGGGGG"},
			want:  "x",
		},
		{
			name:  "short hex color ignored",
			style: tui.Style{FG: "#FFF"},
			want:  "x",
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

func TestBuiltInThemesUsePaletteBaseColors(t *testing.T) {
	paletteColors := readPaletteColors(t)
	themes := map[string]tui.Theme{
		"default": tui.DefaultTheme(),
		"dark":    tui.DarkTheme(),
		"light":   tui.LightTheme(),
	}

	for name, theme := range themes {
		t.Run(name, func(t *testing.T) {
			assertMonochromeThemeColors(t, reflect.ValueOf(theme), "theme", paletteColors)
		})
	}
}

func TestDefaultThemeIsDarkTheme(t *testing.T) {
	if got, want := tui.DefaultTheme(), tui.DarkTheme(); !reflect.DeepEqual(got, want) {
		t.Fatalf("default theme should match dark theme")
	}
}

func TestBuiltInThemeLogicalColors(t *testing.T) {
	tests := []struct {
		name  string
		theme tui.Theme
		want  tui.Theme
	}{
		{
			name:  "dark",
			theme: tui.DarkTheme(),
			want: tui.Theme{
				Text: tui.TextTheme{
					Normal:   tui.Style{FG: "#E9ECEF"},
					Muted:    tui.Style{FG: "#CED4DA"},
					Strong:   tui.Style{FG: "#F8F9FA", Bold: true},
					Emphasis: tui.Style{FG: "#E9ECEF", Italic: true},
					Code:     tui.Style{FG: "#F8F9FA", Bold: true},
				},
				UI: tui.UITheme{
					EditorCursor:                tui.Style{Reverse: true},
					EditorSeparator:             tui.Style{FG: "#495057"},
					WorkingIndicator:            tui.Style{FG: "#CED4DA"},
					MenuItem:                    tui.Style{FG: "#E9ECEF"},
					MenuItemSelected:            tui.Style{FG: "#F8F9FA", BG: "#495057", Bold: true},
					MenuItemDescription:         tui.Style{FG: "#CED4DA"},
					MenuItemDescriptionSelected: tui.Style{FG: "#E9ECEF"},
					StatusBar:                   tui.Style{FG: "#CED4DA"},
				},
				Box: tui.BoxTheme{
					User: tui.BoxStyle{
						Container: tui.Style{FG: "#F8F9FA", BG: "#343A40"},
					},
					Assistant: tui.BoxStyle{
						Container: tui.Style{FG: "#E9ECEF"},
					},
					AssistantThinking: tui.BoxStyle{
						Container: tui.Style{FG: "#CED4DA"},
						Body:      tui.Style{FG: "#CED4DA", Italic: true},
						Strong:    tui.Style{FG: "#E9ECEF", Bold: true},
						Emphasis:  tui.Style{FG: "#CED4DA", Italic: true},
						Code:      tui.Style{FG: "#E9ECEF", Bold: true},
					},
					ToolCall: tui.BoxStyle{
						Container:   tui.Style{FG: "#E9ECEF", BG: "#212529"},
						Title:       tui.Style{FG: "#F8F9FA", Bold: true},
						Meta:        tui.Style{FG: "#CED4DA"},
						Muted:       tui.Style{FG: "#CED4DA"},
						DiffAdded:   tui.Style{FG: "#E9ECEF", BG: "color-22"},
						DiffRemoved: tui.Style{FG: "#E9ECEF", BG: "color-52"},
					},
					System: tui.BoxStyle{
						Container: tui.Style{FG: "#CED4DA"},
						Muted:     tui.Style{FG: "#CED4DA"},
					},
					Error: tui.BoxStyle{
						Container: tui.Style{FG: "color-203"},
					},
				},
			},
		},
		{
			name:  "light",
			theme: tui.LightTheme(),
			want: tui.Theme{
				Text: tui.TextTheme{
					Normal:   tui.Style{FG: "#343A40"},
					Muted:    tui.Style{FG: "#6C757D"},
					Strong:   tui.Style{FG: "#212529", Bold: true},
					Emphasis: tui.Style{FG: "#343A40", Italic: true},
					Code:     tui.Style{FG: "#212529", Bold: true},
				},
				UI: tui.UITheme{
					EditorCursor:                tui.Style{Reverse: true},
					EditorSeparator:             tui.Style{FG: "#ADB5BD"},
					WorkingIndicator:            tui.Style{FG: "#6C757D"},
					MenuItem:                    tui.Style{FG: "#343A40"},
					MenuItemSelected:            tui.Style{FG: "#212529", BG: "#DEE2E6", Bold: true},
					MenuItemDescription:         tui.Style{FG: "#6C757D"},
					MenuItemDescriptionSelected: tui.Style{FG: "#343A40"},
					StatusBar:                   tui.Style{FG: "#6C757D"},
				},
				Box: tui.BoxTheme{
					User: tui.BoxStyle{
						Container: tui.Style{FG: "#212529", BG: "#E9ECEF"},
					},
					Assistant: tui.BoxStyle{
						Container: tui.Style{FG: "#343A40"},
					},
					AssistantThinking: tui.BoxStyle{
						Container: tui.Style{FG: "#6C757D"},
						Body:      tui.Style{FG: "#6C757D", Italic: true},
						Strong:    tui.Style{FG: "#343A40", Bold: true},
						Emphasis:  tui.Style{FG: "#6C757D", Italic: true},
						Code:      tui.Style{FG: "#343A40", Bold: true},
					},
					ToolCall: tui.BoxStyle{
						Container:   tui.Style{FG: "#343A40", BG: "#F8F9FA"},
						Title:       tui.Style{FG: "#212529", Bold: true},
						Meta:        tui.Style{FG: "#6C757D"},
						Muted:       tui.Style{FG: "#6C757D"},
						DiffAdded:   tui.Style{FG: "#343A40", BG: "color-194"},
						DiffRemoved: tui.Style{FG: "#343A40", BG: "color-224"},
					},
					System: tui.BoxStyle{
						Container: tui.Style{FG: "#6C757D"},
						Muted:     tui.Style{FG: "#6C757D"},
					},
					Error: tui.BoxStyle{
						Container: tui.Style{FG: "color-124"},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.theme, tt.want) {
				t.Fatalf("theme colors\ngot:  %#v\nwant: %#v", tt.theme, tt.want)
			}
		})
	}
}

func TestBuiltInThemeSurfacePolarity(t *testing.T) {
	tests := []struct {
		name              string
		theme             tui.Theme
		wantLightSurfaces bool
	}{
		{name: "dark", theme: tui.DarkTheme(), wantLightSurfaces: false},
		{name: "light", theme: tui.LightTheme(), wantLightSurfaces: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertSurfacePolarity(t, "user", tt.theme.Box.User.Container.BG, tt.wantLightSurfaces)
			assertSurfacePolarity(t, "tool", tt.theme.Box.ToolCall.Container.BG, tt.wantLightSurfaces)
			assertSurfacePolarity(t, "selected menu", tt.theme.UI.MenuItemSelected.BG, tt.wantLightSurfaces)
		})
	}
}

func TestBuiltInThemeForegroundBackgroundContrast(t *testing.T) {
	themes := map[string]tui.Theme{
		"dark":  tui.DarkTheme(),
		"light": tui.LightTheme(),
	}

	for name, theme := range themes {
		t.Run(name, func(t *testing.T) {
			assertStyleContrast(t, "user", theme.Box.User.Container)
			assertStyleContrast(t, "tool", theme.Box.ToolCall.Container)
			assertStyleContrast(t, "selected menu", theme.UI.MenuItemSelected)
		})
	}
}

func TestAssistantThemesDoNotSetBackgrounds(t *testing.T) {
	themes := map[string]tui.Theme{
		"dark":  tui.DarkTheme(),
		"light": tui.LightTheme(),
	}

	for name, theme := range themes {
		t.Run(name, func(t *testing.T) {
			assertBoxHasForegroundOnly(t, "Assistant", theme.Box.Assistant, theme.Text)
			assertBoxHasForegroundOnly(t, "AssistantThinking", theme.Box.AssistantThinking, theme.Text)
			assertBoxHasForegroundOnly(t, "System", theme.Box.System, theme.Text)
			assertBoxHasForegroundOnly(t, "Error", theme.Box.Error, theme.Text)
		})
	}
}

func assertBoxHasForegroundOnly(t *testing.T, name string, box tui.BoxStyle, fallback tui.TextTheme) {
	t.Helper()

	if box.Container.FG == "" {
		t.Fatalf("%s container has no foreground", name)
	}
	if box.Container.BG != "" {
		t.Fatalf("%s container has background: %q", name, box.Container.BG)
	}
	textTheme := box.TextTheme(fallback)
	if textTheme.Normal.FG == "" {
		t.Fatalf("%s normal text has no foreground", name)
	}
	if textTheme.Normal.BG != "" {
		t.Fatalf("%s normal text has background: %q", name, textTheme.Normal.BG)
	}
}

func assertSurfacePolarity(t *testing.T, name string, color tui.Color, wantLight bool) {
	t.Helper()

	light, ok := hexColorIsLight(color)
	if !ok {
		t.Fatalf("%s surface is not a hex color: %q", name, color)
	}
	if light != wantLight {
		t.Fatalf("%s surface polarity\ngot light:  %t\nwant light: %t\ncolor:      %q", name, light, wantLight, color)
	}
}

func assertStyleContrast(t *testing.T, name string, style tui.Style) {
	t.Helper()

	fg, ok := hexLuminance(style.FG)
	if !ok {
		t.Fatalf("%s foreground is not a hex color: %q", name, style.FG)
	}
	bg, ok := hexLuminance(style.BG)
	if !ok {
		t.Fatalf("%s background is not a hex color: %q", name, style.BG)
	}
	if diff := absFloat(fg - bg); diff < 100 {
		t.Fatalf("%s contrast is too low: fg=%q bg=%q luminance diff=%.1f", name, style.FG, style.BG, diff)
	}
}

func hexColorIsLight(color tui.Color) (bool, bool) {
	luminance, ok := hexLuminance(color)
	return luminance >= 128, ok
}

func hexLuminance(color tui.Color) (float64, bool) {
	r, g, b, ok := parseHexColor(color)
	if !ok {
		return 0, false
	}
	return 0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b), true
}

func parseHexColor(color tui.Color) (int, int, int, bool) {
	value := string(color)
	if len(value) != 7 || value[0] != '#' {
		return 0, 0, 0, false
	}
	r, ok := parseHexByte(value[1:3])
	if !ok {
		return 0, 0, 0, false
	}
	g, ok := parseHexByte(value[3:5])
	if !ok {
		return 0, 0, 0, false
	}
	b, ok := parseHexByte(value[5:7])
	if !ok {
		return 0, 0, 0, false
	}
	return r, g, b, true
}

func parseHexByte(value string) (int, bool) {
	n, err := strconv.ParseUint(value, 16, 8)
	if err != nil {
		return 0, false
	}
	return int(n), true
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func readPaletteColors(t *testing.T) map[tui.Color]bool {
	t.Helper()

	data, err := os.ReadFile("../dev/palette")
	if err != nil {
		t.Fatalf("read dev palette: %v", err)
	}

	colors := make(map[tui.Color]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		color := tui.Color("#" + strings.TrimSpace(line))
		colors[color] = true
	}
	return colors
}

func assertMonochromeThemeColors(t *testing.T, value reflect.Value, path string, paletteColors map[tui.Color]bool) {
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
			assertMonochromeThemeColor(t, path+".FG", style.FG, paletteColors)
			assertMonochromeThemeColor(t, path+".BG", style.BG, paletteColors)
			return
		}
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			assertMonochromeThemeColors(t, value.Field(i), path+"."+field.Name, paletteColors)
		}
	}
}

func assertMonochromeThemeColor(t *testing.T, path string, color tui.Color, paletteColors map[tui.Color]bool) {
	t.Helper()

	if color == "" {
		return
	}
	if isSemanticAccentPath(path) {
		if !isSemanticAccentColor(path, color) {
			t.Fatalf("%s uses unexpected semantic accent color: %q", path, color)
		}
		return
	}
	if !paletteColors[color] {
		t.Fatalf("%s uses color outside monochrome palette: %q", path, color)
	}
}

func isSemanticAccentPath(path string) bool {
	switch path {
	case "theme.Box.ToolCall.DiffAdded.FG",
		"theme.Box.ToolCall.DiffAdded.BG",
		"theme.Box.ToolCall.DiffRemoved.FG",
		"theme.Box.ToolCall.DiffRemoved.BG",
		"theme.Box.Error.Container.FG":
		return true
	default:
		return false
	}
}

func isSemanticAccentColor(path string, color tui.Color) bool {
	switch path {
	case "theme.Box.ToolCall.DiffAdded.FG":
		return color == "#E9ECEF" || color == "#343A40"
	case "theme.Box.ToolCall.DiffAdded.BG":
		return color == "color-22" || color == "color-194"
	case "theme.Box.ToolCall.DiffRemoved.FG":
		return color == "#E9ECEF" || color == "#343A40"
	case "theme.Box.ToolCall.DiffRemoved.BG":
		return color == "color-52" || color == "color-224"
	case "theme.Box.Error.Container.FG":
		return color == "color-124" || color == "color-203"
	default:
		return false
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
