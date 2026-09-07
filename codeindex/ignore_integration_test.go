package codeindex_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/crowl/ronin/codeindex"
)

func TestNestedIgnoreTraversalAndRefresh(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".gitignore", "*.go\n!main.go\n!src/**\nblocked/\n!blocked/keep.go\n")
	write(t, root, "src/.gitignore", "*.go\n!keep.go\n!deep/\n")
	write(t, root, "src/deep/.gitignore", "!deep.go\n")
	write(t, root, "blocked/.gitignore", "!keep.go\n")
	for _, name := range []string{"main.go", "hidden.go", "src/keep.go", "src/drop.go", "src/deep/deep.go", "src/deep/drop.go", "blocked/keep.go", "sibling/keep.go"} {
		write(t, root, name, "package p\nfunc Example() {}\n")
	}
	index := codeindex.New(root, t.TempDir())
	assertFiles := func(want []string) {
		t.Helper()
		result, err := index.Map(t.Context(), codeindex.Query{Depth: 8})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, entry := range result.Entries {
			if entry.Kind == "file" {
				got = append(got, entry.Path)
			}
		}
		slices.Sort(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("files = %v, want %v", got, want)
		}
	}
	assertFiles([]string{"main.go", "src/deep/deep.go", "src/keep.go"})
	// Ignored ancestors remain pruned, even if a child .gitignore negates them.
	// Removing the ancestor exclusion and adding a child inclusion restores it.
	write(t, root, ".gitignore", "*.go\n!main.go\n!src/**\n")
	assertFiles([]string{"blocked/keep.go", "main.go", "src/deep/deep.go", "src/keep.go"})
	// Removing a nested rule file must evict records that were only included by it.
	if err := os.Remove(filepath.Join(root, "src", "deep", ".gitignore")); err != nil {
		t.Fatal(err)
	}
	assertFiles([]string{"blocked/keep.go", "main.go", "src/keep.go"})
}
