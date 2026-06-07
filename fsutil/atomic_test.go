package fsutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crowl/ronin/fsutil"
)

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
