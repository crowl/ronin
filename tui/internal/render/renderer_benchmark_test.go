package render_test

import (
	"strconv"
	"testing"

	"github.com/crowl/ronin/tui/internal/render"
)

func BenchmarkRendererNoop(b *testing.B) {
	term := newVirtualTerminal(120, 40)
	renderer := newBenchmarkRenderer(b, term)
	lines := benchmarkLines(500, "Line ")

	if err := renderer.Render(render.Request{Lines: lines, Width: 120, Height: 40}); err != nil {
		b.Fatalf("render initial frame: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := renderer.Render(render.Request{Lines: lines, Width: 120, Height: 40}); err != nil {
			b.Fatalf("render unchanged frame: %v", err)
		}
	}
}

func BenchmarkRendererAppend(b *testing.B) {
	const maxLines = 512

	frames := make([][]string, maxLines)
	lines := benchmarkLines(maxLines, "Line ")
	for i := range frames {
		frames[i] = lines[:i+1]
	}

	term := newVirtualTerminal(120, 40)
	renderer := newBenchmarkRenderer(b, term)

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if i%maxLines == 0 {
			b.StopTimer()
			term = newVirtualTerminal(120, 40)
			renderer = newBenchmarkRenderer(b, term)
			b.StartTimer()
		}
		if err := renderer.Render(render.Request{Lines: frames[i%maxLines], Width: 120, Height: 40}); err != nil {
			b.Fatalf("render appended frame: %v", err)
		}
	}
}

func BenchmarkRendererPatchExistingLine(b *testing.B) {
	term := newVirtualTerminal(120, 40)
	renderer := newBenchmarkRenderer(b, term)
	lines := benchmarkLines(500, "Line ")
	variants := benchmarkLineVariants(1024, "Patched line ")

	if err := renderer.Render(render.Request{Lines: lines, Width: 120, Height: 40}); err != nil {
		b.Fatalf("render initial frame: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		lines[250] = variants[i%len(variants)]
		if err := renderer.Render(render.Request{Lines: lines, Width: 120, Height: 40}); err != nil {
			b.Fatalf("render patched frame: %v", err)
		}
	}
}

func BenchmarkRendererLargeHistoryChangeNearEnd(b *testing.B) {
	term := newVirtualTerminal(120, 40)
	renderer := newBenchmarkRenderer(b, term)
	lines := benchmarkLines(5000, "Line ")
	variants := benchmarkLineVariants(1024, "Last line update ")

	if err := renderer.Render(render.Request{Lines: lines, Width: 120, Height: 40}); err != nil {
		b.Fatalf("render initial frame: %v", err)
	}

	last := len(lines) - 1
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		lines[last] = variants[i%len(variants)]
		if err := renderer.Render(render.Request{Lines: lines, Width: 120, Height: 40}); err != nil {
			b.Fatalf("render last-line change: %v", err)
		}
	}
}

func newBenchmarkRenderer(b *testing.B, term *virtualTerminal) *render.Renderer {
	b.Helper()

	renderer, err := render.New(term)
	if err != nil {
		b.Fatalf("create renderer: %v", err)
	}
	return renderer
}

func benchmarkLines(count int, prefix string) []string {
	lines := make([]string, count)
	for i := range lines {
		lines[i] = prefix + strconv.Itoa(i)
	}
	return lines
}

func benchmarkLineVariants(count int, prefix string) []string {
	variants := make([]string, count)
	for i := range variants {
		variants[i] = prefix + strconv.Itoa(i)
	}
	return variants
}
