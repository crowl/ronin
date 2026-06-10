package gosemantic

import (
	"go/ast"
	"go/printer"
	"go/token"
	"strings"
	"unicode"

	"golang.org/x/tools/go/packages"

	"github.com/crowl/ronin/tool/fsutil"
)

type symbolKind string

const (
	kindType   symbolKind = "type"
	kindFunc   symbolKind = "func"
	kindMethod symbolKind = "method"
	kindConst  symbolKind = "const"
	kindVar    symbolKind = "var"
)

// Symbol is a located declaration in the module. The range covers the full
// declaration including its doc comment so a follow-up read_file is trivial.
type Symbol struct {
	Name      string     `json:"name"`
	Kind      symbolKind `json:"kind"`
	Recv      string     `json:"recv,omitempty"`
	PkgPath   string     `json:"package"`
	File      string     `json:"file"`
	StartLine int        `json:"start_line"`
	EndLine   int        `json:"end_line"`
	Signature string     `json:"signature"`
	Doc       string     `json:"doc,omitempty"`
}

func exported(s Symbol) bool {
	if s.Recv != "" && !isExportedName(s.Recv) {
		return false
	}
	return isExportedName(s.Name)
}

func isExportedName(name string) bool {
	if name == "" {
		return false
	}
	r := []rune(name)[0]
	return unicode.IsUpper(r)
}

// packageSymbols extracts every top-level declaration in a package. cwd
// controls how file paths are displayed (relative to the conversation's working
// directory, matching read_file).
func packageSymbols(cwd string, fset *token.FileSet, pkg *packages.Package) []Symbol {
	var symbols []Symbol
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			symbols = append(symbols, declSymbols(cwd, fset, pkg.PkgPath, decl)...)
		}
	}
	return symbols
}

func declSymbols(cwd string, fset *token.FileSet, pkgPath string, decl ast.Decl) []Symbol {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return []Symbol{funcSymbol(cwd, fset, pkgPath, d)}
	case *ast.GenDecl:
		return genDeclSymbols(cwd, fset, pkgPath, d)
	default:
		return nil
	}
}

func funcSymbol(cwd string, fset *token.FileSet, pkgPath string, fn *ast.FuncDecl) Symbol {
	kind := kindFunc
	recv := ""
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		kind = kindMethod
		recv = receiverTypeName(fn.Recv.List[0].Type)
	}

	signature := &ast.FuncDecl{
		Recv: fn.Recv,
		Name: fn.Name,
		Type: fn.Type,
	}

	start, end := declRange(fset, fn, fn.Doc)
	return Symbol{
		Name:      fn.Name.Name,
		Kind:      kind,
		Recv:      recv,
		PkgPath:   pkgPath,
		File:      displayFile(cwd, fset, fn.Pos()),
		StartLine: start,
		EndLine:   end,
		Signature: render(fset, signature),
		Doc:       docText(fn.Doc),
	}
}

func genDeclSymbols(cwd string, fset *token.FileSet, pkgPath string, d *ast.GenDecl) []Symbol {
	var symbols []Symbol
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			start, end := declRange(fset, d, specDoc(d, s.Doc))
			symbols = append(symbols, Symbol{
				Name:      s.Name.Name,
				Kind:      kindType,
				PkgPath:   pkgPath,
				File:      displayFile(cwd, fset, s.Pos()),
				StartLine: start,
				EndLine:   end,
				Signature: typeSignature(fset, s),
				Doc:       docText(specDoc(d, s.Doc)),
			})
		case *ast.ValueSpec:
			kind := kindVar
			if d.Tok == token.CONST {
				kind = kindConst
			}
			start, end := declRange(fset, d, specDoc(d, s.Doc))
			for _, name := range s.Names {
				symbols = append(symbols, Symbol{
					Name:      name.Name,
					Kind:      kind,
					PkgPath:   pkgPath,
					File:      displayFile(cwd, fset, name.Pos()),
					StartLine: start,
					EndLine:   end,
					Signature: valueSignature(fset, kind, name, s),
					Doc:       docText(specDoc(d, s.Doc)),
				})
			}
		}
	}
	return symbols
}

func typeSignature(fset *token.FileSet, s *ast.TypeSpec) string {
	head := "type " + s.Name.Name + typeParams(fset, s.TypeParams)
	if s.Assign.IsValid() {
		head += " ="
	}
	switch s.Type.(type) {
	case *ast.StructType:
		return head + " struct{ ... }"
	case *ast.InterfaceType:
		return head + " interface{ ... }"
	default:
		return head + " " + render(fset, s.Type)
	}
}

func valueSignature(fset *token.FileSet, kind symbolKind, name *ast.Ident, s *ast.ValueSpec) string {
	var b strings.Builder
	b.WriteString(string(kind))
	b.WriteString(" ")
	b.WriteString(name.Name)
	if s.Type != nil {
		b.WriteString(" ")
		b.WriteString(render(fset, s.Type))
	}
	return b.String()
}

func typeParams(fset *token.FileSet, params *ast.FieldList) string {
	if params == nil || len(params.List) == 0 {
		return ""
	}
	return render(fset, params)
}

func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	default:
		return ""
	}
}

// declRange returns the 1-based line range covering a declaration, extended
// upward to include its doc comment when present.
func declRange(fset *token.FileSet, node ast.Node, doc *ast.CommentGroup) (int, int) {
	start := node.Pos()
	if doc != nil {
		start = doc.Pos()
	}
	return fset.Position(start).Line, fset.Position(node.End()).Line
}

func displayFile(cwd string, fset *token.FileSet, pos token.Pos) string {
	abs := fset.Position(pos).Filename
	return fsutil.DisplayPath(cwd, abs)
}

func docText(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	return strings.TrimSpace(doc.Text())
}

// specDoc falls back to the GenDecl doc comment for single-spec blocks, where
// the comment attaches to the declaration rather than the spec.
func specDoc(d *ast.GenDecl, ownDoc *ast.CommentGroup) *ast.CommentGroup {
	if ownDoc != nil {
		return ownDoc
	}
	if len(d.Specs) == 1 {
		return d.Doc
	}
	return nil
}

func render(fset *token.FileSet, node ast.Node) string {
	var b strings.Builder
	cfg := printer.Config{Mode: printer.UseSpaces, Tabwidth: 4}
	if err := cfg.Fprint(&b, fset, node); err != nil {
		return ""
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
