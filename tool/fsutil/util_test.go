package fsutil_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/crowl/ronin/tool/fsutil"
)

func TestResolvePath(t *testing.T) {
	t.Run("resolves relative path with display inside cwd", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatalf("os.Mkdir() error = %v", err)
		}

		resolved, err := fsutil.ResolvePath(dir, filepath.Join("sub", "file.txt"))
		if err != nil {
			t.Fatalf("ResolvePath() error = %v", err)
		}

		wantAbs, err := fsutil.ResolveExistingOrParent(filepath.Join(dir, "sub", "file.txt"))
		if err != nil {
			t.Fatalf("ResolveExistingOrParent() error = %v", err)
		}
		if resolved.Abs != wantAbs {
			t.Fatalf("Abs = %q, want %q", resolved.Abs, wantAbs)
		}
		if resolved.Display != "sub/file.txt" {
			t.Fatalf("Display = %q, want sub/file.txt", resolved.Display)
		}
	})

	t.Run("resolves existing symlink parent", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privileges on Windows")
		}

		dir := t.TempDir()
		realDir := filepath.Join(dir, "real")
		linkDir := filepath.Join(dir, "link")
		if err := os.Mkdir(realDir, 0o755); err != nil {
			t.Fatalf("os.Mkdir() error = %v", err)
		}
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Fatalf("os.Symlink() error = %v", err)
		}

		resolved, err := fsutil.ResolvePath(dir, filepath.Join("link", "file.txt"))
		if err != nil {
			t.Fatalf("ResolvePath() error = %v", err)
		}

		wantAbs, err := fsutil.ResolveExistingOrParent(filepath.Join(realDir, "file.txt"))
		if err != nil {
			t.Fatalf("ResolveExistingOrParent() error = %v", err)
		}
		if resolved.Abs != wantAbs {
			t.Fatalf("Abs = %q, want %q", resolved.Abs, wantAbs)
		}
	})
}

func TestDisplayPath(t *testing.T) {
	dir := t.TempDir()

	inside := filepath.Join(dir, "a", "b.txt")
	if got := fsutil.DisplayPath(dir, inside); got != "a/b.txt" {
		t.Fatalf("DisplayPath() inside = %q, want a/b.txt", got)
	}

	outside := filepath.Dir(dir)
	got := fsutil.DisplayPath(filepath.Join(dir, "child"), outside)
	if got != filepath.ToSlash(outside) {
		t.Fatalf("DisplayPath() outside = %q, want %q", got, filepath.ToSlash(outside))
	}
}
