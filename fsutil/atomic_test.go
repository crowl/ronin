package fsutil_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		// Windows exposes writable files as 0666, not POSIX permission bits.
		wantMode := os.FileMode(0o640)
		if runtime.GOOS == "windows" {
			wantMode = 0o666
		}
		if info.Mode().Perm() != wantMode {
			t.Fatalf("mode = %v, want %v", info.Mode().Perm(), wantMode)
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
		wantMode := os.FileMode(0o755)
		if runtime.GOOS == "windows" {
			wantMode = 0o666
		}
		if info.Mode().Perm() != wantMode {
			t.Fatalf("mode = %v, want %v", info.Mode().Perm(), wantMode)
		}
		if info.Mode()&os.ModeSetuid != 0 {
			t.Fatalf("mode includes setuid bit: %v", info.Mode())
		}
	})

	t.Run("returns error for missing parent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "missing", "file.txt")

		err := fsutil.WriteFileAtomic(path, []byte("data"), 0o644)
		if err == nil {
			t.Fatal("WriteFileAtomic() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "write file \""+path+"\": create temporary file:") {
			t.Fatalf("error = %v, want temporary-file creation context for %q", err, path)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("errors.Is(error, os.ErrNotExist) = false, error = %v", err)
		}
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("errors.As(error, *os.PathError) = false, error = %v", err)
		}
	})

	t.Run("preserves existing directory after rename failure", func(t *testing.T) {
		parent := t.TempDir()
		path := filepath.Join(parent, "destination")
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatalf("os.Mkdir() error = %v", err)
		}
		entry := filepath.Join(path, "existing.txt")
		const contents = "existing contents"
		if err := os.WriteFile(entry, []byte(contents), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		err := fsutil.WriteFileAtomic(path, []byte("replacement"), 0o644)
		if err == nil {
			t.Fatal("WriteFileAtomic() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "write file \""+path+"\": rename temporary file:") {
			t.Fatalf("error = %v, want rename context for %q", err, path)
		}
		var linkErr *os.LinkError
		if !errors.As(err, &linkErr) {
			t.Fatalf("errors.As(error, *os.LinkError) = false, error = %v", err)
		}
		if linkErr.Op != "rename" {
			t.Fatalf("LinkError.Op = %q, want rename", linkErr.Op)
		}
		if linkErr.New != path {
			t.Fatalf("LinkError.New = %q, want %q", linkErr.New, path)
		}
		if !errors.Is(err, linkErr.Err) {
			t.Fatalf("errors.Is(error, linkErr.Err) = false, error = %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("os.Stat() error = %v", err)
		}
		if !info.IsDir() {
			t.Fatalf("destination is not a directory")
		}
		got, err := os.ReadFile(entry)
		if err != nil {
			t.Fatalf("os.ReadFile() error = %v", err)
		}
		if string(got) != contents {
			t.Fatalf("existing content = %q, want %q", got, contents)
		}

		entries, err := os.ReadDir(parent)
		if err != nil {
			t.Fatalf("os.ReadDir() error = %v", err)
		}
		if len(entries) != 1 || entries[0].Name() != "destination" {
			t.Fatalf("parent entries = %v, want only destination", entries)
		}
	})
}
