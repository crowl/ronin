package codeindex

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crowl/ronin/codeindex/syntax"
)

// Query restricts results to a workspace-relative path. Limit defaults to 40,
// max 100. Depth is relative to Path, defaults to 2, max 8 (map only).
type Query struct {
	Path     string
	Language string
	Kind     string
	Text     string
	Limit    int
	Depth    int
}

type Entry struct {
	Path      string       `json:"path"`
	Language  string       `json:"language,omitempty"`
	Kind      string       `json:"kind"`
	Name      string       `json:"name,omitempty"`
	Container string       `json:"container,omitempty"`
	Signature string       `json:"signature,omitempty"`
	SHA256    string       `json:"sha256,omitempty"`
	Span      *syntax.Span `json:"span,omitempty"`
	Excerpt   string       `json:"excerpt,omitempty"`
	Match     string       `json:"match,omitempty"`
	Files     int          `json:"files,omitempty"`
	score     int
}

type Result struct {
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
	Status    Status  `json:"status"`
}

func normalize(q Query) (Query, error) {
	if q.Path == "" {
		q.Path = "."
	}
	if strings.ContainsAny(q.Path, "\\\x00:") || strings.HasPrefix(q.Path, "/") {
		return q, errors.New("path must be workspace-relative using forward slashes")
	}
	q.Path = path.Clean(q.Path)
	if q.Path == ".." || strings.HasPrefix(q.Path, "../") {
		return q, errors.New("path is outside workspace")
	}
	if q.Limit == 0 {
		q.Limit = 40
	}
	if q.Limit < 1 || q.Limit > 100 {
		return q, errors.New("limit must be 1..100")
	}
	if q.Depth == 0 {
		q.Depth = 2
	}
	if q.Depth < 1 || q.Depth > 8 {
		return q, errors.New("depth must be 1..8")
	}
	if q.Language != "" && q.Language != "go" && q.Language != "typescript" && q.Language != "tsx" {
		return q, errors.New("language must be go, typescript, or tsx")
	}
	if len(q.Path) > 512 {
		return q, errors.New("path exceeds 512 bytes")
	}
	switch q.Kind {
	case "", "file", "text", "function", "method", "type", "class", "interface", "variable", "constant", "enum":
	default:
		return q, errors.New("unsupported kind filter")
	}
	if len(q.Text) > 256 {
		return q, errors.New("query exceeds 256 bytes")
	}
	return q, nil
}

func inScope(file, scope string) bool {
	return scope == "." || file == scope || strings.HasPrefix(file, scope+"/")
}

// Map returns deterministic directory summaries, files and source-backed
// declarations. It never injects source automatically into a conversation.
func (i *Index) Map(ctx context.Context, q Query) (Result, error) {
	q, err := normalize(q)
	if err != nil {
		return Result{}, err
	}
	files, status, err := i.snapshot(ctx)
	if err != nil {
		return Result{}, err
	}
	result := Result{Entries: []Entry{}, Status: status}
	dirs := map[string]int{}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if !inScope(f.Path, q.Path) || (q.Language != "" && f.Language != q.Language) {
			continue
		}
		relative := f.Path
		if q.Path != "." && q.Path != f.Path {
			relative = strings.TrimPrefix(f.Path, q.Path+"/")
		}
		components := strings.Split(relative, "/")
		if q.Path != f.Path && len(components) > q.Depth {
			dir := strings.Join(components[:q.Depth], "/")
			if q.Path != "." {
				dir = path.Join(q.Path, dir)
			}
			dirs[dir]++
			continue
		}
		if q.Kind == "" || q.Kind == "file" {
			addBounded(&result, Entry{Path: f.Path, Language: f.Language, Kind: "file", SHA256: f.SHA256}, q.Limit)
		}
		// Directory maps list files; drill into a file for declarations.
		if q.Kind == "" && q.Path != f.Path {
			continue
		}
		for _, d := range f.Outline.Declarations {
			if q.Kind != "" && q.Kind != d.Kind {
				continue
			}
			addBounded(&result, declarationEntry(f, d), q.Limit)
		}
	}
	var names []string
	for dir := range dirs {
		names = append(names, dir)
	}
	slices.Sort(names)
	for _, dir := range names {
		addBounded(&result, Entry{Path: dir, Kind: "directory", Files: dirs[dir]}, q.Limit)
	}
	slices.SortFunc(result.Entries, entryOrder)
	boundJSON(&result)
	return result, nil
}

// Find ranks exact names, identifier/path matches, then lexical source matches.
// All query terms must match; camelCase and snake_case are split into words.
// Source search remains available on files whose structural extraction failed.
func (i *Index) Find(ctx context.Context, q Query) (Result, error) {
	q, err := normalize(q)
	if err != nil {
		return Result{}, err
	}
	terms := words(q.Text)
	if len(terms) == 0 {
		return Result{}, errors.New("query must contain letters or digits")
	}
	files, status, err := i.snapshot(ctx)
	if err != nil {
		return Result{}, err
	}
	result := Result{Entries: []Entry{}, Status: status}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if !inScope(f.Path, q.Path) || (q.Language != "" && f.Language != q.Language) {
			continue
		}
		if (q.Kind == "" || q.Kind == "file") && allTerms(terms, f.Path) {
			e := Entry{Path: f.Path, Language: f.Language, Kind: "file", SHA256: f.SHA256, Match: "path", score: 70}
			ranked(&result, e, q.Limit)
		}
		for _, d := range f.Outline.Declarations {
			if q.Kind != "" && q.Kind != d.Kind {
				continue
			}
			e := declarationEntry(f, d)
			switch {
			case strings.EqualFold(strings.TrimSpace(q.Text), d.Name):
				e.score = 100
				e.Match = "exact_name"
			case allTerms(terms, d.Name):
				e.score = 90
				e.Match = "name"
			case allTerms(terms, d.Name+" "+d.Signature+" "+d.Container):
				e.score = 60
				e.Match = "signature"
			default:
				continue
			}
			ranked(&result, e, q.Limit)
		}
		if q.Kind != "" && q.Kind != "text" {
			continue
		}
		// Return matching lines rather than copying full declarations into results.
		offset := 0
		for lineNo, line := range strings.Split(f.Source, "\n") {
			if lineNo%128 == 0 {
				if err := ctx.Err(); err != nil {
					return Result{}, err
				}
			}
			if allTerms(terms, line) {
				e := Entry{Path: f.Path, Language: f.Language, Kind: "text", SHA256: f.SHA256, Excerpt: clip(strings.TrimSpace(line), 240), Match: "source", score: 40, Span: &syntax.Span{StartByte: uint(offset), EndByte: uint(offset + len(line)), StartLine: uint(lineNo + 1), EndLine: uint(lineNo + 1)}}
				ranked(&result, e, q.Limit)
			}
			offset += len(line) + 1
		}
	}
	boundJSON(&result)
	return result, nil
}

func declarationEntry(f fileRecord, d syntax.Declaration) Entry {
	return Entry{Path: f.Path, Language: f.Language, Kind: d.Kind, Name: clip(d.Name, 160), Container: clip(d.Container, 160), Signature: clip(d.Signature, 320), SHA256: f.SHA256, Span: &d.Span}
}

// Bound encoded bytes too: JSON escaping can expand source and path text.
func boundJSON(r *Result) {
	for len(r.Entries) > 0 {
		data, _ := json.Marshal(r)
		if len(data) <= 64*1024 {
			return
		}
		r.Entries = r.Entries[:len(r.Entries)-1]
		r.Truncated = true
	}
}

func addBounded(r *Result, e Entry, limit int) {
	if len(r.Entries) < limit {
		r.Entries = append(r.Entries, e)
	} else {
		r.Truncated = true
	}
}
func ranked(r *Result, e Entry, limit int) {
	r.Entries = append(r.Entries, e)
	slices.SortFunc(r.Entries, func(a, b Entry) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return entryOrder(a, b)
	})
	if len(r.Entries) > limit {
		r.Entries = r.Entries[:limit]
		r.Truncated = true
	}
}
func entryOrder(a, b Entry) int {
	if c := strings.Compare(a.Path, b.Path); c != 0 {
		return c
	}
	var x, y uint
	if a.Span != nil {
		x = a.Span.StartByte
	}
	if b.Span != nil {
		y = b.Span.StartByte
	}
	if x < y {
		return -1
	}
	if x > y {
		return 1
	}
	return strings.Compare(a.Kind, b.Kind)
}
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	n -= 3
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}
func words(s string) []string {
	var out []rune
	var prev rune
	for _, r := range s {
		if unicode.IsUpper(r) && unicode.IsLower(prev) {
			out = append(out, ' ')
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, unicode.ToLower(r))
		} else {
			out = append(out, ' ')
		}
		prev = r
	}
	return strings.Fields(string(out))
}
func allTerms(terms []string, text string) bool {
	normalized := strings.Join(words(text), " ")
	raw := strings.ToLower(text)
	for _, term := range terms {
		if !strings.Contains(normalized, term) && !strings.Contains(raw, term) {
			return false
		}
	}
	return true
}
