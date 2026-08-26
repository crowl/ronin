package gosemantic

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/tool"
)

const maxMatches = 50

type FindSymbolArgs struct {
	Name    string `json:"name" jsonschema:"Declaration to locate. Use a bare name (Tool), package-qualified name (readfile.Tool), receiver method (Args.Validate), or package and receiver method (readfile.Args.Validate). Struct and interface fields are not indexed."`
	Package string `json:"package,omitempty" jsonschema:"Optional package import path, directory path, or name to restrict the search; useful when a bare symbol name is ambiguous."`
}

func (a FindSymbolArgs) Validate() error {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return tool.Error{Code: "invalid_args", Message: "name must not be empty"}
	}
	if strings.ContainsRune(a.Name, '\x00') || strings.ContainsRune(a.Package, '\x00') {
		return tool.Error{Code: "invalid_args", Message: "arguments must not contain NUL bytes"}
	}
	q := parseSymbolQuery(name)
	if q.name == "" {
		return tool.Error{Code: "invalid_args", Message: "symbol name must not be empty"}
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
	return "Locate a Go declaration and return its signature plus the exact file and 1-based line range for a targeted read_file call. Use this before text search when you know the name of a type, function, method, constant, or variable. Accepts bare, package-qualified, receiver-method, or package-and-receiver-method names and returns all matches when ambiguous. It does not find references, call sites, local declarations, or struct/interface fields; use text search for those queries."
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
		if pkgFilter != "" && !packageMatches(t.cwd, pkg, pkgFilter) {
			continue
		}
		for _, sym := range packageSymbols(t.cwd, mod.fset, pkg) {
			if query.matches(t.cwd, sym, pkg) {
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
	raw = strings.TrimSpace(raw)
	lastDot := strings.LastIndex(raw, ".")
	if lastDot == -1 {
		return symbolQuery{name: raw}
	}

	name := strings.TrimSpace(raw[lastDot+1:])
	prefix := strings.TrimSpace(raw[:lastDot])

	if strings.Contains(prefix, "*") || strings.Contains(prefix, "(") {
		prefixDot := strings.LastIndex(prefix, ".")
		if prefixDot == -1 {
			recv := cleanReceiver(prefix)
			return symbolQuery{pkg: recv, recv: recv, name: name}
		}
		pkg := strings.TrimSpace(prefix[:prefixDot])
		recv := cleanReceiver(prefix[prefixDot+1:])
		return symbolQuery{pkg: pkg, recv: recv, name: name}
	}

	prefixDot := strings.LastIndex(prefix, ".")
	if prefixDot == -1 {
		if strings.Contains(prefix, "/") {
			return symbolQuery{pkg: prefix, name: name}
		}
		return symbolQuery{pkg: prefix, recv: prefix, name: name}
	}

	pLeft := strings.TrimSpace(prefix[:prefixDot])
	pRight := strings.TrimSpace(prefix[prefixDot+1:])

	if strings.Contains(pRight, "/") {
		return symbolQuery{pkg: prefix, name: name}
	}

	return symbolQuery{pkg: pLeft, recv: pRight, name: name}
}

func cleanReceiver(r string) string {
	r = strings.TrimSpace(r)
	r = strings.TrimPrefix(r, "(")
	r = strings.TrimSuffix(r, ")")
	r = strings.TrimPrefix(r, "*")
	return strings.TrimSpace(r)
}

func (q symbolQuery) matches(cwd string, s Symbol, pkg *packages.Package) bool {
	if s.Name != q.name {
		return false
	}

	switch {
	case q.pkg == "" && q.recv == "":
		return true
	case q.pkg != "" && q.recv != "" && q.pkg == q.recv:
		// Two-part query: either package-qualified or a method receiver.
		return packageMatches(cwd, pkg, q.pkg) || s.Recv == q.recv
	default:
		if q.pkg != "" && !packageMatches(cwd, pkg, q.pkg) {
			return false
		}
		if q.recv != "" && s.Recv != q.recv {
			return false
		}
		return true
	}
}

func normalizePackageSpec(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(spec))
}

func packageMatches(cwd string, pkg *packages.Package, filter string) bool {
	filter = normalizePackageSpec(filter)
	if filter == "" {
		return false
	}

	if pkg.PkgPath == filter {
		return true
	}
	if pkg.Name == filter {
		return true
	}
	if strings.HasSuffix(pkg.PkgPath, "/"+filter) {
		return true
	}

	dir := packageDir(pkg)
	if dir == "" {
		return false
	}

	if filepath.IsAbs(filter) {
		return filepath.Clean(dir) == filepath.Clean(filter)
	}

	if cwd != "" {
		absCWD, err := filepath.Abs(cwd)
		if err != nil {
			absCWD = cwd
		}
		absFilter := filepath.Join(absCWD, filter)
		if filepath.Clean(dir) == filepath.Clean(absFilter) {
			return true
		}

		if rel, err := filepath.Rel(absCWD, dir); err == nil {
			relSlash := filepath.ToSlash(filepath.Clean(rel))
			filterSlash := filepath.ToSlash(filepath.Clean(filter))
			if filterSlash != "." && strings.HasSuffix(relSlash, "/"+filterSlash) {
				return true
			}
		}
	}

	return false
}

func packageDir(pkg *packages.Package) string {
	if len(pkg.GoFiles) > 0 {
		return filepath.Dir(pkg.GoFiles[0])
	}
	if len(pkg.CompiledGoFiles) > 0 {
		return filepath.Dir(pkg.CompiledGoFiles[0])
	}
	if len(pkg.Syntax) > 0 && pkg.Fset != nil {
		pos := pkg.Syntax[0].Pos()
		if pos.IsValid() {
			filename := pkg.Fset.Position(pos).Filename
			if filename != "" {
				return filepath.Dir(filename)
			}
		}
	}
	return ""
}
