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

func TestWriteFileAtomic(t *testing.T) {
	t.Run("creates file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "created.txt")

		if err := fsutil.WriteFileAtomic(path, []byte("hello"), 0o640); err != nil {
			t.Fatalf("WriteFileAtomic() error = %v", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile() error = %v", err)
		}
		if string(data) != "hello" {
			t.Fatalf("content = %q, want hello", data)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("os.Stat() error = %v", err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("mode = %v, want 0640", info.Mode().Perm())
		}
	})

	t.Run("replaces content and preserves requested permission bits", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "replace.txt")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		if err := fsutil.WriteFileAtomic(path, []byte("new"), 0o755|os.ModeSetuid); err != nil {
			t.Fatalf("WriteFileAtomic() error = %v", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile() error = %v", err)
		}
		if string(data) != "new" {
			t.Fatalf("content = %q, want new", data)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("os.Stat() error = %v", err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
		}
		if info.Mode()&os.ModeSetuid != 0 {
			t.Fatalf("mode includes setuid bit: %v", info.Mode())
		}
	})

	t.Run("returns error for missing parent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "missing", "file.txt")

		if err := fsutil.WriteFileAtomic(path, []byte("data"), 0o644); err == nil {
			t.Fatal("WriteFileAtomic() error = nil, want error")
		}
	})
}
