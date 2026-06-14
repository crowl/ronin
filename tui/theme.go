package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/crowl/ronin/tui/internal/terminal"
)

type Color string

type Style struct {
	FG      Color `json:"fg,omitempty"`
	BG      Color `json:"bg,omitempty"`
	Bold    bool  `json:"bold,omitempty"`
	Italic  bool  `json:"italic,omitempty"`
	Reverse bool  `json:"reverse,omitempty"`
}

type Theme struct {
	Text TextTheme `json:"text,omitempty"`
	UI   UITheme   `json:"ui,omitempty"`
	Box  BoxTheme  `json:"box,omitempty"`
}

type TextTheme struct {
	Normal   Style `json:"normal,omitempty"`
	Muted    Style `json:"muted,omitempty"`
	Strong   Style `json:"strong,omitempty"`
	Emphasis Style `json:"emphasis,omitempty"`
	Code     Style `json:"code,omitempty"`
}

type UITheme struct {
	EditorCursor                Style `json:"editorCursor,omitempty"`
	EditorSeparator             Style `json:"editorSeparator,omitempty"`
	WorkingIndicator            Style `json:"workingIndicator,omitempty"`
	MenuItem                    Style `json:"menuItem,omitempty"`
	MenuItemSelected            Style `json:"menuItemSelected,omitempty"`
	MenuItemDescription         Style `json:"menuItemDescription,omitempty"`
	MenuItemDescriptionSelected Style `json:"menuItemDescriptionSelected,omitempty"`
	StatusBar                   Style `json:"statusBar,omitempty"`
}

type BoxTheme struct {
	User              BoxStyle `json:"user,omitempty"`
	Assistant         BoxStyle `json:"assistant,omitempty"`
	AssistantThinking BoxStyle `json:"assistantThinking,omitempty"`
	ToolCall          BoxStyle `json:"toolCall,omitempty"`
	System            BoxStyle `json:"system,omitempty"`
	Error             BoxStyle `json:"error,omitempty"`
}

type BoxStyle struct {
	Container   Style `json:"container,omitempty"`
	Body        Style `json:"body,omitempty"`
	Title       Style `json:"title,omitempty"`
	Meta        Style `json:"meta,omitempty"`
	Muted       Style `json:"muted,omitempty"`
	Strong      Style `json:"strong,omitempty"`
	Emphasis    Style `json:"emphasis,omitempty"`
	Code        Style `json:"code,omitempty"`
	DiffAdded   Style `json:"diff_added,omitempty"`
	DiffRemoved Style `json:"diff_removed,omitempty"`
}

func (t Theme) Empty() bool {
	return t.Text == TextTheme{} && t.UI == UITheme{} && t.Box == BoxTheme{}
}

func DefaultTheme() Theme {
	return DarkTheme()
}

const (
	paletteLightest Color = "#F8F9FA"
	paletteLighter  Color = "#E9ECEF"
	paletteLight    Color = "#DEE2E6"
	paletteSoft     Color = "#CED4DA"
	paletteMid      Color = "#ADB5BD"
	paletteMuted    Color = "#6C757D"
	paletteDim      Color = "#495057"
	paletteDark     Color = "#343A40"
	paletteDarkest  Color = "#212529"

	darkGreen  Color = "#238636"
	darkRed    Color = "#F85149"
	lightGreen Color = "#2DA44E"
	lightRed   Color = "#CF222E"
)

type themeColors struct {
	Text            Color
	TextMuted       Color
	TextStrong      Color
	TextThinking    Color
	Surface         Color
	SurfaceRaised   Color
	SurfaceSelected Color
	Separator       Color
	AccentText      Color
	Green           Color
	Red             Color
}

func DarkTheme() Theme {
	return themeFromColors(themeColors{
		Text:            paletteLighter,
		TextMuted:       paletteMuted,
		TextStrong:      paletteLightest,
		TextThinking:    paletteMid,
		Surface:         paletteDarkest,
		SurfaceRaised:   paletteDark,
		SurfaceSelected: paletteDim,
		Separator:       paletteMid,
		AccentText:      paletteLightest,
		Green:           darkGreen,
		Red:             darkRed,
	})
}

func LightTheme() Theme {
	return themeFromColors(themeColors{
		Text:            paletteDark,
		TextMuted:       paletteMuted,
		TextStrong:      paletteDarkest,
		TextThinking:    paletteMid,
		Surface:         paletteLightest,
		SurfaceRaised:   paletteLighter,
		SurfaceSelected: paletteLight,
		Separator:       paletteMid,
		AccentText:      paletteLightest,
		Green:           lightGreen,
		Red:             lightRed,
	})
}

func themeFromColors(c themeColors) Theme {
	return Theme{
		Text: TextTheme{
			Normal:   Style{FG: c.Text},
			Muted:    Style{FG: c.TextMuted},
			Strong:   Style{FG: c.TextStrong, Bold: true},
			Emphasis: Style{FG: c.Text, Italic: true},
			Code:     Style{FG: c.TextStrong, Bold: true},
		},
		UI: UITheme{
			EditorCursor:                Style{Reverse: true},
			EditorSeparator:             Style{FG: c.Separator},
			WorkingIndicator:            Style{FG: c.TextMuted},
			MenuItem:                    Style{FG: c.Text},
			MenuItemSelected:            Style{FG: c.TextStrong, BG: c.SurfaceSelected, Bold: true},
			MenuItemDescription:         Style{FG: c.TextMuted},
			MenuItemDescriptionSelected: Style{FG: c.Text},
			StatusBar:                   Style{FG: c.TextMuted},
		},
		Box: BoxTheme{
			User: BoxStyle{
				Container: Style{FG: c.TextStrong, BG: c.SurfaceRaised},
			},
			Assistant: BoxStyle{
				Container: Style{FG: c.Text},
			},
			AssistantThinking: BoxStyle{
				Container: Style{FG: c.TextThinking},
				Body:      Style{FG: c.TextThinking, Italic: true},
				Strong:    Style{FG: c.TextThinking, Bold: true},
				Emphasis:  Style{FG: c.TextThinking, Italic: true},
				Code:      Style{FG: c.Text, Bold: true},
			},
			ToolCall: BoxStyle{
				Container:   Style{FG: c.Text, BG: c.Surface},
				Title:       Style{FG: c.TextStrong, Bold: true},
				Meta:        Style{FG: c.TextMuted},
				Muted:       Style{FG: c.TextMuted},
				DiffAdded:   Style{FG: c.AccentText, BG: c.Green},
				DiffRemoved: Style{FG: c.AccentText, BG: c.Red},
			},
			System: BoxStyle{
				Container: Style{FG: c.TextMuted},
				Muted:     Style{FG: c.TextMuted},
			},
			Error: BoxStyle{
				Container: Style{FG: c.Red},
			},
		},
	}
}

func (s Style) Apply(v string) string {
	start := s.Start()
	if start == "" {
		return v
	}
	return start + v + terminal.SGRReset
}

func (s Style) Start() string {
	params := s.sgrParams()
	if len(params) == 0 {
		return ""
	}
	return fmt.Sprintf(terminal.SGRFormat, strings.Join(params, ";"))
}

func (s Style) Empty() bool {
	return s.FG == "" && s.BG == "" && !s.Bold && !s.Italic && !s.Reverse
}

func (s Style) Merge(overlay Style) Style {
	if overlay.FG != "" {
		s.FG = overlay.FG
	}
	if overlay.BG != "" {
		s.BG = overlay.BG
	}
	s.Bold = s.Bold || overlay.Bold
	s.Italic = s.Italic || overlay.Italic
	s.Reverse = s.Reverse || overlay.Reverse
	return s
}

func (b BoxStyle) ApplyBody(v string) string {
	return b.Container.Merge(b.Body).Apply(v)
}

func (b BoxStyle) ApplyTitle(v string) string {
	return b.Container.Merge(b.Title).Apply(v)
}

func (b BoxStyle) ApplyMeta(v string) string {
	return b.Container.Merge(b.Meta).Apply(v)
}

func (b BoxStyle) ApplyMuted(v string) string {
	return b.Container.Merge(b.Muted).Apply(v)
}

func (b BoxStyle) ApplyDiffAdded(v string) string {
	return b.Container.Merge(b.DiffAdded).Apply(v)
}

func (b BoxStyle) ApplyDiffRemoved(v string) string {
	return b.Container.Merge(b.DiffRemoved).Apply(v)
}

func (b BoxStyle) TextTheme(fallback TextTheme) TextTheme {
	base := b.Container.Merge(b.Body)
	return TextTheme{
		Normal:   fallback.Normal.Merge(base),
		Muted:    fallback.Muted.Merge(base).Merge(b.Muted),
		Strong:   fallback.Strong.Merge(base).Merge(b.Strong),
		Emphasis: fallback.Emphasis.Merge(base).Merge(b.Emphasis),
		Code:     fallback.Code.Merge(base).Merge(b.Code),
	}
}

func (s Style) sgrParams() []string {
	var params []string
	if s.Bold {
		params = append(params, "1")
	}
	if s.Italic {
		params = append(params, "3")
	}
	if s.Reverse {
		params = append(params, "7")
	}
	if fg, ok := sgrColorParams(s.FG, false); ok {
		params = append(params, fg...)
	}
	if bg, ok := sgrColorParams(s.BG, true); ok {
		params = append(params, bg...)
	}
	return params
}

func sgrColorParams(color Color, background bool) ([]string, bool) {
	name := string(color)
	if name == "" {
		return nil, false
	}
	if strings.HasPrefix(name, "color-") {
		n, err := strconv.Atoi(strings.TrimPrefix(name, "color-"))
		if err != nil || n < 0 || n > 255 {
			return nil, false
		}
		if background {
			return []string{"48", "5", strconv.Itoa(n)}, true
		}
		return []string{"38", "5", strconv.Itoa(n)}, true
	}

	if strings.HasPrefix(name, "#") {
		r, g, b, ok := hexColor(name)
		if !ok {
			return nil, false
		}
		if background {
			return []string{"48", "2", strconv.Itoa(r), strconv.Itoa(g), strconv.Itoa(b)}, true
		}
		return []string{"38", "2", strconv.Itoa(r), strconv.Itoa(g), strconv.Itoa(b)}, true
	}

	base, ok := namedColorCode(name)
	if !ok {
		return nil, false
	}
	if background {
		if base >= 90 {
			return []string{strconv.Itoa(base + 10)}, true
		}
		return []string{strconv.Itoa(base + 10)}, true
	}
	return []string{strconv.Itoa(base)}, true
}

func hexColor(name string) (int, int, int, bool) {
	if len(name) != 7 {
		return 0, 0, 0, false
	}

	r, ok := hexByte(name[1:3])
	if !ok {
		return 0, 0, 0, false
	}
	g, ok := hexByte(name[3:5])
	if !ok {
		return 0, 0, 0, false
	}
	b, ok := hexByte(name[5:7])
	if !ok {
		return 0, 0, 0, false
	}
	return r, g, b, true
}

func hexByte(s string) (int, bool) {
	n, err := strconv.ParseUint(s, 16, 8)
	if err != nil {
		return 0, false
	}
	return int(n), true
}

func namedColorCode(name string) (int, bool) {
	switch name {
	case "black":
		return 30, true
	case "red":
		return 31, true
	case "green":
		return 32, true
	case "yellow":
		return 33, true
	case "blue":
		return 34, true
	case "magenta":
		return 35, true
	case "cyan":
		return 36, true
	case "white":
		return 37, true
	case "brightBlack", "bright-black":
		return 90, true
	case "brightRed", "bright-red":
		return 91, true
	case "brightGreen", "bright-green":
		return 92, true
	case "brightYellow", "bright-yellow":
		return 93, true
	case "brightBlue", "bright-blue":
		return 94, true
	case "brightMagenta", "bright-magenta":
		return 95, true
	case "brightCyan", "bright-cyan":
		return 96, true
	case "brightWhite", "bright-white":
		return 97, true
	default:
		return 0, false
	}
}
