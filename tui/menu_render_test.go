package tui

import (
	"strings"
	"testing"

	"github.com/crowl/ronin/tui/internal/text"
)

func TestMenuPresenterLines(t *testing.T) {
	items := []menuItem{
		{Name: "/new", Value: "/new", Description: "start a fresh conversation"},
		{Name: "/model", Argument: "openai:gpt-some-long-name", Value: "/model openai:gpt-some-long-name", Description: "switch model"},
		{Name: "/reasoning", Argument: "low", Value: "/reasoning low", Description: "set reasoning level"},
		{Name: "/exit", Value: "/exit", Description: "exit from ronin"},
	}
	theme := DefaultTheme()

	t.Run("arguments and descriptions align to dynamic columns", func(t *testing.T) {
		p := menuPresenter{SelectedIndex: 0, Items: items}
		lines := p.Lines(120, theme)
		if len(lines) != len(items) {
			t.Fatalf("line count\ngot:  %d\nwant: %d", len(lines), len(items))
		}

		nameWidth := text.VisibleLen("/reasoning")
		argumentWidth := text.VisibleLen("openai:gpt-some-long-name")
		wantArgumentColumn := len(menuItemPrefix) + nameWidth + menuDescriptionGap
		wantDescriptionColumn := wantArgumentColumn + argumentWidth + menuDescriptionGap

		for i, line := range lines {
			plain := text.StripANSI(line)
			if items[i].Argument != "" {
				argumentColumn := text.VisibleLen(plain[:strings.Index(plain, items[i].Argument)])
				if argumentColumn != wantArgumentColumn {
					t.Fatalf("argument column for %q\ngot:  %d\nwant: %d\nline: %q",
						items[i].Value, argumentColumn, wantArgumentColumn, plain)
				}
			}

			descriptionColumn := text.VisibleLen(plain[:strings.Index(plain, items[i].Description)])
			if descriptionColumn != wantDescriptionColumn {
				t.Fatalf("description column for %q\ngot:  %d\nwant: %d\nline: %q",
					items[i].Value, descriptionColumn, wantDescriptionColumn, plain)
			}
		}
	})

	t.Run("selected row uses a background highlight, not reverse", func(t *testing.T) {
		p := menuPresenter{SelectedIndex: 1, Items: items}
		lines := p.Lines(120, theme)

		selected := lines[1]
		if !strings.Contains(selected, theme.UI.MenuItemSelected.Start()) {
			t.Fatalf("selected row missing highlight style\nline: %q", selected)
		}
		if strings.Contains(selected, ";7;") || strings.Contains(selected, "[7m") {
			t.Fatalf("selected row should not use reverse video\nline: %q", selected)
		}
		if got := text.VisibleLen(text.StripANSI(selected)); got != 120 {
			t.Fatalf("selected row width\ngot:  %d\nwant: 120", got)
		}
	})

	t.Run("selected row shows the pointer prefix", func(t *testing.T) {
		p := menuPresenter{SelectedIndex: 2, Items: items}
		lines := p.Lines(120, theme)
		if !strings.HasPrefix(text.StripANSI(lines[2]), menuItemPrefixSelected) {
			t.Fatalf("selected row missing pointer prefix\nline: %q", text.StripANSI(lines[2]))
		}
	})
}
