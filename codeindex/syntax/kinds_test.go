package syntax_test

import (
	extract "github.com/crowl/ronin/codeindex/syntax"
	"testing"
)

func TestDeclarationKinds(t *testing.T) {
	for _, tt := range []struct {
		lang, source string
		want         map[string]string
	}{
		{"go", "package p\nconst Answer = 42\nvar Ready = true\nfunc Run() { var local = 1; _ = local }", map[string]string{"Answer": "constant", "Ready": "variable", "Run": "function"}},
		{"typescript", "export enum Mode { Read, Write }\nexport const Ready = true;\nexport const Run = () => { const local = 1; };", map[string]string{"Mode": "enum", "Ready": "variable", "Run": "function"}},
	} {
		t.Run(tt.lang, func(t *testing.T) {
			e, err := extract.New(tt.lang)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			out, err := e.Extract(t.Context(), []byte(tt.source))
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Diagnostics) > 0 || len(out.Declarations) != len(tt.want) {
				t.Fatalf("outline: %+v", out)
			}
			for _, d := range out.Declarations {
				if tt.want[d.Name] != d.Kind {
					t.Errorf("unexpected declaration: %+v", d)
				}
			}
		})
	}
}
