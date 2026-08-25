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
	t.Run("arguments and descriptions align to dynamic columns", func(t *testing.T) {
		p := menuPresenter{SelectedIndex: 0, Items: items}
		lines := p.Lines(120)
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

	t.Run("columns stay fixed when widest items scroll off screen", func(t *testing.T) {
		items := []menuItem{
			{Name: "/widest-command", Argument: "widest-argument-value", Value: "/widest-command widest-argument-value", Description: "widest command"},
			{Name: "/new", Value: "/new", Description: "start a fresh conversation"},
			{Name: "/model", Argument: "openai:gpt", Value: "/model openai:gpt", Description: "switch model"},
			{Name: "/exit", Value: "/exit", Description: "exit from ronin"},
			{Name: "/compact", Value: "/compact", Description: "compact conversation"},
			{Name: "/new", Value: "/new", Description: "start a fresh conversation"},
			{Name: "/short", Value: "/short", Description: "short command"},
		}

		p := menuPresenter{SelectedIndex: menuMaxVisibleItems, Items: items}
		lines := p.Lines(120)
		if len(lines) != menuMaxVisibleItems {
			t.Fatalf("line count\ngot:  %d\nwant: %d", len(lines), menuMaxVisibleItems)
		}

		wantArgumentColumn := len(menuItemPrefix) + text.VisibleLen("/widest-command") + menuDescriptionGap
		wantDescriptionColumn := wantArgumentColumn + text.VisibleLen("widest-argument-value") + menuDescriptionGap

		firstVisibleItem := items[1]
		firstVisibleLine := text.StripANSI(lines[0])
		descriptionColumn := text.VisibleLen(firstVisibleLine[:strings.Index(firstVisibleLine, firstVisibleItem.Description)])
		if descriptionColumn != wantDescriptionColumn {
			t.Fatalf("description column with widest item off screen\ngot:  %d\nwant: %d\nline: %q", descriptionColumn, wantDescriptionColumn, firstVisibleLine)
		}

		lastVisibleItem := items[menuMaxVisibleItems]
		lastVisibleLine := text.StripANSI(lines[len(lines)-1])
		descriptionColumn = text.VisibleLen(lastVisibleLine[:strings.Index(lastVisibleLine, lastVisibleItem.Description)])
		if descriptionColumn != wantDescriptionColumn {
			t.Fatalf("description column with widest item off screen\ngot:  %d\nwant: %d\nline: %q", descriptionColumn, wantDescriptionColumn, lastVisibleLine)
		}
	})

	t.Run("selected row uses reverse video", func(t *testing.T) {
		p := menuPresenter{SelectedIndex: 1, Items: items}
		lines := p.Lines(120)

		selected := lines[1]
		if !strings.Contains(selected, selectedStyle.start()) {
			t.Fatalf("selected row missing reverse style\nline: %q", selected)
		}
		if got := text.VisibleLen(text.StripANSI(selected)); got != 120 {
			t.Fatalf("selected row width\ngot:  %d\nwant: 120", got)
		}
	})

	t.Run("selected row shows the pointer prefix", func(t *testing.T) {
		p := menuPresenter{SelectedIndex: 2, Items: items}
		lines := p.Lines(120)
		if !strings.HasPrefix(text.StripANSI(lines[2]), menuItemPrefixSelected) {
			t.Fatalf("selected row missing pointer prefix\nline: %q", text.StripANSI(lines[2]))
		}
	})
}
