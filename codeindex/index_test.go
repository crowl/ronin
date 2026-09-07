package codeindex_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crowl/ronin/codeindex"
)

func TestRefreshFindAndDeletion(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	write(t, root, "auth.go", "package auth\n// session expiry check\nfunc ValidateSession() {}\n")
	index := codeindex.New(root, cache)
	first, err := index.Find(t.Context(), codeindex.Query{Text: "ValidateSession"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) == 0 || first.Entries[0].Match != "exact_name" || first.Status.Parsed != 1 {
		t.Fatalf("first: %+v", first)
	}
	hash := first.Entries[0].SHA256
	second, err := codeindex.New(root, cache).Find(t.Context(), codeindex.Query{Text: "session expiry"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 1 || second.Entries[0].Match != "source" || second.Status.Reused != 1 {
		t.Fatalf("second: %+v", second)
	}
	write(t, root, "auth.go", "package auth\nfunc RevokeSession() {}\n")
	next, err := index.Find(t.Context(), codeindex.Query{Text: "RevokeSession"})
	if err != nil {
		t.Fatal(err)
	}
	if next.Entries[0].SHA256 == hash || next.Status.Parsed != 1 {
		t.Fatalf("refresh: %+v", next)
	}
	if err := os.Remove(filepath.Join(root, "auth.go")); err != nil {
		t.Fatal(err)
	}
	empty, err := index.Map(t.Context(), codeindex.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Status.Files != 0 || len(empty.Entries) != 0 {
		t.Fatalf("deleted file retained: %+v", empty)
	}
}

func TestIgnoreScopeAndWorktrees(t *testing.T) {
	root, other, cache := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, root, ".gitignore", "ignored.go\nsub/*\n!sub/keep.go\n")
	for _, name := range []string{"main.go", "ignored.go", "sub/keep.go", "sub/drop.go", "node_modules/pkg/a.ts"} {
		write(t, root, name, "package p\nfunc First() {}")
	}
	write(t, other, "main.go", "package p\nfunc Other() {}")
	if err := os.Symlink(filepath.Join(other, "main.go"), filepath.Join(root, "link.go")); err != nil {
		t.Logf("symlink not available: %v", err)
	}
	index := codeindex.New(root, cache)
	got, err := index.Map(t.Context(), codeindex.Query{Depth: 8})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.Files != 2 {
		t.Fatalf("ignore/symlink scope: %+v", got)
	}
	for _, e := range got.Entries {
		if e.Path != "main.go" && e.Path != "sub/keep.go" {
			t.Fatalf("unexpected path: %s", e.Path)
		}
	}
	_, err = index.Map(t.Context(), codeindex.Query{Path: "../"})
	if err == nil {
		t.Fatal("accepted escaping path")
	}
	isolated, err := codeindex.New(other, cache).Find(t.Context(), codeindex.Query{Text: "First"})
	if err != nil {
		t.Fatal(err)
	}
	if len(isolated.Entries) != 0 {
		t.Fatal("cross-workspace records leaked")
	}
	write(t, root, "sub/.gitignore", "keep.go\n")
	// The ignored sub/.gitignore still controls directory-local exclusions.
	got, err = index.Map(t.Context(), codeindex.Query{Depth: 8})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.Files != 1 {
		t.Fatalf("nested ignore refresh: %+v", got)
	}
}

func TestPartialStructureAndBounds(t *testing.T) {
	root := t.TempDir()
	index := codeindex.New(root, t.TempDir())
	write(t, root, "broken.ts", "export function load() { return f<typeof import('module')>(); }\n// unique lexical needle\n")
	write(t, root, "binary.go", "package p\x00")
	got, err := index.Find(t.Context(), codeindex.Query{Text: "unique lexical"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Status.Issues < 2 || got.Status.Skipped != 1 {
		t.Fatalf("partial: %+v", got)
	}
	write(t, root, "many.go", "package p\n"+strings.Repeat("func Match() {}\n", 150))
	got, err = index.Find(t.Context(), codeindex.Query{Text: "Match", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 3 || !got.Truncated {
		t.Fatalf("bounds: %+v", got)
	}
	data, _ := json.Marshal(got)
	if len(data) > 128*1024 {
		t.Fatal("oversized output")
	}
}

func TestCancellationAndConcurrentReaders(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package p\nfunc A() {}")
	index := codeindex.New(root, t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := index.Map(ctx, codeindex.Query{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			got, err := index.Map(t.Context(), codeindex.Query{})
			if err != nil {
				t.Error(err)
				return
			}
			if got.Status.Files != 1 {
				t.Errorf("snapshot: %+v", got)
			}
		})
	}
	wg.Wait()
}

func TestCacheFailureAndRebuild(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	write(t, root, "a.go", "package p\nfunc A() {}")
	index := codeindex.New(root, cache)
	if _, err := index.Map(t.Context(), codeindex.Query{}); err != nil {
		t.Fatal(err)
	}
	dbs, err := filepath.Glob(filepath.Join(cache, "*.db"))
	if err != nil || len(dbs) != 1 {
		t.Fatalf("cache paths: %v %v", dbs, err)
	}
	if err := os.WriteFile(dbs[0], []byte("invalid sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Map(t.Context(), codeindex.Query{}); err == nil {
		t.Fatal("corrupt cache reported success")
	}
	if err := os.Remove(dbs[0]); err != nil {
		t.Fatal(err)
	}
	got, err := index.Map(t.Context(), codeindex.Query{})
	if err != nil || got.Status.Parsed != 1 {
		t.Fatalf("rebuild: %+v %v", got, err)
	}
	if _, err := codeindex.New(root, filepath.Join(root, "cache")).Map(t.Context(), codeindex.Query{}); err == nil {
		t.Fatal("cache written inside workspace")
	}
}

func write(t *testing.T, root, name, source string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}
