package editfile_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/editfile"
	"github.com/crowl/ronin/tool/fsutil"
)

func TestToolCall(t *testing.T) {
	t.Run("applies unique replacement", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		res, err := callEditFile(t, editfile.New(dir, fsutil.NewMutationQueue()), editfile.Args{
			Path: "file.txt",
			Patches: []editfile.Patch{{
				Old: "world",
				New: "gopher",
			}},
		})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if !res.Changed {
			t.Fatal("Changed = false, want true")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile() error = %v", err)
		}
		if string(data) != "hello gopher\n" {
			t.Fatalf("file content = %q, want replacement", data)
		}
	})

	t.Run("rejects missing old text", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		_, err := callEditFile(t, editfile.New(dir, fsutil.NewMutationQueue()), editfile.Args{
			Path: "file.txt",
			Patches: []editfile.Patch{{
				Old: "missing",
				New: "found",
			}},
		})
		assertToolErrorCode(t, err, "old_text_not_found")
	})

	t.Run("rejects non unique old text", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("same\nsame\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		_, err := callEditFile(t, editfile.New(dir, fsutil.NewMutationQueue()), editfile.Args{
			Path: "file.txt",
			Patches: []editfile.Patch{{
				Old: "same",
				New: "different",
			}},
		})
		assertToolErrorCode(t, err, "old_text_not_unique")
	})

	t.Run("replace all permits non unique old text", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("same\nsame\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		res, err := callEditFile(t, editfile.New(dir, fsutil.NewMutationQueue()), editfile.Args{
			Path: "file.txt",
			Patches: []editfile.Patch{{
				Old:        "same",
				New:        "different",
				ReplaceAll: true,
			}},
		})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if res.Patches[0].Replacements != 2 {
			t.Fatalf("Replacements = %d, want 2", res.Patches[0].Replacements)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile() error = %v", err)
		}
		if string(data) != "different\ndifferent\n" {
			t.Fatalf("file content = %q, want all replacements", data)
		}
	})

	t.Run("first failing patch reports patch index zero", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		_, err := callEditFile(t, editfile.New(dir, fsutil.NewMutationQueue()), editfile.Args{
			Path: "file.txt",
			Patches: []editfile.Patch{{
				Old: "missing",
				New: "found",
			}},
		})
		if err == nil {
			t.Fatal("Call() error = nil, want error")
		}
		var toolErr tool.Error
		if !errors.As(err, &toolErr) {
			t.Fatalf("Call() error = %v, want tool.Error", err)
		}
		if toolErr.PatchIndex == nil || *toolErr.PatchIndex != 0 {
			t.Fatalf("PatchIndex = %v, want 0", toolErr.PatchIndex)
		}

		data, jsonErr := json.Marshal(toolErr)
		if jsonErr != nil {
			t.Fatalf("json.Marshal() error = %v", jsonErr)
		}
		if string(data) == "" || !json.Valid(data) {
			t.Fatalf("json.Marshal() = %q, want valid JSON", data)
		}
	})

	t.Run("rejects empty old text during validation", func(t *testing.T) {
		_, err := callEditFile(t, editfile.New(t.TempDir(), fsutil.NewMutationQueue()), editfile.Args{
			Path: "file.txt",
			Patches: []editfile.Patch{{
				Old: "",
				New: "b",
			}},
		})
		assertToolErrorCode(t, err, "invalid_args")
	})

	t.Run("rejects NUL path during validation", func(t *testing.T) {
		_, err := editfile.New(t.TempDir(), fsutil.NewMutationQueue()).Call(context.Background(), []byte("{\"path\":\"bad\\u0000path\",\"patches\":[{\"old\":\"a\",\"new\":\"b\"}]}"))
		assertToolErrorCode(t, err, "invalid_args")
	})

	t.Run("rejects binary file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "binary.dat")
		if err := os.WriteFile(path, []byte{'a', 0, 'b'}, 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		_, err := callEditFile(t, editfile.New(dir, fsutil.NewMutationQueue()), editfile.Args{
			Path: "binary.dat",
			Patches: []editfile.Patch{{
				Old: "a",
				New: "b",
			}},
		})
		assertToolErrorCode(t, err, "binary_file")
	})

	t.Run("rejects non regular file", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("/dev/null is not available on Windows")
		}

		_, err := callEditFile(t, editfile.New("/", fsutil.NewMutationQueue()), editfile.Args{
			Path: "/dev/null",
			Patches: []editfile.Patch{{
				Old: "a",
				New: "b",
			}},
		})
		assertToolErrorCode(t, err, "not_a_file")
	})

	t.Run("stops when context is canceled during patch loop", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("a b c\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		raw, err := json.Marshal(editfile.Args{
			Path: "file.txt",
			Patches: []editfile.Patch{
				{Old: "a", New: "aa"},
				{Old: "b", New: "bb"},
			},
		})
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = editfile.New(dir, fsutil.NewMutationQueue()).Call(ctx, raw)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Call() error = %v, want context.Canceled", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile() error = %v", err)
		}
		if string(data) != "a b c\n" {
			t.Fatalf("file content = %q, want unchanged", data)
		}
	})
}

func callEditFile(t *testing.T, editTool *editfile.Tool, args editfile.Args) (editfile.Result, error) {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	result, err := editTool.Call(t.Context(), raw)
	if err != nil {
		return editfile.Result{}, err
	}

	res, ok := result.(editfile.Result)
	if !ok {
		t.Fatalf("Call() result type = %T, want editfile.Result", result)
	}
	return res, nil
}

func assertToolErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", code)
	}
	var toolErr tool.Error
	if !errors.As(err, &toolErr) {
		t.Fatalf("error = %v, want tool.Error", err)
	}
	if toolErr.Code != code {
		t.Fatalf("Code = %q, want %q", toolErr.Code, code)
	}
}
