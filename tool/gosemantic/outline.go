package gosemantic

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/tool"
)

type OutlinePackageArgs struct {
	Package           string `json:"package" jsonschema:"Package to outline, given as an import path (github.com/you/mod/pkg), directory path (tool/readfile or .), or package name (readfile)."`
	IncludeUnexported bool   `json:"include_unexported,omitempty" jsonschema:"Include unexported symbols. Defaults to false (exported API only)."`
}

func (a OutlinePackageArgs) Validate() error {
	if strings.TrimSpace(a.Package) == "" {
		return tool.Error{Code: "invalid_args", Message: "package must not be empty"}
	}
	if strings.ContainsRune(a.Package, '\x00') {
		return tool.Error{Code: "invalid_args", Message: "package must not contain NUL bytes"}
	}
	return nil
}

type TypeOutline struct {
	Symbol
	Methods []Symbol `json:"methods,omitempty"`
}

type PackageOutline struct {
	Package   string        `json:"package"`
	Types     []TypeOutline `json:"types,omitempty"`
	Functions []Symbol      `json:"functions,omitempty"`
	Constants []Symbol      `json:"constants,omitempty"`
	Variables []Symbol      `json:"variables,omitempty"`
}

type OutlinePackageResult struct {
	Packages []PackageOutline `json:"packages"`
	Warnings []string         `json:"warnings,omitempty"`
}

func (r OutlinePackageResult) Artifacts() []tool.Artifact {
	return []tool.Artifact{tool.TextArtifact{Text: outlinePackageSummary(r)}}
}

func NewOutlinePackage(cwd string) *OutlinePackage {
	return &OutlinePackage{cwd: cwd}
}

type OutlinePackage struct {
	cwd string
}

func (t *OutlinePackage) Name() string {
	return "outline_package"
}

func (t *OutlinePackage) Description() string {
	return "Map an unfamiliar Go package before reading implementation. Lists its types with methods, functions, constants, and variables, including signatures, exact files, 1-based line ranges, and doc comments; exported API only by default. Choose relevant declarations from the outline, then pass their ranges to read_file instead of reading whole files. Use find_symbol when you already know a declaration name and text search for references, implementations, callers, callees, and other textual queries."
}

func (t *OutlinePackage) Parameters() *jsonschema.Schema {
	return jsonschema.FromType[OutlinePackageArgs]()
}

func (t *OutlinePackage) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	return tool.CallTyped(ctx, raw, t.call)
}

func (t *OutlinePackage) CallTitle(rawArgs json.RawMessage) (string, error) {
	args, err := tool.DecodeArgs[OutlinePackageArgs](rawArgs)
	if err != nil {
		return "", err
	}
	return t.Name() + " " + args.Package, nil
}

func (t *OutlinePackage) call(ctx context.Context, args OutlinePackageArgs) (OutlinePackageResult, error) {
	mod, err := loadModule(ctx, t.cwd)
	if err != nil {
		return OutlinePackageResult{}, err
	}

	filter := normalizePackageSpec(args.Package)

	var outlines []PackageOutline
	for _, pkg := range mod.packages {
		if !packageMatches(t.cwd, pkg, filter) {
			continue
		}
		outlines = append(outlines, buildOutline(t.cwd, mod, pkg, args.IncludeUnexported))
	}

	if len(outlines) == 0 {
		return OutlinePackageResult{}, tool.Error{Code: "not_found", Message: "no package matching " + args.Package + " in the local module"}
	}

	return OutlinePackageResult{Packages: outlines, Warnings: mod.warnings}, nil
}

func buildOutline(cwd string, mod *module, pkg *packages.Package, includeUnexported bool) PackageOutline {
	outline := PackageOutline{Package: pkg.PkgPath}
	methodsByRecv := map[string][]Symbol{}

	for _, sym := range packageSymbols(cwd, mod.fset, pkg) {
		if !includeUnexported && !exported(sym) {
			continue
		}
		switch sym.Kind {
		case kindMethod:
			methodsByRecv[sym.Recv] = append(methodsByRecv[sym.Recv], sym)
		case kindFunc:
			outline.Functions = append(outline.Functions, sym)
		case kindConst:
			outline.Constants = append(outline.Constants, sym)
		case kindVar:
			outline.Variables = append(outline.Variables, sym)
		case kindType:
			outline.Types = append(outline.Types, TypeOutline{Symbol: sym})
		}
	}

	for i := range outline.Types {
		methods := methodsByRecv[outline.Types[i].Name]
		slices.SortFunc(methods, func(a, b Symbol) int { return strings.Compare(a.Name, b.Name) })
		outline.Types[i].Methods = methods
	}

	return outline
}
