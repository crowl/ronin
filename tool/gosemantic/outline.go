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
	Package           string `json:"package" jsonschema:"Package to outline, given as an import path (github.com/you/mod/pkg) or a path relative to the module (tool/readfile). A bare package name is allowed and may match several packages."`
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
	var artifacts []tool.Artifact
	for _, pkg := range r.Packages {
		for _, t := range pkg.Types {
			artifacts = append(artifacts, rangeArtifact(t.Symbol))
		}
		for _, f := range pkg.Functions {
			artifacts = append(artifacts, rangeArtifact(f))
		}
	}
	return artifacts
}

func rangeArtifact(s Symbol) tool.Artifact {
	return tool.FileRangeArtifact{Path: s.File, StartLine: s.StartLine, EndLine: s.EndLine}
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
	return "List the symbols a Go package in the local module declares: types with their methods, functions, constants, and variables, each with its signature, file, line range, and doc comment. Exported only by default."
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
		if !packageMatches(pkg.PkgPath, pkg.Name, filter) {
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

func normalizePackageSpec(spec string) string {
	spec = strings.TrimSpace(spec)
	spec = strings.TrimPrefix(spec, "./")
	spec = strings.TrimSuffix(spec, "/")
	return spec
}
