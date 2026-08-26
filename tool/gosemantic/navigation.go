package gosemantic

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/fsutil"
)

const (
	defaultNavigationLimit = 50
	maxNavigationLimit     = 200
)

type NavigationArgs struct {
	Operation string `json:"operation" jsonschema:"Navigation operation: references, implementations, callers, or callees."`
	Name      string `json:"name,omitempty" jsonschema:"Symbol selector. Accepts the same bare, package-qualified, receiver-method, and package-and-receiver-method forms as find_symbol."`
	Package   string `json:"package,omitempty" jsonschema:"Optional package import path, directory path, or name for a symbol selector."`
	File      string `json:"file,omitempty" jsonschema:"Position selector file. Use with 1-based line and column; the position must identify a Go identifier."`
	Line      int    `json:"line,omitempty" jsonschema:"1-based source line for a position selector."`
	Column    int    `json:"column,omitempty" jsonschema:"1-based byte column for a position selector."`
	Offset    int    `json:"offset,omitempty" jsonschema:"Number of sorted results to skip. Defaults to 0."`
	Limit     int    `json:"limit,omitempty" jsonschema:"Maximum results to return. Defaults to 50 and is capped at 200."`
}

func (a NavigationArgs) Validate() error {
	switch a.Operation {
	case "references", "implementations", "callers", "callees":
	default:
		return tool.Error{Code: "invalid_args", Message: "operation must be references, implementations, callers, or callees"}
	}

	hasName := strings.TrimSpace(a.Name) != ""
	hasPosition := strings.TrimSpace(a.File) != "" || a.Line != 0 || a.Column != 0
	if hasName == hasPosition {
		return tool.Error{Code: "invalid_args", Message: "provide exactly one selector: name (with optional package) or file, line, and column"}
	}
	if hasName {
		if strings.TrimSpace(a.File) != "" || a.Line != 0 || a.Column != 0 {
			return tool.Error{Code: "invalid_args", Message: "name and position selectors cannot be combined"}
		}
		if err := (FindSymbolArgs{Name: a.Name, Package: a.Package}).Validate(); err != nil {
			return err
		}
	} else {
		if strings.TrimSpace(a.File) == "" || a.Line < 1 || a.Column < 1 {
			return tool.Error{Code: "invalid_args", Message: "position selector requires file and positive 1-based line and column"}
		}
		if strings.TrimSpace(a.Package) != "" {
			return tool.Error{Code: "invalid_args", Message: "package is only valid with a name selector"}
		}
	}
	if a.Offset < 0 || a.Limit < 0 {
		return tool.Error{Code: "invalid_args", Message: "offset and limit must not be negative"}
	}
	if strings.ContainsRune(a.Name, '\x00') || strings.ContainsRune(a.Package, '\x00') || strings.ContainsRune(a.File, '\x00') {
		return tool.Error{Code: "invalid_args", Message: "arguments must not contain NUL bytes"}
	}
	return nil
}

type NavigationTarget struct {
	Name    string `json:"name"`
	Package string `json:"package,omitempty"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
}

type NavigationLocation struct {
	File             string  `json:"file"`
	Line             int     `json:"line"`
	Column           int     `json:"column"`
	EndLine          int     `json:"end_line,omitempty"`
	EndColumn        int     `json:"end_column,omitempty"`
	Enclosing        *Symbol `json:"enclosing,omitempty"`
	Related          *Symbol `json:"related,omitempty"`
	DynamicCandidate bool    `json:"dynamic_candidate,omitempty"`
}

type NavigationResult struct {
	Operation  string               `json:"operation"`
	Target     NavigationTarget     `json:"target"`
	Locations  []NavigationLocation `json:"locations"`
	Candidates []Symbol             `json:"candidates,omitempty"`
	Total      int                  `json:"total"`
	Offset     int                  `json:"offset"`
	HasMore    bool                 `json:"has_more"`
	Warnings   []string             `json:"warnings,omitempty"`
}

func (r NavigationResult) Artifacts() []tool.Artifact {
	return []tool.Artifact{tool.TextArtifact{Text: navigationSummary(r)}}
}

type Navigation struct {
	cwd string
}

func NewNavigation(cwd string) *Navigation {
	return &Navigation{cwd: cwd}
}

func (t *Navigation) Name() string {
	return "go_navigation"
}

func (t *Navigation) Description() string {
	return "Navigate Go code semantically by symbol or 1-based file position. Finds references, interface implementations, callers, or callees across local modules; callers and callees conservatively include possible interface-dispatched targets marked dynamic_candidate. Results include exact positions and enclosing declaration ranges for targeted read_file calls. Prefer this over text search for supported Go navigation queries."
}

func (t *Navigation) Parameters() *jsonschema.Schema {
	return jsonschema.FromType[NavigationArgs]()
}

func (t *Navigation) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	return tool.CallTyped(ctx, raw, t.call)
}

func (t *Navigation) CallTitle(rawArgs json.RawMessage) (string, error) {
	args, err := tool.DecodeArgs[NavigationArgs](rawArgs)
	if err != nil {
		return "", err
	}
	selector := args.Name
	if selector == "" {
		selector = fmt.Sprintf("%s:%d:%d", args.File, args.Line, args.Column)
	}
	return t.Name() + " " + args.Operation + " " + selector, nil
}

func (t *Navigation) call(ctx context.Context, args NavigationArgs) (NavigationResult, error) {
	mod, err := loadSemanticModule(ctx, t.cwd)
	if err != nil {
		return NavigationResult{}, err
	}

	target, candidates, err := resolveNavigationTarget(t.cwd, mod, args)
	if err != nil {
		return NavigationResult{}, err
	}
	if len(candidates) > 1 {
		return NavigationResult{
			Operation:  args.Operation,
			Candidates: candidates,
			Total:      len(candidates),
			Warnings:   mod.warnings,
		}, nil
	}

	var locations []NavigationLocation
	switch args.Operation {
	case "references":
		locations = referenceLocations(t.cwd, mod, target.object)
	case "implementations":
		locations, err = implementationLocations(t.cwd, mod, target.object)
	case "callers", "callees":
		locations, err = callLocations(t.cwd, mod, target.object, args.Operation)
	}
	if err != nil {
		return NavigationResult{}, err
	}

	sortNavigationLocations(locations)
	total := len(locations)
	offset := min(args.Offset, total)
	limit := args.Limit
	if limit == 0 {
		limit = defaultNavigationLimit
	}
	limit = min(limit, maxNavigationLimit)
	end := min(offset+limit, total)

	return NavigationResult{
		Operation: args.Operation,
		Target:    target.target,
		Locations: locations[offset:end],
		Total:     total,
		Offset:    offset,
		HasMore:   end < total,
		Warnings:  mod.warnings,
	}, nil
}

type resolvedTarget struct {
	object types.Object
	target NavigationTarget
}

func resolveNavigationTarget(cwd string, mod *module, args NavigationArgs) (resolvedTarget, []Symbol, error) {
	if args.Name != "" {
		return resolveTargetByName(cwd, mod, args)
	}
	return resolveTargetByPosition(cwd, mod, args)
}

func resolveTargetByName(cwd string, mod *module, args NavigationArgs) (resolvedTarget, []Symbol, error) {
	query := parseSymbolQuery(args.Name)
	pkgFilter := strings.TrimSpace(args.Package)

	type match struct {
		symbol Symbol
		object types.Object
	}
	var matches []match
	seen := make(map[string]bool)
	for _, pkg := range mod.packages {
		if pkg.TypesInfo == nil {
			continue
		}
		if pkgFilter != "" && !packageMatches(cwd, pkg, pkgFilter) {
			continue
		}
		for _, symbol := range packageSymbols(cwd, mod.fset, pkg) {
			if !query.matches(cwd, symbol, pkg) {
				continue
			}
			if obj := objectForSymbol(cwd, mod.fset, pkg, symbol); obj != nil {
				key := relatedName(&symbol)
				if seen[key] {
					continue
				}
				seen[key] = true
				matches = append(matches, match{symbol: symbol, object: obj})
			}
		}
	}
	if len(matches) == 0 {
		return resolvedTarget{}, nil, tool.Error{Code: "not_found", Message: "no semantic symbol named " + args.Name + " in the local module"}
	}
	if len(matches) > 1 {
		candidates := make([]Symbol, 0, len(matches))
		for _, match := range matches {
			candidates = append(candidates, match.symbol)
		}
		return resolvedTarget{}, candidates, nil
	}
	m := matches[0]
	return resolvedTarget{object: m.object, target: navigationTarget(cwd, mod.fset, m.object)}, []Symbol{m.symbol}, nil
}

func objectForSymbol(cwd string, fset *token.FileSet, pkg *packages.Package, symbol Symbol) types.Object {
	for ident, obj := range pkg.TypesInfo.Defs {
		if obj == nil || ident.Name != symbol.Name {
			continue
		}
		pos := fset.Position(ident.Pos())
		if filepath.ToSlash(pos.Filename) == "" || pos.Line < symbol.StartLine || pos.Line > symbol.EndLine {
			continue
		}
		if displayFile(cwd, fset, ident.Pos()) == symbol.File {
			return obj
		}
	}
	return nil
}

func resolveTargetByPosition(cwd string, mod *module, args NavigationArgs) (resolvedTarget, []Symbol, error) {
	resolved, err := fsutil.ResolvePath(cwd, args.File)
	if err != nil {
		return resolvedTarget{}, nil, tool.Error{Code: "invalid_args", Message: "resolve position file: " + err.Error()}
	}
	wanted := filepath.Clean(resolved.Abs)

	for _, pkg := range mod.packages {
		if pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			filename := filepath.Clean(mod.fset.Position(file.Pos()).Filename)
			if filename != wanted {
				continue
			}
			var found *ast.Ident
			ast.Inspect(file, func(node ast.Node) bool {
				ident, ok := node.(*ast.Ident)
				if !ok {
					return true
				}
				start := mod.fset.Position(ident.Pos())
				end := mod.fset.Position(ident.End())
				if start.Line == args.Line && args.Column >= start.Column && args.Column < end.Column {
					found = ident
					return false
				}
				return true
			})
			if found == nil {
				return resolvedTarget{}, nil, tool.Error{Code: "not_found", Message: "position does not identify a Go identifier"}
			}
			obj := pkg.TypesInfo.ObjectOf(found)
			if obj == nil {
				return resolvedTarget{}, nil, tool.Error{Code: "not_found", Message: "identifier has no semantic object"}
			}
			return resolvedTarget{object: obj, target: navigationTarget(cwd, mod.fset, obj)}, nil, nil
		}
	}
	return resolvedTarget{}, nil, tool.Error{Code: "not_found", Message: "position file is not part of a loaded local Go package"}
}

func navigationTarget(cwd string, fset *token.FileSet, obj types.Object) NavigationTarget {
	pos := fset.Position(obj.Pos())
	pkgPath := ""
	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
	}
	return NavigationTarget{
		Name:    obj.Name(),
		Package: pkgPath,
		File:    fsutil.DisplayPath(cwd, pos.Filename),
		Line:    pos.Line,
		Column:  pos.Column,
	}
}

func referenceLocations(cwd string, mod *module, target types.Object) []NavigationLocation {
	var locations []NavigationLocation
	for _, pkg := range mod.packages {
		if pkg.TypesInfo == nil {
			continue
		}
		for ident, obj := range pkg.TypesInfo.Uses {
			if sameObject(obj, target) {
				locations = append(locations, locationAt(cwd, mod, ident.Pos(), ident.End(), nil, false))
			}
		}
	}
	return dedupeNavigationLocations(locations)
}

func implementationLocations(cwd string, mod *module, target types.Object) ([]NavigationLocation, error) {
	iface, method, ok := selectedInterface(mod, target)
	if !ok {
		return nil, tool.Error{Code: "unsupported_target", Message: "implementations requires an interface type or interface method"}
	}

	var locations []NavigationLocation
	for _, obj := range implementingObjects(mod, iface, method) {
		related := symbolForObject(cwd, mod, obj)
		locations = append(locations, locationAt(cwd, mod, obj.Pos(), obj.Pos()+token.Pos(len(obj.Name())), related, false))
	}
	return dedupeNavigationLocations(locations), nil
}

func implementingObjects(mod *module, iface *types.Interface, method *types.Func) []types.Object {
	var objects []types.Object
	for _, pkg := range mod.packages {
		if pkg.Types == nil {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			typeName, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := types.Unalias(typeName.Type()).(*types.Named)
			if !ok {
				continue
			}
			if _, isInterface := named.Underlying().(*types.Interface); isInterface {
				continue
			}

			candidate := types.Type(named)
			if !types.Implements(candidate, iface) && types.Implements(types.NewPointer(named), iface) {
				candidate = types.NewPointer(named)
			} else if !types.Implements(candidate, iface) {
				continue
			}

			obj := types.Object(typeName)
			if method != nil {
				selection := types.NewMethodSet(candidate).Lookup(method.Pkg(), method.Name())
				if selection == nil {
					continue
				}
				obj = selection.Obj()
			}
			objects = append(objects, obj)
		}
	}
	return objects
}

func selectedInterface(mod *module, target types.Object) (*types.Interface, *types.Func, bool) {
	if typeName, ok := target.(*types.TypeName); ok {
		if iface, ok := types.Unalias(typeName.Type()).Underlying().(*types.Interface); ok {
			return iface.Complete(), nil, true
		}
	}
	method, ok := target.(*types.Func)
	if !ok {
		return nil, nil, false
	}
	for _, pkg := range mod.packages {
		if pkg.Types == nil {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			typeName, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			iface, ok := types.Unalias(typeName.Type()).Underlying().(*types.Interface)
			if !ok {
				continue
			}
			iface = iface.Complete()
			for i := range iface.NumMethods() {
				if sameObject(iface.Method(i), method) {
					return iface, iface.Method(i), true
				}
			}
		}
	}
	return nil, nil, false
}

func callLocations(cwd string, mod *module, target types.Object, operation string) ([]NavigationLocation, error) {
	targetFunc, ok := target.(*types.Func)
	if !ok {
		return nil, tool.Error{Code: "unsupported_target", Message: operation + " requires a function or method"}
	}

	wellTyped := make([]*packages.Package, 0, len(mod.packages))
	for _, pkg := range mod.packages {
		if pkg.Types != nil && pkg.TypesInfo != nil && len(pkg.Syntax) > 0 && !pkg.IllTyped {
			wellTyped = append(wellTyped, pkg)
		}
	}
	if len(wellTyped) == 0 {
		return nil, tool.Error{Code: "analysis_failed", Message: "no well-typed local packages available for call analysis"}
	}

	prog, _ := ssautil.AllPackages(wellTyped, 0)
	prog.Build()
	graph := cha.CallGraph(prog)

	var locations []NavigationLocation
	for _, node := range graph.Nodes {
		if node == nil || node.Func == nil {
			continue
		}
		if operation == "callees" && functionMatches(node.Func, targetFunc) {
			for _, edge := range node.Out {
				if edge.Site != nil && edge.Site.Common().IsInvoke() && edge.Site.Common().Method != nil {
					iface, ok := types.Unalias(edge.Site.Common().Value.Type()).Underlying().(*types.Interface)
					if ok {
						method := edge.Site.Common().Method
						for _, obj := range implementingObjects(mod, iface, method) {
							related := symbolForObject(cwd, mod, obj)
							locations = append(locations, locationAt(cwd, mod, edge.Pos(), edge.Pos(), related, true))
						}
						continue
					}
				}
				if edge.Callee != nil {
					locations = appendCallEdge(cwd, mod, locations, edge, edge.Callee.Func)
				}
			}
		}
		if operation == "callers" {
			for _, edge := range node.Out {
				if edge.Callee != nil && (functionMatches(edge.Callee.Func, targetFunc) || invokeMatches(edge, targetFunc)) {
					locations = appendCallEdge(cwd, mod, locations, edge, edge.Caller.Func)
				}
			}
		}
	}
	return dedupeNavigationLocations(locations), nil
}

func appendCallEdge(cwd string, mod *module, locations []NavigationLocation, edge *callgraph.Edge, relatedFunc *ssa.Function) []NavigationLocation {
	if edge == nil || edge.Site == nil || relatedFunc == nil {
		return locations
	}
	pos := edge.Pos()
	if !localPosition(mod, pos) {
		return locations
	}
	related := symbolForFunction(cwd, mod, relatedFunc)
	return append(locations, locationAt(cwd, mod, pos, pos, related, edge.Site.Common().IsInvoke()))
}

func functionMatches(fn *ssa.Function, target *types.Func) bool {
	if fn == nil {
		return false
	}
	obj := fn.Object()
	if obj != nil && sameObject(obj, target) {
		return true
	}
	if origin := fn.Origin(); origin != nil {
		originObj := origin.Object()
		return originObj != nil && sameObject(originObj, target)
	}
	return false
}

func invokeMatches(edge *callgraph.Edge, target *types.Func) bool {
	if edge == nil || edge.Site == nil || !edge.Site.Common().IsInvoke() {
		return false
	}
	return sameObject(edge.Site.Common().Method, target)
}

func sameObject(a, b types.Object) bool {
	if a == nil || b == nil {
		return false
	}
	if a == b {
		return true
	}
	return a.Name() == b.Name() && a.Pkg() != nil && b.Pkg() != nil && a.Pkg().Path() == b.Pkg().Path() && a.Pos() == b.Pos()
}

func localPosition(mod *module, pos token.Pos) bool {
	filename := filepath.Clean(mod.fset.Position(pos).Filename)
	if filename == "." || filename == "" {
		return false
	}
	for _, pkg := range mod.packages {
		for _, file := range pkg.Syntax {
			if filepath.Clean(mod.fset.Position(file.Pos()).Filename) == filename {
				return true
			}
		}
	}
	return false
}

func locationAt(cwd string, mod *module, start, end token.Pos, related *Symbol, dynamic bool) NavigationLocation {
	startPos := mod.fset.Position(start)
	endPos := mod.fset.Position(end)
	return NavigationLocation{
		File:             fsutil.DisplayPath(cwd, startPos.Filename),
		Line:             startPos.Line,
		Column:           startPos.Column,
		EndLine:          endPos.Line,
		EndColumn:        endPos.Column,
		Enclosing:        enclosingSymbol(cwd, mod, start),
		Related:          related,
		DynamicCandidate: dynamic,
	}
}

func enclosingSymbol(cwd string, mod *module, pos token.Pos) *Symbol {
	for _, pkg := range mod.packages {
		for _, file := range pkg.Syntax {
			if pos < file.Pos() || pos > file.End() {
				continue
			}
			for _, decl := range file.Decls {
				if pos < decl.Pos() || pos > decl.End() {
					continue
				}
				symbols := declSymbols(cwd, mod.fset, pkg.PkgPath, decl)
				if len(symbols) > 0 {
					symbol := symbols[0]
					return &symbol
				}
			}
		}
	}
	return nil
}

func symbolForFunction(cwd string, mod *module, fn *ssa.Function) *Symbol {
	if fn == nil {
		return nil
	}
	if obj := fn.Object(); obj != nil {
		if symbol := symbolForObject(cwd, mod, obj); symbol != nil {
			return symbol
		}
	}
	if origin := fn.Origin(); origin != nil && origin != fn {
		if obj := origin.Object(); obj != nil {
			if symbol := symbolForObject(cwd, mod, obj); symbol != nil {
				return symbol
			}
		}
	}
	if fn.Pos().IsValid() {
		return enclosingSymbol(cwd, mod, fn.Pos())
	}
	return nil
}

func symbolForObject(cwd string, mod *module, obj types.Object) *Symbol {
	if obj == nil || !obj.Pos().IsValid() {
		return nil
	}
	for _, pkg := range mod.packages {
		if obj.Pkg() == nil || pkg.PkgPath != obj.Pkg().Path() {
			continue
		}
		for _, symbol := range packageSymbols(cwd, mod.fset, pkg) {
			if symbol.Name != obj.Name() {
				continue
			}
			pos := mod.fset.Position(obj.Pos())
			if fsutil.DisplayPath(cwd, pos.Filename) == symbol.File && pos.Line >= symbol.StartLine && pos.Line <= symbol.EndLine {
				symbol := symbol
				return &symbol
			}
		}
	}
	return nil
}

func sortNavigationLocations(locations []NavigationLocation) {
	sort.Slice(locations, func(i, j int) bool {
		a, b := locations[i], locations[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		if a.DynamicCandidate != b.DynamicCandidate {
			return !a.DynamicCandidate
		}
		return relatedName(a.Related) < relatedName(b.Related)
	})
}

func dedupeNavigationLocations(locations []NavigationLocation) []NavigationLocation {
	sortNavigationLocations(locations)
	out := locations[:0]
	previous := ""
	for _, location := range locations {
		key := fmt.Sprintf("%s:%d:%d:%t:%s", location.File, location.Line, location.Column, location.DynamicCandidate, relatedName(location.Related))
		if key == previous {
			continue
		}
		previous = key
		out = append(out, location)
	}
	return out
}

func relatedName(symbol *Symbol) string {
	if symbol == nil {
		return ""
	}
	return symbol.PkgPath + "." + symbol.Recv + "." + symbol.Name
}

func navigationSummary(result NavigationResult) string {
	var b strings.Builder
	if len(result.Candidates) > 1 {
		fmt.Fprintf(&b, "Symbol selector is ambiguous; choose one of %d declarations:\n", len(result.Candidates))
		for _, candidate := range result.Candidates {
			writeSymbolSummary(&b, "", candidate)
		}
		writeWarningsSummary(&b, result.Warnings)
		return strings.TrimSpace(b.String())
	}

	heading := result.Operation
	if heading != "" {
		heading = strings.ToUpper(heading[:1]) + heading[1:]
	}
	fmt.Fprintf(&b, "%s for %s (%s:%d:%d): %d result(s)", heading, result.Target.Name, result.Target.File, result.Target.Line, result.Target.Column, result.Total)
	if result.HasMore {
		fmt.Fprintf(&b, ", showing %d-%d", result.Offset+1, result.Offset+len(result.Locations))
	}
	b.WriteString("\n")
	for _, location := range result.Locations {
		fmt.Fprintf(&b, "- %s:%d:%d", location.File, location.Line, location.Column)
		if location.DynamicCandidate {
			b.WriteString(" [dynamic candidate]")
		}
		if location.Related != nil {
			fmt.Fprintf(&b, " -> %s", location.Related.Signature)
		}
		b.WriteString("\n")
		if location.Enclosing != nil {
			fmt.Fprintf(&b, "  enclosing range: %s:%d-%d\n", location.Enclosing.File, location.Enclosing.StartLine, location.Enclosing.EndLine)
		}
	}
	writeWarningsSummary(&b, result.Warnings)
	return strings.TrimSpace(b.String())
}
