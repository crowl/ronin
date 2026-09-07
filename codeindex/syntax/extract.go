// Package syntax extracts source-backed declarations using pinned Tree-sitter grammars.
package syntax

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	ts "github.com/tree-sitter/go-tree-sitter"
	goGrammar "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tsGrammar "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

//go:embed queries/go.scm
var goQuery string

//go:embed queries/typescript.scm
var typescriptQuery string

// Span uses zero-based, half-open byte offsets and one-based inclusive lines.
// Columns are intentionally omitted: Tree-sitter columns count bytes, not runes.
type Span struct {
	StartByte uint `json:"start_byte"`
	EndByte   uint `json:"end_byte"`
	StartLine uint `json:"start_line"`
	EndLine   uint `json:"end_line"`
}

type Declaration struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Container string `json:"container,omitempty"`
	Signature string `json:"signature"`
	Span      Span   `json:"span"`
}

type Diagnostic struct {
	Kind string `json:"kind"`
	Span Span   `json:"span"`
}

type Outline struct {
	Language     string        `json:"language"`
	SHA256       string        `json:"sha256"`
	Declarations []Declaration `json:"declarations"`
	Diagnostics  []Diagnostic  `json:"diagnostics,omitempty"`
}

// Extractor owns native parser/query resources. Use one per worker; it is not
// safe for concurrent use. Close it after use. Outlines own their data and
// remain valid after subsequent parses or Close.
type Extractor struct {
	language string
	parser   *ts.Parser
	query    *ts.Query
}

// Language returns the supported grammar name for a path, or an empty string.
func Language(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts", ".mts", ".cts":
		return "typescript"
	case ".tsx":
		return "tsx"
	default:
		return ""
	}
}

func New(language string) (*Extractor, error) {
	var grammar *ts.Language
	querySource := typescriptQuery
	switch language {
	case "go":
		grammar = ts.NewLanguage(goGrammar.Language())
		querySource = goQuery
	case "typescript":
		grammar = ts.NewLanguage(tsGrammar.LanguageTypescript())
	case "tsx":
		grammar = ts.NewLanguage(tsGrammar.LanguageTSX())
	default:
		return nil, fmt.Errorf("unsupported language %q", language)
	}
	parser := ts.NewParser()
	if err := parser.SetLanguage(grammar); err != nil {
		parser.Close()
		return nil, fmt.Errorf("load %s grammar: %w", language, err)
	}
	query, err := ts.NewQuery(grammar, querySource)
	if err != nil {
		parser.Close()
		return nil, fmt.Errorf("compile %s query: %w", language, *err)
	}
	return &Extractor{language: language, parser: parser, query: query}, nil
}

func (e *Extractor) Close() {
	e.query.Close()
	e.parser.Close()
}

// Extract returns syntactic declarations and ERROR/MISSING diagnostics. Syntax
// errors do not make extraction fail. Cancellation returns no partial outline.
func (e *Extractor) Extract(ctx context.Context, source []byte) (Outline, error) {
	if err := ctx.Err(); err != nil {
		return Outline{}, err
	}
	// Reset both before and after, so a canceled parse cannot resume against
	// the next file's contents.
	e.parser.Reset()
	defer e.parser.Reset()
	// The experimental binding pin includes upstream PR #56, which releases
	// the progress callback handle when ParseWithOptions returns.
	tree := e.parser.ParseWithOptions(func(offset int, _ ts.Point) []byte {
		if offset >= len(source) {
			return nil
		}
		return source[offset:min(offset+32*1024, len(source))]
	}, nil, &ts.ParseOptions{
		ProgressCallback: func(ts.ParseState) bool { return ctx.Err() != nil },
	})
	if tree == nil {
		if err := ctx.Err(); err != nil {
			return Outline{}, err
		}
		return Outline{}, fmt.Errorf("parser returned no tree")
	}
	defer tree.Close()
	if err := ctx.Err(); err != nil {
		return Outline{}, err
	}
	root := tree.RootNode()
	result := Outline{Language: e.language, SHA256: fmt.Sprintf("%x", sha256.Sum256(source)), Declarations: []Declaration{}}
	cursor := ts.NewQueryCursor()
	defer cursor.Close()
	cursor.SetTimeoutMicros(50_000)
	queryStarted := time.Now()
	matches := cursor.Matches(e.query, root, source)
	captureNames := e.query.CaptureNames()
	for match := matches.Next(); match != nil; match = matches.Next() {
		if err := ctx.Err(); err != nil {
			return Outline{}, err
		}
		var name, declaration *ts.Node
		var kind string
		for _, capture := range match.Captures {
			node := capture.Node
			if captureNames[capture.Index] == "name" {
				name = &node
			} else {
				declaration = &node
				kind = captureNames[capture.Index]
			}
		}
		if name == nil || declaration == nil || insideFunction(declaration) {
			continue
		}
		if declaration.Kind() == "variable_declarator" {
			if value := declaration.ChildByFieldName("value"); value != nil && (value.Kind() == "arrow_function" || value.Kind() == "function_expression") {
				kind = "function"
			}
		}
		if len(result.Declarations) >= 4000 {
			return Outline{}, fmt.Errorf("file exceeds 4000 declaration limit")
		}
		result.Declarations = append(result.Declarations, Declaration{
			Name: bounded(name.Utf8Text(source)), Kind: kind,
			Container: bounded(container(declaration, source)),
			Signature: signature(declaration, source), Span: span(declaration),
		})
	}
	// Query timeout must not silently produce an apparently complete outline.
	if cursor.DidExceedMatchLimit() {
		return Outline{}, fmt.Errorf("query match limit exceeded")
	}
	// The binding exposes no timeout flag. Conservatively reject any query
	// whose wall time reached the native budget, even if it completed.
	if time.Since(queryStarted) >= 50*time.Millisecond {
		return Outline{}, fmt.Errorf("query exceeded 50ms budget")
	}
	if err := diagnostics(ctx, root, &result.Diagnostics); err != nil {
		return Outline{}, err
	}
	slices.SortFunc(result.Declarations, func(a, b Declaration) int {
		if a.Span.StartByte < b.Span.StartByte {
			return -1
		}
		if a.Span.StartByte > b.Span.StartByte {
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return result, nil
}

func insideFunction(node *ts.Node) bool {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Kind() {
		case "function_declaration", "method_declaration", "method_definition", "arrow_function", "function_expression", "func_literal":
			return true
		}
	}
	return false
}

func container(node *ts.Node, source []byte) string {
	if receiver := node.ChildByFieldName("receiver"); receiver != nil {
		return receiver.Utf8Text(source)
	}
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Kind() {
		case "class_declaration", "abstract_class_declaration", "interface_declaration":
			if name := parent.ChildByFieldName("name"); name != nil {
				return name.Utf8Text(source)
			}
		}
	}
	return ""
}

func signature(node *ts.Node, source []byte) string {
	end := node.EndByte()
	if body := node.ChildByFieldName("body"); body != nil {
		end = body.StartByte()
	} else if node.Kind() == "variable_declarator" {
		if value := node.ChildByFieldName("value"); value != nil {
			if body := value.ChildByFieldName("body"); body != nil {
				end = body.StartByte()
			}
		}
	} else if node.Kind() == "type_spec" {
		if typ := node.ChildByFieldName("type"); typ != nil {
			for i := uint(0); i < typ.NamedChildCount(); i++ {
				child := typ.NamedChild(i)
				if child.Kind() == "field_declaration_list" {
					end = child.StartByte()
					break
				}
			}
		}
	}
	return bounded(strings.TrimSpace(string(source[node.StartByte():end])))
}

func bounded(s string) string {
	if len(s) <= 2048 {
		return s
	}
	n := 2045
	for !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

func span(node *ts.Node) Span {
	end := node.EndPosition()
	endLine := end.Row + 1
	if end.Column == 0 && end.Row > node.StartPosition().Row {
		endLine--
	}
	return Span{StartByte: node.StartByte(), EndByte: node.EndByte(), StartLine: node.StartPosition().Row + 1, EndLine: endLine}
}

func diagnostics(ctx context.Context, root *ts.Node, out *[]Diagnostic) error {
	cursor := root.Walk()
	defer cursor.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(*out) >= 100 {
			return nil
		}
		node := cursor.Node()
		if node.IsError() {
			*out = append(*out, Diagnostic{Kind: "ERROR", Span: span(node)})
		}
		if node.IsMissing() {
			*out = append(*out, Diagnostic{Kind: "MISSING", Span: span(node)})
		}
		if cursor.GotoFirstChild() {
			continue
		}
		for !cursor.GotoNextSibling() {
			if !cursor.GotoParent() {
				return nil
			}
		}
	}
}
