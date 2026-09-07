package codeindex_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crowl/ronin/codeindex"
)

func TestCachePathEscaping(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache #100% & é")
	write(t, root, "main.go", "package main\nfunc FindMe() {}\n")
	index := codeindex.New(root, cache)
	first, err := index.Find(t.Context(), codeindex.Query{Text: "FindMe"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) == 0 || first.Status.Parsed != 1 {
		t.Fatalf("first: %+v", first)
	}
	files, err := os.ReadDir(cache)
	if err != nil || len(files) != 1 || filepath.Ext(files[0].Name()) != ".db" {
		t.Fatalf("cache files: %v, err=%v", files, err)
	}
	second, err := codeindex.New(root, cache).Find(t.Context(), codeindex.Query{Text: "FindMe"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) == 0 || second.Status.Reused != 1 {
		t.Fatalf("reopened: %+v", second)
	}
}
