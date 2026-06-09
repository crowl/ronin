package gosemantic

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/tool"
)

const maxMatches = 50

type FindSymbolArgs struct {
	Name    string `json:"name" jsonschema:"Symbol to locate. Accepts a bare name (Tool), a package-qualified name (readfile.Tool), or a method or field (Args.Validate)."`
	Package string `json:"package,omitempty" jsonschema:"Optional package import path or name to restrict the search."`
}

func (a FindSymbolArgs) Validate() error {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return tool.Error{Code: "invalid_args", Message: "name must not be empty"}
	}
	if strings.ContainsRune(a.Name, '\x00') || strings.ContainsRune(a.Package, '\x00') {
		return tool.Error{Code: "invalid_args", Message: "arguments must not contain NUL bytes"}
	}
	if strings.Count(name, ".") > 2 {
		return tool.Error{Code: "invalid_args", Message: "name has too many qualifiers; use [package.][receiver.]name"}
	}
	return nil
}

type FindSymbolResult struct {
	Matches  []Symbol `json:"matches"`
	Warnings []string `json:"warnings,omitempty"`
}

func (r FindSymbolResult) Artifacts() []tool.Artifact {
	artifacts := make([]tool.Artifact, 0, len(r.Matches))
	for _, m := range r.Matches {
		artifacts = append(artifacts, tool.FileRangeArtifact{
			Path:      m.File,
			StartLine: m.StartLine,
			EndLine:   m.EndLine,
		})
	}
	return artifacts
}

func NewFindSymbol(cwd string) *FindSymbol {
	return &FindSymbol{cwd: cwd}
}

type FindSymbol struct {
	cwd string
}

func (t *FindSymbol) Name() string {
	return "find_symbol"
}

func (t *FindSymbol) Description() string {
	return "Locate where a Go symbol is defined in the local module. Returns each matching declaration's package, file, line range, signature, and doc comment. Accepts bare, package-qualified, or method/field names; returns all matches when ambiguous."
}

func (t *FindSymbol) Parameters() *jsonschema.Schema {
	return jsonschema.FromType[FindSymbolArgs]()
}

func (t *FindSymbol) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	return tool.CallTyped(ctx, raw, t.call)
}

func (t *FindSymbol) CallTitle(rawArgs json.RawMessage) (string, error) {
	args, err := tool.DecodeArgs[FindSymbolArgs](rawArgs)
	if err != nil {
		return "", err
	}
	return t.Name() + " " + args.Name, nil
}

func (t *FindSymbol) call(ctx context.Context, args FindSymbolArgs) (FindSymbolResult, error) {
	mod, err := loadModule(ctx, t.cwd)
	if err != nil {
		return FindSymbolResult{}, err
	}

	query := parseSymbolQuery(args.Name)
	pkgFilter := strings.TrimSpace(args.Package)

	var matches []Symbol
	for _, pkg := range mod.packages {
		if pkgFilter != "" && !packageMatches(pkg.PkgPath, pkg.Name, pkgFilter) {
			continue
		}
		for _, sym := range packageSymbols(t.cwd, mod.fset, pkg) {
			if query.matches(sym, pkg.Name) {
				matches = append(matches, sym)
				if len(matches) >= maxMatches {
					return FindSymbolResult{Matches: matches, Warnings: mod.warnings}, nil
				}
			}
		}
	}

	if len(matches) == 0 {
		return FindSymbolResult{}, tool.Error{Code: "not_found", Message: "no symbol named " + args.Name + " in the local module"}
	}

	return FindSymbolResult{Matches: matches, Warnings: mod.warnings}, nil
}

type symbolQuery struct {
	pkg  string
	recv string
	name string
}

func parseSymbolQuery(raw string) symbolQuery {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	switch len(parts) {
	case 1:
		return symbolQuery{name: parts[0]}
	case 2:
		return symbolQuery{pkg: parts[0], recv: parts[0], name: parts[1]}
	default:
		return symbolQuery{pkg: parts[0], recv: parts[1], name: parts[2]}
	}
}

func (q symbolQuery) matches(s Symbol, pkgName string) bool {
	if s.Name != q.name {
		return false
	}

	switch {
	case q.pkg == "" && q.recv == "":
		return true
	case q.pkg != "" && q.recv != "" && q.pkg == q.recv:
		// Two-part query: either package-qualified or a method receiver.
		return pkgName == q.pkg || s.Recv == q.recv
	default:
		// Three-part query: package and receiver both required.
		return pkgName == q.pkg && s.Recv == q.recv
	}
}

func packageMatches(pkgPath, pkgName, filter string) bool {
	return pkgPath == filter || pkgName == filter || strings.HasSuffix(pkgPath, "/"+filter)
}
