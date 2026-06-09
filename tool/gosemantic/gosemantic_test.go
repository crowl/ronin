package gosemantic_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/gosemantic"
)

func fixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("testdata", "fixturemod"))
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}
	return root
}

func TestFindSymbolAmbiguous(t *testing.T) {
	res := findSymbol(t, fixtureRoot(t), `{"name":"Circle"}`)

	if len(res.Matches) != 2 {
		t.Fatalf("want 2 matches for ambiguous Circle, got %d: %+v", len(res.Matches), res.Matches)
	}
	pkgs := map[string]bool{}
	for _, m := range res.Matches {
		if m.Kind != "type" {
			t.Errorf("match %s: want kind type, got %q", m.Name, m.Kind)
		}
		pkgs[m.PkgPath] = true
	}
	if !pkgs["example.com/fixture/shapes"] || !pkgs["example.com/fixture/geo"] {
		t.Errorf("want matches in shapes and geo, got %v", pkgs)
	}
}

func TestFindSymbolQualified(t *testing.T) {
	res := findSymbol(t, fixtureRoot(t), `{"name":"shapes.Circle"}`)

	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %d", len(res.Matches))
	}
	m := res.Matches[0]
	if m.PkgPath != "example.com/fixture/shapes" {
		t.Errorf("package: got %q", m.PkgPath)
	}
	if m.File != "shapes/shapes.go" {
		t.Errorf("file: got %q", m.File)
	}
	if m.Signature != "type Circle struct{ ... }" {
		t.Errorf("signature: got %q", m.Signature)
	}
	if m.StartLine != 10 || m.EndLine != 13 {
		t.Errorf("range: got %d:%d, want 10:13", m.StartLine, m.EndLine)
	}
	if !strings.HasPrefix(m.Doc, "Circle is a round shape") {
		t.Errorf("doc: got %q", m.Doc)
	}
}

func TestFindSymbolMethod(t *testing.T) {
	res := findSymbol(t, fixtureRoot(t), `{"name":"Circle.Area"}`)

	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %d: %+v", len(res.Matches), res.Matches)
	}
	m := res.Matches[0]
	if m.Kind != "method" || m.Recv != "Circle" {
		t.Errorf("want method on Circle, got kind=%q recv=%q", m.Kind, m.Recv)
	}
	if m.Signature != "func (c Circle) Area() float64" {
		t.Errorf("signature: got %q", m.Signature)
	}
}

func TestFindSymbolConstRange(t *testing.T) {
	res := findSymbol(t, fixtureRoot(t), `{"name":"Pi"}`)

	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %d", len(res.Matches))
	}
	m := res.Matches[0]
	if m.Kind != "const" || m.Signature != "const Pi" {
		t.Errorf("want const Pi, got kind=%q signature=%q", m.Kind, m.Signature)
	}
	if m.StartLine != 4 || m.EndLine != 5 {
		t.Errorf("range: got %d:%d, want 4:5 (doc included)", m.StartLine, m.EndLine)
	}
}

func TestFindSymbolPackageFilter(t *testing.T) {
	res := findSymbol(t, fixtureRoot(t), `{"name":"Circle","package":"geo"}`)

	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match with package filter, got %d", len(res.Matches))
	}
	if res.Matches[0].PkgPath != "example.com/fixture/geo" {
		t.Errorf("package: got %q", res.Matches[0].PkgPath)
	}
}

func TestFindSymbolNotFound(t *testing.T) {
	_, err := gosemantic.NewFindSymbol(fixtureRoot(t)).Call(context.Background(), json.RawMessage(`{"name":"Missing"}`))
	assertToolError(t, err, "not_found")
}

func TestFindSymbolInvalidArgs(t *testing.T) {
	_, err := gosemantic.NewFindSymbol(fixtureRoot(t)).Call(context.Background(), json.RawMessage(`{"name":"  "}`))
	assertToolError(t, err, "invalid_args")
}

func TestFindSymbolReportsWarnings(t *testing.T) {
	res := findSymbol(t, fixtureRoot(t), `{"name":"Circle"}`)
	if !containsSubstring(res.Warnings, "brokenpkg") {
		t.Errorf("want a warning mentioning brokenpkg, got %v", res.Warnings)
	}
}

func TestOutlinePackageExported(t *testing.T) {
	res := outlinePackage(t, fixtureRoot(t), `{"package":"shapes"}`)

	if len(res.Packages) != 1 {
		t.Fatalf("want 1 package, got %d", len(res.Packages))
	}
	pkg := res.Packages[0]

	if got := names(pkg.Functions); !equalStrings(got, []string{"Describe"}) {
		t.Errorf("functions: got %v", got)
	}
	if got := names(pkg.Constants); !equalStrings(got, []string{"Pi"}) {
		t.Errorf("constants: got %v", got)
	}
	if got := names(pkg.Variables); !equalStrings(got, []string{"DefaultName"}) {
		t.Errorf("variables: got %v", got)
	}
	if len(pkg.Types) != 1 || pkg.Types[0].Name != "Circle" {
		t.Fatalf("types: got %+v", pkg.Types)
	}
	if got := names(pkg.Types[0].Methods); !equalStrings(got, []string{"Area", "Scale"}) {
		t.Errorf("Circle methods: got %v", got)
	}
}

func TestOutlinePackageIncludeUnexported(t *testing.T) {
	res := outlinePackage(t, fixtureRoot(t), `{"package":"shapes","include_unexported":true}`)
	pkg := res.Packages[0]

	if !equalStrings(names(pkg.Functions), []string{"Describe", "helper"}) {
		t.Errorf("functions: got %v", names(pkg.Functions))
	}
	typeNames := []string{}
	for _, ty := range pkg.Types {
		typeNames = append(typeNames, ty.Name)
	}
	if !equalStrings(typeNames, []string{"Circle", "hidden"}) {
		t.Errorf("types: got %v", typeNames)
	}
}

func TestOutlinePackageNotFound(t *testing.T) {
	_, err := gosemantic.NewOutlinePackage(fixtureRoot(t)).Call(context.Background(), json.RawMessage(`{"package":"nope"}`))
	assertToolError(t, err, "not_found")
}

func TestOutlinePackageByImportPath(t *testing.T) {
	res := outlinePackage(t, fixtureRoot(t), `{"package":"example.com/fixture/geo"}`)
	if len(res.Packages) != 1 || res.Packages[0].Package != "example.com/fixture/geo" {
		t.Fatalf("want geo package, got %+v", res.Packages)
	}
}

func TestFindSymbolCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := gosemantic.NewFindSymbol(fixtureRoot(t)).Call(ctx, json.RawMessage(`{"name":"Circle"}`))
	assertToolError(t, err, "package_load_failed")
}

// findSymbol runs the find_symbol tool and returns its typed result.
func findSymbol(t *testing.T, cwd, args string) gosemantic.FindSymbolResult {
	t.Helper()
	out, err := gosemantic.NewFindSymbol(cwd).Call(t.Context(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("find_symbol(%s): %v", args, err)
	}
	res, ok := out.(gosemantic.FindSymbolResult)
	if !ok {
		t.Fatalf("find_symbol returned %T", out)
	}
	return res
}

func outlinePackage(t *testing.T, cwd, args string) gosemantic.OutlinePackageResult {
	t.Helper()
	out, err := gosemantic.NewOutlinePackage(cwd).Call(t.Context(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("outline_package(%s): %v", args, err)
	}
	res, ok := out.(gosemantic.OutlinePackageResult)
	if !ok {
		t.Fatalf("outline_package returned %T", out)
	}
	return res
}

func assertToolError(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error with code %q, got nil", code)
	}
	var te tool.Error
	if !errors.As(err, &te) {
		t.Fatalf("want tool.Error, got %T: %v", err, err)
	}
	if te.Code != code {
		t.Fatalf("want code %q, got %q (%s)", code, te.Code, te.Message)
	}
}

func names(syms []gosemantic.Symbol) []string {
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		out = append(out, s.Name)
	}
	return out
}

func equalStrings(got, want []string) bool {
	return slices.Equal(got, want)
}

func containsSubstring(items []string, sub string) bool {
	for _, item := range items {
		if strings.Contains(item, sub) {
			return true
		}
	}
	return false
}
