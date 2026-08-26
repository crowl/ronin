package gosemantic_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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

func TestFindSymbolDescriptionGuidesUse(t *testing.T) {
	description := gosemantic.NewFindSymbol(fixtureRoot(t)).Description()
	for _, expected := range []string{"exact file", "1-based line range", "targeted read_file", "before text search", "does not find references", "struct/interface fields", "go_navigation"} {
		if !strings.Contains(description, expected) {
			t.Errorf("description %q does not contain %q", description, expected)
		}
	}
}

func TestOutlinePackageDescriptionGuidesUse(t *testing.T) {
	description := gosemantic.NewOutlinePackage(fixtureRoot(t)).Description()
	for _, expected := range []string{"unfamiliar Go package", "exact files", "1-based line ranges", "pass their ranges to read_file", "find_symbol", "go_navigation", "references", "callers", "callees"} {
		if !strings.Contains(description, expected) {
			t.Errorf("description %q does not contain %q", description, expected)
		}
	}
}

func TestNavigationDescriptionGuidesUse(t *testing.T) {
	description := gosemantic.NewNavigation(fixtureRoot(t)).Description()
	for _, expected := range []string{"by symbol or 1-based file position", "references", "implementations", "callers", "callees", "dynamic_candidate", "enclosing declaration ranges", "targeted read_file"} {
		if !strings.Contains(description, expected) {
			t.Errorf("description %q does not contain %q", description, expected)
		}
	}
}

func TestNavigationReferencesByName(t *testing.T) {
	res := navigate(t, fixtureRoot(t), `{"operation":"references","name":"shapes.Pi"}`)
	if res.Target.Name != "Pi" || res.Total != 1 {
		t.Fatalf("references Pi = %+v", res)
	}
	location := res.Locations[0]
	if location.File != "shapes/shapes.go" || location.Line != 17 {
		t.Errorf("reference location = %+v", location)
	}
	if location.Enclosing == nil || location.Enclosing.Name != "Area" || location.Enclosing.StartLine != 15 || location.Enclosing.EndLine != 18 {
		t.Errorf("enclosing declaration = %+v", location.Enclosing)
	}
}

func TestNavigationReferencesByPosition(t *testing.T) {
	res := navigate(t, fixtureRoot(t), `{"operation":"references","file":"shapes/shapes.go","line":5,"column":7}`)
	if res.Target.Name != "Pi" || res.Total != 1 || res.Locations[0].Line != 17 {
		t.Fatalf("references by position = %+v", res)
	}
}

func TestNavigationLocalVariableReferencesByPosition(t *testing.T) {
	res := navigate(t, fixtureRoot(t), `{"operation":"references","file":"shapes/navigation.go","line":24,"column":16}`)
	if res.Target.Name != "s" || res.Total != 1 || res.Locations[0].Line != 25 {
		t.Fatalf("local variable references = %+v", res)
	}
}

func TestNavigationPositionSupportsFields(t *testing.T) {
	res := navigate(t, fixtureRoot(t), `{"operation":"references","file":"shapes/shapes.go","line":12,"column":2}`)
	if res.Target.Name != "Radius" || res.Total != 4 {
		t.Fatalf("field references = %+v", res)
	}
}

func TestNavigationIncludesTestFiles(t *testing.T) {
	res := navigate(t, fixtureRoot(t), `{"operation":"references","name":"shapes.CircleArea"}`)
	var found bool
	for _, location := range res.Locations {
		if location.File == "shapes/navigation_test.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("CircleArea references do not include test file: %+v", res)
	}
}

func TestNavigationImplementationsForInterface(t *testing.T) {
	res := navigate(t, fixtureRoot(t), `{"operation":"implementations","name":"shapes.Shape"}`)
	if res.Total != 2 {
		t.Fatalf("Shape implementations = %+v", res)
	}
	if got := relatedNames(res.Locations); !slices.Equal(got, []string{".Circle", ".Rectangle"}) {
		t.Errorf("implementation types = %v", got)
	}
}

func TestNavigationImplementationsForInterfaceMethodPosition(t *testing.T) {
	res := navigate(t, fixtureRoot(t), `{"operation":"implementations","file":"shapes/navigation.go","line":5,"column":2}`)
	if res.Target.Name != "Area" || res.Total != 2 {
		t.Fatalf("Area implementations = %+v", res)
	}
	if got := relatedNames(res.Locations); !slices.Equal(got, []string{"Circle.Area", "Rectangle.Area"}) {
		t.Errorf("implementation methods = %v", got)
	}
}

func TestNavigationStaticCallersAndCallees(t *testing.T) {
	callers := navigate(t, fixtureRoot(t), `{"operation":"callers","name":"shapes.CircleArea"}`)
	if callers.Total != 2 || !hasRelated(callers.Locations, "CombinedArea") || !hasRelated(callers.Locations, "TestCircleArea") {
		t.Fatalf("CircleArea callers = %+v", callers)
	}

	callees := navigate(t, fixtureRoot(t), `{"operation":"callees","name":"shapes.CombinedArea"}`)
	if callees.Total != 2 {
		t.Fatalf("CombinedArea callees = %+v", callees)
	}
	if got := relatedNames(callees.Locations); !slices.Equal(got, []string{".CircleArea", ".ShapeArea"}) {
		t.Errorf("callees = %v", got)
	}
}

func TestNavigationDynamicCallersAreMarked(t *testing.T) {
	res := navigate(t, fixtureRoot(t), `{"operation":"callers","name":"shapes.Circle.Area"}`)
	var foundDynamic bool
	for _, location := range res.Locations {
		if location.DynamicCandidate && location.Related != nil && location.Related.Name == "ShapeArea" {
			foundDynamic = true
		}
	}
	if !foundDynamic {
		t.Fatalf("Circle.Area callers do not include dynamic ShapeArea call: %+v", res)
	}
}

func TestNavigationDynamicCalleesAreMarked(t *testing.T) {
	res := navigate(t, fixtureRoot(t), `{"operation":"callees","name":"shapes.ShapeArea"}`)
	if res.Total < 2 {
		t.Fatalf("ShapeArea callees = %+v, related = %v", res, relatedNames(res.Locations))
	}
	for _, location := range res.Locations {
		if !location.DynamicCandidate {
			t.Errorf("dynamic callee is not marked: %+v", location)
		}
	}
}

func TestNavigationAmbiguousSymbolReturnsCandidates(t *testing.T) {
	res := navigate(t, fixtureRoot(t), `{"operation":"references","name":"Circle"}`)
	if len(res.Candidates) != 2 || len(res.Locations) != 0 {
		t.Fatalf("ambiguous navigation = %+v", res)
	}
}

func TestNavigationPagination(t *testing.T) {
	res := navigate(t, fixtureRoot(t), `{"operation":"references","name":"shapes.Circle","offset":1,"limit":2}`)
	if len(res.Locations) != 2 || res.Offset != 1 || !res.HasMore || res.Total <= 3 {
		t.Fatalf("paginated references = %+v", res)
	}
}

func TestNavigationRejectsInvalidSelectors(t *testing.T) {
	for _, args := range []string{
		`{"operation":"references","name":"Pi","file":"shapes/shapes.go","line":5,"column":7}`,
		`{"operation":"references","file":"shapes/shapes.go","line":5}`,
		`{"operation":"unknown","name":"Pi"}`,
	} {
		_, err := gosemantic.NewNavigation(fixtureRoot(t)).Call(t.Context(), json.RawMessage(args))
		assertToolError(t, err, "invalid_args")
	}
}

func TestNavigationArtifactsExposeRanges(t *testing.T) {
	res := navigate(t, fixtureRoot(t), `{"operation":"references","name":"shapes.Pi"}`)
	text := artifactText(t, res.Artifacts())
	for _, expected := range []string{"References for Pi", "shapes/shapes.go:17", "enclosing range: shapes/shapes.go:15-18", "Warnings:", "brokenpkg"} {
		if !strings.Contains(text, expected) {
			t.Errorf("artifact text does not contain %q:\n%s", expected, text)
		}
	}
}

func TestFindSymbolArtifactsSummarizeMatchesAndWarnings(t *testing.T) {
	res := findSymbol(t, fixtureRoot(t), `{"name":"shapes.Circle"}`)
	text := artifactText(t, res.Artifacts())

	for _, expected := range []string{
		"Package: example.com/fixture/shapes",
		"type Circle struct{ ... }",
		"shapes/shapes.go:10-13",
		"Circle is a round shape defined by its radius.",
		"Warnings:",
		"brokenpkg",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("artifact text does not contain %q:\n%s", expected, text)
		}
	}
}

func TestOutlinePackageArtifactsSummarizeEveryDeclarationGroup(t *testing.T) {
	res := outlinePackage(t, fixtureRoot(t), `{"package":"shapes"}`)
	text := artifactText(t, res.Artifacts())

	for _, expected := range []string{
		"Package: example.com/fixture/shapes",
		"Types:",
		"Methods:",
		"func (c Circle) Area() float64",
		"Functions:",
		"func Describe(name string) string",
		"Constants:",
		"const Pi",
		"Variables:",
		"var DefaultName",
		"Warnings:",
		"brokenpkg",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("artifact text does not contain %q:\n%s", expected, text)
		}
	}
}

func TestFindSymbolRelativeWorkingDirectoryUsesRelativePaths(t *testing.T) {
	t.Chdir(fixtureRoot(t))

	res := findSymbol(t, ".", `{"name":"shapes.Circle"}`)

	if got := res.Matches[0].File; got != "shapes/shapes.go" {
		t.Errorf("file = %q, want relative path", got)
	}
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

	if got := names(pkg.Functions); !equalStrings(got, []string{"CircleArea", "ShapeArea", "CombinedArea", "Describe"}) {
		t.Errorf("functions: got %v", got)
	}
	if got := names(pkg.Constants); !equalStrings(got, []string{"Pi"}) {
		t.Errorf("constants: got %v", got)
	}
	if got := names(pkg.Variables); !equalStrings(got, []string{"DefaultName"}) {
		t.Errorf("variables: got %v", got)
	}
	if len(pkg.Types) != 3 || pkg.Types[2].Name != "Circle" {
		t.Fatalf("types: got %+v", pkg.Types)
	}
	if got := names(pkg.Types[2].Methods); !equalStrings(got, []string{"Area", "Scale"}) {
		t.Errorf("Circle methods: got %v", got)
	}
}

func TestOutlinePackageIncludeUnexported(t *testing.T) {
	res := outlinePackage(t, fixtureRoot(t), `{"package":"shapes","include_unexported":true}`)
	pkg := res.Packages[0]

	if !equalStrings(names(pkg.Functions), []string{"CircleArea", "ShapeArea", "CombinedArea", "Describe", "helper"}) {
		t.Errorf("functions: got %v", names(pkg.Functions))
	}
	typeNames := []string{}
	for _, ty := range pkg.Types {
		typeNames = append(typeNames, ty.Name)
	}
	if !equalStrings(typeNames, []string{"Shape", "Rectangle", "Circle", "hidden"}) {
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

func TestFindSymbolFullImportPath(t *testing.T) {
	res := findSymbol(t, fixtureRoot(t), `{"name":"example.com/fixture/shapes.Circle"}`)
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match for full import path, got %d", len(res.Matches))
	}
	if res.Matches[0].Name != "Circle" || res.Matches[0].PkgPath != "example.com/fixture/shapes" {
		t.Errorf("got %+v", res.Matches[0])
	}
}

func TestFindSymbolFullImportPathMethod(t *testing.T) {
	res := findSymbol(t, fixtureRoot(t), `{"name":"example.com/fixture/shapes.Circle.Area"}`)
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match for full import path method, got %d", len(res.Matches))
	}
	if res.Matches[0].Name != "Area" || res.Matches[0].Recv != "Circle" {
		t.Errorf("got %+v", res.Matches[0])
	}
}

func TestFindSymbolThreePartMethod(t *testing.T) {
	res := findSymbol(t, fixtureRoot(t), `{"name":"shapes.Circle.Area"}`)
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match for shapes.Circle.Area, got %d", len(res.Matches))
	}
	if res.Matches[0].Name != "Area" || res.Matches[0].Recv != "Circle" {
		t.Errorf("got %+v", res.Matches[0])
	}
}

func TestFindSymbolPointerReceiverSyntax(t *testing.T) {
	for _, name := range []string{"(*Circle).Scale", "*Circle.Scale", "shapes.(*Circle).Scale", "shapes.*Circle.Scale"} {
		t.Run(name, func(t *testing.T) {
			res := findSymbol(t, fixtureRoot(t), `{"name":`+strconvQuote(name)+`}`)
			if len(res.Matches) != 1 {
				t.Fatalf("want 1 match for %s, got %d", name, len(res.Matches))
			}
			if res.Matches[0].Name != "Scale" || res.Matches[0].Recv != "Circle" {
				t.Errorf("got %+v", res.Matches[0])
			}
		})
	}
}

func TestOutlinePackageCurrentDirectory(t *testing.T) {
	shapesDir := filepath.Join(fixtureRoot(t), "shapes")
	res := outlinePackage(t, shapesDir, `{"package":"."}`)
	if len(res.Packages) != 1 || res.Packages[0].Package != "example.com/fixture/shapes" {
		t.Fatalf("want shapes package, got %+v", res.Packages)
	}

	res2 := outlinePackage(t, shapesDir, `{"package":"./"}`)
	if len(res2.Packages) != 1 || res2.Packages[0].Package != "example.com/fixture/shapes" {
		t.Fatalf("want shapes package, got %+v", res2.Packages)
	}
}

func TestOutlinePackageRelativeDirectory(t *testing.T) {
	res := outlinePackage(t, fixtureRoot(t), `{"package":"./shapes"}`)
	if len(res.Packages) != 1 || res.Packages[0].Package != "example.com/fixture/shapes" {
		t.Fatalf("want shapes package, got %+v", res.Packages)
	}

	res2 := outlinePackage(t, fixtureRoot(t), `{"package":"shapes/"}`)
	if len(res2.Packages) != 1 || res2.Packages[0].Package != "example.com/fixture/shapes" {
		t.Fatalf("want shapes package, got %+v", res2.Packages)
	}
}

func TestMultiModuleMonorepoDiscovery(t *testing.T) {
	root := t.TempDir()

	subA := filepath.Join(root, "serviceA")
	if err := os.MkdirAll(subA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subA, "go.mod"), []byte("module example.com/serviceA\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subA, "service.go"), []byte("package serviceA\n\n// Alpha is a service\nfunc Alpha() string { return \"A\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	subB := filepath.Join(root, "serviceB")
	if err := os.MkdirAll(subB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subB, "go.mod"), []byte("module example.com/serviceB\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subB, "service.go"), []byte("package serviceB\n\n// Beta is a service\nfunc Beta() string { return \"B\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// cwd is root (which has NO go.mod)
	resA := findSymbol(t, root, `{"name":"Alpha"}`)
	if len(resA.Matches) != 1 || resA.Matches[0].Name != "Alpha" {
		t.Fatalf("Alpha match = %+v", resA.Matches)
	}
	if resA.Matches[0].File != "serviceA/service.go" {
		t.Errorf("Alpha file = %q, want serviceA/service.go", resA.Matches[0].File)
	}

	resB := findSymbol(t, root, `{"name":"Beta"}`)
	if len(resB.Matches) != 1 || resB.Matches[0].Name != "Beta" {
		t.Fatalf("Beta match = %+v", resB.Matches)
	}
	if resB.Matches[0].File != "serviceB/service.go" {
		t.Errorf("Beta file = %q, want serviceB/service.go", resB.Matches[0].File)
	}

	outlineA := outlinePackage(t, root, `{"package":"serviceA"}`)
	if len(outlineA.Packages) != 1 || outlineA.Packages[0].Package != "example.com/serviceA" {
		t.Fatalf("outlineA = %+v", outlineA.Packages)
	}
}

func TestGoWorkspaceDiscovery(t *testing.T) {
	root := t.TempDir()

	modA := filepath.Join(root, "modA")
	if err := os.MkdirAll(modA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modA, "go.mod"), []byte("module example.com/modA\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modA, "modA.go"), []byte("package modA\n\nfunc ModAFunc() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	modB := filepath.Join(root, "modB")
	if err := os.MkdirAll(modB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modB, "go.mod"), []byte("module example.com/modB\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modB, "modB.go"), []byte("package modB\n\nfunc ModBFunc() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	goWorkContent := "go 1.22\n\nuse (\n\t./modA\n\t./modB\n)\n"
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte(goWorkContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// From workspace root
	res := findSymbol(t, root, `{"name":"ModAFunc"}`)
	if len(res.Matches) != 1 || res.Matches[0].Name != "ModAFunc" {
		t.Fatalf("ModAFunc match = %+v", res.Matches)
	}

	// From subdirectory
	res2 := findSymbol(t, modB, `{"name":"ModAFunc"}`)
	if len(res2.Matches) != 1 || res2.Matches[0].Name != "ModAFunc" {
		t.Fatalf("ModAFunc from modB match = %+v", res2.Matches)
	}
}

func TestFindSymbolNoGoMod(t *testing.T) {
	emptyDir := t.TempDir()
	_, err := gosemantic.NewFindSymbol(emptyDir).Call(context.Background(), json.RawMessage(`{"name":"Foo"}`))
	assertToolError(t, err, "package_load_failed")
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestFindSymbolCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := gosemantic.NewFindSymbol(fixtureRoot(t)).Call(ctx, json.RawMessage(`{"name":"Circle"}`))
	assertToolError(t, err, "package_load_failed")
}

func navigate(t *testing.T, cwd, args string) gosemantic.NavigationResult {
	t.Helper()
	out, err := gosemantic.NewNavigation(cwd).Call(t.Context(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("go_navigation(%s): %v", args, err)
	}
	res, ok := out.(gosemantic.NavigationResult)
	if !ok {
		t.Fatalf("go_navigation returned %T", out)
	}
	return res
}

func hasRelated(locations []gosemantic.NavigationLocation, name string) bool {
	for _, location := range locations {
		if location.Related != nil && location.Related.Name == name {
			return true
		}
	}
	return false
}

func relatedNames(locations []gosemantic.NavigationLocation) []string {
	out := make([]string, 0, len(locations))
	for _, location := range locations {
		if location.Related != nil {
			out = append(out, location.Related.Recv+"."+location.Related.Name)
		}
	}
	slices.Sort(out)
	return out
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

func artifactText(t *testing.T, artifacts []tool.Artifact) string {
	t.Helper()
	if len(artifacts) != 1 {
		t.Fatalf("artifact count = %d, want 1: %#v", len(artifacts), artifacts)
	}
	artifact, ok := artifacts[0].(tool.TextArtifact)
	if !ok {
		t.Fatalf("artifact type = %T, want tool.TextArtifact", artifacts[0])
	}
	if strings.TrimSpace(artifact.Text) == "" {
		t.Fatal("artifact text is empty")
	}
	return artifact.Text
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
