package syntax_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	extract "github.com/crowl/ronin/codeindex/syntax"
)

func TestExtract(t *testing.T) {
	for _, tt := range []struct {
		language string
		source   string
		names    string
	}{
		{"go", "package p\n// Store holds data.\ntype Store struct { Name string }\ntype ID = string\nfunc New() *Store { return nil }\nfunc (s *Store) Validate() error { local := func() {}; _ = local; return nil }\n", "Store,ID,New,Validate"},
		{"typescript", "export interface Store { validate(): boolean; }\nexport type ID = string;\nexport class Memory { validate(): boolean { return true; } }\nexport function create(): Store { function local() {} return new Memory(); }\nexport const check = (s: Store): boolean => s.validate();\n", "Store,validate,ID,Memory,validate,create,check"},
		{"tsx", "interface Props { label: string }\nexport const Button = ({label}: Props) => <button>{label}</button>;\nexport default function App() { return <Button label=\"Hi\" />; }\n", "Props,Button,App"},
	} {
		t.Run(tt.language, func(t *testing.T) {
			e := newExtractor(t, tt.language)
			out, err := e.Extract(t.Context(), []byte(tt.source))
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Diagnostics) != 0 {
				t.Fatalf("unexpected diagnostics: %+v", out.Diagnostics)
			}
			var names []string
			for _, d := range out.Declarations {
				names = append(names, d.Name)
				if d.Span.EndByte > uint(len(tt.source)) || d.Span.StartByte >= d.Span.EndByte {
					t.Fatalf("invalid span: %+v", d)
				}
				if !strings.Contains(tt.source[d.Span.StartByte:d.Span.EndByte], d.Name) {
					t.Fatalf("span does not include name: %+v", d)
				}
				if strings.Contains(d.Signature, "return") || strings.Contains(d.Signature, "<button>") {
					t.Fatalf("body in signature: %+v", d)
				}
				if d.Name == "Validate" && d.Container != "(s *Store)" {
					t.Fatalf("receiver: %+v", d)
				}
				if d.Name == "validate" && d.Container != "Store" && d.Container != "Memory" {
					t.Fatalf("container: %+v", d)
				}
			}
			if got := strings.Join(names, ","); got != tt.names {
				t.Fatalf("names = %s, want %s", got, tt.names)
			}
			if len(out.SHA256) != 64 {
				t.Fatalf("invalid SHA256: %s", out.SHA256)
			}
		})
	}
}

func TestIncompleteSource(t *testing.T) {
	for _, tt := range []struct{ language, source string }{
		{"go", "package p\nfunc Good() {}\nfunc Broken( {"},
		{"typescript", "export function Good() {}\nexport const broken = ("},
		{"tsx", "export function Good() { return <div/>; }\nconst broken = <"},
	} {
		t.Run(tt.language, func(t *testing.T) {
			e := newExtractor(t, tt.language)
			out, err := e.Extract(t.Context(), []byte(tt.source))
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Diagnostics) == 0 {
				t.Fatal("expected parse diagnostics")
			}
			for _, d := range out.Declarations {
				if d.Name == "Good" {
					return
				}
			}
			t.Fatalf("lost intact declaration: %+v", out)
		})
	}
}

func TestMissingTokenAndUnicodeRanges(t *testing.T) {
	e := newExtractor(t, "go")
	source := []byte("package p\n// café\nfunc Café() {\n return\n")
	out, err := e.Extract(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Declarations) != 1 {
		t.Fatalf("declarations: %+v", out)
	}
	d := out.Declarations[0]
	if d.Name != "Café" || d.Span.StartLine != 3 || d.Span.EndLine != 4 {
		t.Fatalf("unicode/line range: %+v", d)
	}
	for _, diagnostic := range out.Diagnostics {
		if diagnostic.Kind == "MISSING" {
			return
		}
	}
	t.Fatalf("expected missing token: %+v", out.Diagnostics)
}

func TestCancellationAndReuse(t *testing.T) {
	e := newExtractor(t, "typescript")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.Extract(ctx, []byte("const a = 1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	// Large input ensures the deadline exercises an in-progress native parse.
	source := []byte(strings.Repeat("export const item = (x: number) => x + 1;\n", 100_000))
	ctx, cancel = context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := e.Extract(ctx, source); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
	out, err := e.Extract(t.Context(), []byte("export function Fresh() {}"))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Declarations) != 1 || out.Declarations[0].Name != "Fresh" || len(out.Diagnostics) != 0 {
		t.Fatalf("reused parser: %+v", out)
	}
}

// PR #365 fixes generic import-type members in both dialects. These fixtures
// exercise the original declaration-loss case and aliases/annotations.
func TestImportTypeMembers(t *testing.T) {
	for _, language := range []string{"typescript", "tsx"} {
		t.Run(language, func(t *testing.T) {
			e := newExtractor(t, language)
			source := []byte("export function useValue(value: Readonly<import('vue').Ref<string>>) {}\ntype Value = import('mod').Value<string>;\nconst value: import('mod').Value<string> = source;")
			out, err := e.Extract(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Diagnostics) != 0 {
				t.Fatalf("unexpected diagnostics: %+v", out.Diagnostics)
			}
			if len(out.Declarations) != 3 || out.Declarations[2].Name != "value" || out.Declarations[0].Name != "useValue" || out.Declarations[1].Name != "Value" {
				t.Fatalf("declarations: %+v", out.Declarations)
			}
		})
	}
}

// Upstream issue #367 is separate from generic member support. Keep this
// limitation visible; upgrading the grammar should revisit this expectation.
func TestKnownTypeofImportCallLimitation(t *testing.T) {
	for _, language := range []string{"typescript", "tsx"} {
		t.Run(language, func(t *testing.T) {
			e := newExtractor(t, language)
			out, err := e.Extract(t.Context(), []byte("export function load() { return f<typeof import('mod')>(); }"))
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Diagnostics) == 0 {
				t.Fatal("grammar limitation changed; revisit fixture")
			}
			if len(out.Declarations) != 1 || out.Declarations[0].Name != "load" {
				t.Fatalf("lost containing function: %+v", out)
			}
		})
	}
}

func TestResultsOutliveParser(t *testing.T) {
	e, err := extract.New("go")
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.Extract(t.Context(), []byte("package p\nfunc First() {}"))
	if err != nil {
		e.Close()
		t.Fatal(err)
	}
	_, err = e.Extract(t.Context(), []byte("package p\nfunc Second() {}"))
	e.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Declarations) != 1 || out.Declarations[0].Name != "First" || out.Declarations[0].Signature != "func First()" {
		t.Fatalf("retained result: %+v", out)
	}
}

func TestUnsupportedLanguage(t *testing.T) {
	if _, err := extract.New("python"); err == nil {
		t.Fatal("expected unsupported language error")
	}
	for path, want := range map[string]string{"a.go": "go", "a.d.ts": "typescript", "a.mts": "typescript", "a.cts": "typescript", "a.tsx": "tsx", "a.vue": ""} {
		if got := extract.Language(path); got != want {
			t.Errorf("Language(%q) = %q, want %q", path, got, want)
		}
	}
}

func BenchmarkExtract(b *testing.B) {
	for _, language := range []string{"go", "typescript", "tsx"} {
		b.Run(language, func(b *testing.B) {
			e, err := extract.New(language)
			if err != nil {
				b.Fatal(err)
			}
			defer e.Close()
			text := "export function Item(x: number): number { return x + 1; }\n"
			if language == "go" {
				text = "func Item(x int) int { return x + 1 }\n"
			}
			if language == "tsx" {
				text = "export const Item = ({x}: {x: string}) => <span>{x}</span>;\n"
			}
			source := []byte(strings.Repeat(text, 100))
			if language == "go" {
				source = append([]byte("package p\n"), source...)
			}
			b.SetBytes(int64(len(source)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := e.Extract(b.Context(), source); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func newExtractor(t *testing.T, language string) *extract.Extractor {
	t.Helper()
	e, err := extract.New(language)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}
