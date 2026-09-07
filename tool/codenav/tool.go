// Package codenav exposes workspace navigation without shell or write access.
package codenav

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/crowl/ronin/codeindex"
	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/tool"
)

type Args struct {
	Path     string `json:"path,omitempty" jsonschema:"Workspace-relative file or directory using forward slashes. Defaults to the workspace. Paths outside it are rejected."`
	Query    string `json:"query,omitempty" jsonschema:"Search words or symbol name; required for code_find, unused for code_map."`
	Language string `json:"language,omitempty" jsonschema:"Optional language filter: go, typescript, or tsx."`
	Kind     string `json:"kind,omitempty" jsonschema:"Optional declaration kind filter, such as function, method, type, class, interface, variable, or constant."`
	Limit    int    `json:"limit,omitempty" jsonschema:"Maximum results, 1..100; defaults to 40. Narrow path or query when truncated."`
	Depth    int    `json:"depth,omitempty" jsonschema:"Map directory depth relative to path, 1..8; defaults to 2. Unused for find."`
}

func (a Args) Validate() error {
	if len(a.Query) > 256 {
		return fmt.Errorf("query exceeds 256 bytes")
	}
	return nil
}

type Tool struct {
	index *codeindex.Index
	find  bool
}

func NewMap(workspace string) *Tool  { return &Tool{index: codeindex.New(workspace, "")} }
func NewFind(workspace string) *Tool { return &Tool{index: codeindex.New(workspace, ""), find: true} }
func (t *Tool) Name() string {
	if t.find {
		return "code_find"
	}
	return "code_map"
}
func (t *Tool) Description() string {
	if t.find {
		return "Find Go, TypeScript, and TSX code by symbol, path, signature, or source words. Ranked, bounded results include source ranges and SHA-256. All words must match. Refreshes a local index; parser diagnostics do not disable text search. Use read_file for exact current source before editing."
	}
	return "Map Go, TypeScript, and TSX workspace files and declarations without reading full bodies. Use path and depth to drill down; narrow scope if truncated. Includes source ranges, SHA-256, and indexing diagnostics. Respects nested .gitignore rules and excludes dependency/build directories and symlinks."
}
func (t *Tool) Parameters() *jsonschema.Schema { return jsonschema.FromType[Args]() }
func (t *Tool) CallTitle(raw json.RawMessage) (string, error) {
	a, err := tool.DecodeArgs[Args](raw)
	if err != nil {
		return "", err
	}
	return t.Name() + " " + a.Path + " " + a.Query, nil
}
func (t *Tool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	return tool.CallTyped(ctx, raw, t.call)
}

type Result struct{ codeindex.Result }

func (r Result) Artifacts() []tool.Artifact {
	var b strings.Builder
	for _, e := range r.Entries {
		fmt.Fprintf(&b, "%s", e.Path)
		if e.Span != nil {
			fmt.Fprintf(&b, ":%d–%d", e.Span.StartLine, e.Span.EndLine)
		}
		fmt.Fprintf(&b, "  %s %s\n", e.Kind, e.Name)
		if e.Signature != "" {
			fmt.Fprintf(&b, "  %s\n", e.Signature)
		}
		if e.Excerpt != "" {
			fmt.Fprintf(&b, "  %s\n", e.Excerpt)
		}
	}
	if r.Truncated {
		b.WriteString("Results truncated; narrow path/query or increase limit.\n")
	}
	fmt.Fprintf(&b, "Indexed %d files; %d issues.\n", r.Status.Files, r.Status.Issues)
	return []tool.Artifact{tool.TextArtifact{Text: b.String()}}
}
func (t *Tool) call(ctx context.Context, a Args) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	q := codeindex.Query{Path: a.Path, Text: a.Query, Language: a.Language, Kind: a.Kind, Limit: a.Limit, Depth: a.Depth}
	var r codeindex.Result
	var err error
	if t.find {
		r, err = t.index.Find(ctx, q)
	} else {
		r, err = t.index.Map(ctx, q)
	}
	if err != nil {
		return Result{}, fmt.Errorf("%s unavailable; use read_file or shell search: %w", t.Name(), err)
	}
	return Result{r}, nil
}
