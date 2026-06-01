package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusBarCache(t *testing.T) {
	t.Run("reuses cwd status until reset", func(t *testing.T) {
		dir := t.TempDir()
		gitDir := filepath.Join(dir, ".git")
		if err := os.Mkdir(gitDir, 0o755); err != nil {
			t.Fatalf("create git dir: %v", err)
		}
		headPath := filepath.Join(gitDir, "HEAD")
		if err := os.WriteFile(headPath, []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatalf("write HEAD: %v", err)
		}

		var cache statusBarCache
		first := cache.CWDStatus(dir)
		if !strings.Contains(first, "main") {
			t.Fatalf("status missing initial branch: %q", first)
		}

		if err := os.WriteFile(headPath, []byte("ref: refs/heads/feature\n"), 0o644); err != nil {
			t.Fatalf("rewrite HEAD: %v", err)
		}

		cached := cache.CWDStatus(dir)
		if cached != first {
			t.Fatalf("cache did not reuse status\nfirst:  %q\ncached: %q", first, cached)
		}

		cache.Reset()
		refreshed := cache.CWDStatus(dir)
		if !strings.Contains(refreshed, "feature") {
			t.Fatalf("status did not refresh after reset: %q", refreshed)
		}
	})
}
