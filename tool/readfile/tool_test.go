package readfile_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/fsutil"
	"github.com/crowl/ronin/tool/readfile"
)

func TestToolDescriptionGuidesGoRangeReads(t *testing.T) {
	description := readfile.New(t.TempDir(), fsutil.NewReadCache()).Description()
	for _, expected := range []string{"Go exploration", "ranges returned by outline_package or find_symbol", "whole-file reads"} {
		if !strings.Contains(description, expected) {
			t.Errorf("Description() = %q, want %q", description, expected)
		}
	}
	if strings.Contains(description, "go_navigation") {
		t.Errorf("Description() contains removed go_navigation tool: %q", description)
	}
}

func TestToolCall(t *testing.T) {
	t.Run("reads full content and metadata", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "hello.txt")
		content := "hello\nworld\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		res, err := callReadFile(t, readfile.New(dir, fsutil.NewReadCache()), readfile.Args{Path: "hello.txt", Mode: readfile.ReadModeFull})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if res.Content != content {
			t.Fatalf("Content = %q, want %q", res.Content, content)
		}
		if res.Size != int64(len(content)) {
			t.Fatalf("Size = %d, want %d", res.Size, len(content))
		}
		if res.SHA256 != sha256Hex(content) {
			t.Fatalf("SHA256 = %q, want %q", res.SHA256, sha256Hex(content))
		}
		if res.FileID == "" {
			t.Fatal("FileID is empty")
		}
	})

	t.Run("parameters do not expose max bytes", func(t *testing.T) {
		schema := readfile.New(t.TempDir(), fsutil.NewReadCache()).Parameters()
		if _, ok := schema.Properties["max_bytes"]; ok {
			t.Fatal("Parameters() exposes max_bytes")
		}
	})

	t.Run("auto mode omits repeated full content", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "hello.txt")
		if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		readTool := readfile.New(dir, fsutil.NewReadCache())
		first, err := callReadFile(t, readTool, readfile.Args{Path: "hello.txt"})
		if err != nil {
			t.Fatalf("first Call() error = %v", err)
		}
		second, err := callReadFile(t, readTool, readfile.Args{Path: "hello.txt"})
		if err != nil {
			t.Fatalf("second Call() error = %v", err)
		}

		if first.Content != "hello\n" {
			t.Fatalf("first Content = %q, want hello", first.Content)
		}
		if !second.ContentOmitted {
			t.Fatal("second ContentOmitted = false, want true")
		}
		if second.Content != "" {
			t.Fatalf("second Content = %q, want empty", second.Content)
		}
	})

	t.Run("metadata mode omits content", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "hello.txt")
		if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		res, err := callReadFile(t, readfile.New(dir, fsutil.NewReadCache()), readfile.Args{Path: "hello.txt", Mode: readfile.ReadModeMetadata})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if !res.ContentOmitted {
			t.Fatal("ContentOmitted = false, want true")
		}
		if res.Content != "" {
			t.Fatalf("Content = %q, want empty", res.Content)
		}
	})

	t.Run("metadata mode reads large file without content limit", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "large.txt")
		content := strings.Repeat("large\n", 40_000)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		res, err := callReadFile(t, readfile.New(dir, fsutil.NewReadCache()), readfile.Args{Path: "large.txt", Mode: readfile.ReadModeMetadata})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if !res.ContentOmitted {
			t.Fatal("ContentOmitted = false, want true")
		}
		if res.Content != "" {
			t.Fatalf("Content = %q, want empty", res.Content)
		}
		if res.Size != int64(len(content)) {
			t.Fatalf("Size = %d, want %d", res.Size, len(content))
		}
		if res.SHA256 != sha256Hex(content) {
			t.Fatalf("SHA256 = %q, want full file SHA", res.SHA256)
		}
	})

	t.Run("invalid mode returns invalid args", func(t *testing.T) {
		_, err := readfile.New(t.TempDir(), fsutil.NewReadCache()).Call(context.Background(), []byte(`{"path":"file.txt","mode":"bogus"}`))
		if err == nil {
			t.Fatal("Call() error = nil, want invalid args")
		}

		var toolErr tool.Error
		if !errors.As(err, &toolErr) {
			t.Fatalf("Call() error = %v, want tool.Error", err)
		}
		if toolErr.Code != "invalid_args" {
			t.Fatalf("toolErr.Code = %q, want invalid_args", toolErr.Code)
		}
	})

	t.Run("rejects NUL path", func(t *testing.T) {
		_, err := readfile.New(t.TempDir(), fsutil.NewReadCache()).Call(context.Background(), []byte("{\"path\":\"bad\\u0000path\"}"))
		assertToolErrorCode(t, err, "invalid_args")
	})

	t.Run("rejects full file larger than max bytes", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "large.txt")
		if err := os.WriteFile(path, []byte("123456"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		_, err := callReadFile(t, readfile.New(dir, fsutil.NewReadCache()), readfile.Args{Path: "large.txt", MaxBytes: 5})
		assertToolErrorCode(t, err, "file_too_large")
	})

	t.Run("rejects binary file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "binary.dat")
		if err := os.WriteFile(path, []byte{'a', 0, 'b'}, 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		_, err := callReadFile(t, readfile.New(dir, fsutil.NewReadCache()), readfile.Args{Path: "binary.dat"})
		if err == nil {
			t.Fatal("Call() error = nil, want binary_file")
		}

		var toolErr tool.Error
		if !errors.As(err, &toolErr) {
			t.Fatalf("Call() error = %v, want tool.Error", err)
		}
		if toolErr.Code != "binary_file" {
			t.Fatalf("toolErr.Code = %q, want binary_file", toolErr.Code)
		}
	})

	t.Run("returns requested range", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "lines.txt")
		if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		res, err := callReadFile(t, readfile.New(dir, fsutil.NewReadCache()), readfile.Args{Path: "lines.txt", Range: &readfile.Range{StartLine: 2, EndLine: 3}})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if res.Content != "two\nthree\n" {
			t.Fatalf("Content = %q, want requested range", res.Content)
		}
	})

	t.Run("range can read large file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "large.txt")
		content := strings.Repeat("skip\n", 40_000) + "target\n" + strings.Repeat("tail\n", 40_000)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		res, err := callReadFile(t, readfile.New(dir, fsutil.NewReadCache()), readfile.Args{Path: "large.txt", Range: &readfile.Range{StartLine: 40001, EndLine: 40001}})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if res.Content != "target\n" {
			t.Fatalf("Content = %q, want target", res.Content)
		}
		if res.Size != int64(len(content)) {
			t.Fatalf("Size = %d, want %d", res.Size, len(content))
		}
		if res.SHA256 != sha256Hex(content) {
			t.Fatalf("SHA256 = %q, want full file SHA", res.SHA256)
		}
	})

	t.Run("range can read and hash file larger than old scan limit", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "large-range.txt")
		content := strings.Repeat("skip\n", 3_400_000) + "target\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		res, err := callReadFile(t, readfile.New(dir, fsutil.NewReadCache()), readfile.Args{Path: "large-range.txt", Range: &readfile.Range{StartLine: 3_400_001, EndLine: 3_400_001}})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if res.Content != "target\n" {
			t.Fatalf("Content = %q, want target", res.Content)
		}
		if res.Size != int64(len(content)) {
			t.Fatalf("Size = %d, want %d", res.Size, len(content))
		}
		if res.SHA256 != sha256Hex(content) {
			t.Fatalf("SHA256 = %q, want full file SHA", res.SHA256)
		}
	})

	// Range byte limits apply to returned content, not total file size.
	t.Run("range reads small content from file larger than max bytes", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "large-total.txt")
		content := "target\n" + strings.Repeat("tail\n", 1_000)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		res, err := callReadFile(t, readfile.New(dir, fsutil.NewReadCache()), readfile.Args{Path: "large-total.txt", Range: &readfile.Range{StartLine: 1, EndLine: 1}, MaxBytes: int64(len("target\n"))})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if res.Content != "target\n" {
			t.Fatalf("Content = %q, want target", res.Content)
		}
		if res.Size != int64(len(content)) {
			t.Fatalf("Size = %d, want total file size", res.Size)
		}
		if res.SHA256 != sha256Hex(content) {
			t.Fatalf("SHA256 = %q, want full file SHA", res.SHA256)
		}
	})

	t.Run("range enforces returned byte limit", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "lines.txt")
		if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		_, err := callReadFile(t, readfile.New(dir, fsutil.NewReadCache()), readfile.Args{Path: "lines.txt", Range: &readfile.Range{StartLine: 1, EndLine: 3}, MaxBytes: 5})
		assertToolErrorCode(t, err, "file_too_large")
	})

	t.Run("range handles line crossing buffer boundary", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "long-line.txt")
		longLine := strings.Repeat("a", 40*1024) + "\n"
		content := "first\n" + longLine + "third\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		res, err := callReadFile(t, readfile.New(dir, fsutil.NewReadCache()), readfile.Args{Path: "long-line.txt", Range: &readfile.Range{StartLine: 2, EndLine: 2}})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if res.Content != longLine {
			t.Fatalf("Content length = %d, want %d", len(res.Content), len(longLine))
		}
	})

	t.Run("range skips long line crossing buffer boundary", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "long-skip.txt")
		longLine := strings.Repeat("a", 40*1024) + "\n"
		content := "first\n" + longLine + "target\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		res, err := callReadFile(t, readfile.New(dir, fsutil.NewReadCache()), readfile.Args{Path: "long-skip.txt", Range: &readfile.Range{StartLine: 3, EndLine: 3}})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if res.Content != "target\n" {
			t.Fatalf("Content = %q, want target", res.Content)
		}
	})

	t.Run("known sha does not mark full content returned", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "hello.txt")
		content := "hello\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		readTool := readfile.New(dir, fsutil.NewReadCache())
		first, err := callReadFile(t, readTool, readfile.Args{Path: "hello.txt", KnownSHA256: sha256Hex(content)})
		if err != nil {
			t.Fatalf("first Call() error = %v", err)
		}
		if !first.ContentOmitted {
			t.Fatal("first ContentOmitted = false, want true")
		}

		second, err := callReadFile(t, readTool, readfile.Args{Path: "hello.txt"})
		if err != nil {
			t.Fatalf("second Call() error = %v", err)
		}
		if second.Content != content {
			t.Fatalf("second Content = %q, want content", second.Content)
		}
	})
	t.Run("rejects negative range line", func(t *testing.T) {
		_, err := callReadFile(t, readfile.New(t.TempDir(), fsutil.NewReadCache()), readfile.Args{Path: "file.txt", Range: &readfile.Range{StartLine: -1}})
		assertToolErrorCode(t, err, "invalid_args")
	})

	// Zero line values remain unspecified for backward-compatible open-ended ranges.
	t.Run("rejects range end before start", func(t *testing.T) {
		_, err := callReadFile(t, readfile.New(t.TempDir(), fsutil.NewReadCache()), readfile.Args{Path: "file.txt", Range: &readfile.Range{StartLine: 3, EndLine: 2}})
		assertToolErrorCode(t, err, "invalid_args")
	})

	t.Run("clamps requested max bytes to hard cap", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "large.txt")
		content := strings.Repeat("a", 1024*1024) + "b"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		_, err := callReadFile(t, readfile.New(dir, fsutil.NewReadCache()), readfile.Args{Path: "large.txt", MaxBytes: int64(len(content))})
		assertToolErrorCode(t, err, "file_too_large")
	})
}

func callReadFile(t *testing.T, readTool *readfile.Tool, args readfile.Args) (readfile.Result, error) {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	result, err := readTool.Call(t.Context(), raw)
	if err != nil {
		return readfile.Result{}, err
	}

	res, ok := result.(readfile.Result)
	if !ok {
		t.Fatalf("Call() result type = %T, want readfile.Result", result)
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

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
