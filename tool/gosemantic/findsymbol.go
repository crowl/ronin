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
	Name    string `json:"name" jsonschema:"Declaration to locate. Use a bare name (Tool), package-qualified name (readfile.Tool), receiver method (Args.Validate), or package and receiver method (readfile.Args.Validate). Struct and interface fields are not indexed."`
	Package string `json:"package,omitempty" jsonschema:"Optional package import path or name to restrict the search; useful when a bare symbol name is ambiguous."`
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
	return []tool.Artifact{tool.TextArtifact{Text: findSymbolSummary(r)}}
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
	return "Find a Go declaration in the local module when you know its name. Prefer this over text search for locating types, functions, methods, constants, and variables; use the returned file range with read_file when you need the implementation. Accepts bare, package-qualified, receiver-method, or package-and-receiver-method names and returns all matches when ambiguous. It does not find references, call sites, local declarations, or struct/interface fields."
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
