package syntax_test

import (
	"runtime"
	"strings"
	"testing"
	"time"
	"weak"

	ts "github.com/tree-sitter/go-tree-sitter"
	goGrammar "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

// Test the pinned binding contract directly: the extractor's context callback
// must run during native parsing and must not remain rooted in a handle registry
// after either a completed or canceled parse. Weak references avoid finalizers
// and allocation-rate/RSS heuristics.
func TestParserCallbackOwnership(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		name := "complete"
		if cancel {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			parser := ts.NewParser()
			defer parser.Close()
			if err := parser.SetLanguage(ts.NewLanguage(goGrammar.Language())); err != nil {
				t.Fatal(err)
			}
			source := []byte("package p\n" + strings.Repeat("func Item() { println(1) }\n", 2000))
			ref, called, gotTree := parseWithTrackedOptions(parser, source, cancel)
			if !called {
				t.Fatal("native progress callback never ran")
			}
			if gotTree == cancel {
				t.Fatalf("tree returned = %v, cancel = %v", gotTree, cancel)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				runtime.GC()
				if ref.Value() == nil {
					return
				}
				runtime.Gosched()
			}
			t.Fatal("parse options remain reachable after parse returned")
		})
	}
}

// Keep options creation and the last strong reference outside the GC loop.
//
//go:noinline
func parseWithTrackedOptions(parser *ts.Parser, source []byte, cancel bool) (weak.Pointer[ts.ParseOptions], bool, bool) {
	called := false
	options := &ts.ParseOptions{ProgressCallback: func(ts.ParseState) bool { called = true; return cancel }}
	ref := weak.Make(options)
	tree := parser.ParseWithOptions(func(offset int, _ ts.Point) []byte {
		if offset >= len(source) {
			return nil
		}
		return source[offset:min(offset+32*1024, len(source))]
	}, nil, options)
	gotTree := tree != nil
	if tree != nil {
		tree.Close()
	}
	parser.Reset()
	return ref, called, gotTree
}
