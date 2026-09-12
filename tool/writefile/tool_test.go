package writefile_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/fsutil"
	"github.com/crowl/ronin/tool/writefile"
)

func TestToolCall(t *testing.T) {
	t.Run("creates parent directories", func(t *testing.T) {
		dir := t.TempDir()
		res, err := callWriteFile(t, writefile.New(dir, fsutil.NewMutationQueue()), writefile.Args{
			Path:    "nested/file.txt",
			Content: "hello\n",
		})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if !res.Created {
			t.Fatal("Created = false, want true")
		}
		if !res.Changed {
			t.Fatal("Changed = false, want true")
		}

		data, err := os.ReadFile(filepath.Join(dir, "nested", "file.txt"))
		if err != nil {
			t.Fatalf("os.ReadFile() error = %v", err)
		}
		if string(data) != "hello\n" {
			t.Fatalf("file content = %q, want hello", data)
		}
	})

	t.Run("reports unchanged file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("same\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		res, err := callWriteFile(t, writefile.New(dir, fsutil.NewMutationQueue()), writefile.Args{Path: "file.txt", Content: "same\n"})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if res.Created {
			t.Fatal("Created = true, want false")
		}
		if res.Changed {
			t.Fatal("Changed = true, want false")
		}
	})

	t.Run("preserves file mode on overwrite", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "script.sh")
		if err := os.WriteFile(path, []byte("old\n"), 0o755); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}

		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = callWriteFile(t, writefile.New(dir, fsutil.NewMutationQueue()), writefile.Args{Path: "script.sh", Content: "new\n"})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("os.Stat() error = %v", err)
		}
		if info.Mode().Perm() != before.Mode().Perm() {
			t.Fatalf("mode = %v, want original %v", info.Mode().Perm(), before.Mode().Perm())
		}
	})

	t.Run("rejects directory target", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "target"), 0o755); err != nil {
			t.Fatalf("os.Mkdir() error = %v", err)
		}

		_, err := callWriteFile(t, writefile.New(dir, fsutil.NewMutationQueue()), writefile.Args{Path: "target", Content: "new\n"})
		assertToolErrorCode(t, err, "not_a_file")
	})
	t.Run("replaces symlink target path without following leaf", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privileges on Windows")
		}

		dir := t.TempDir()
		target := filepath.Join(dir, "target.txt")
		link := filepath.Join(dir, "link.txt")
		if err := os.WriteFile(target, []byte("target\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("os.Symlink() error = %v", err)
		}

		res, err := callWriteFile(t, writefile.New(dir, fsutil.NewMutationQueue()), writefile.Args{Path: "link.txt", Content: "link\n"})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if !res.Changed {
			t.Fatal("Changed = false, want true")
		}
		if !res.ReplacedSymlink {
			t.Fatal("ReplacedSymlink = false, want true")
		}

		linkInfo, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("os.Lstat() error = %v", err)
		}
		if linkInfo.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("link mode = %v, want regular file", linkInfo.Mode())
		}

		linkData, err := os.ReadFile(link)
		if err != nil {
			t.Fatalf("os.ReadFile(link) error = %v", err)
		}
		if string(linkData) != "link\n" {
			t.Fatalf("link content = %q, want link", linkData)
		}

		targetData, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("os.ReadFile(target) error = %v", err)
		}
		if string(targetData) != "target\n" {
			t.Fatalf("target content = %q, want unchanged target", targetData)
		}
	})
	t.Run("replaces symlink leaf when target already has requested content", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privileges on Windows")
		}

		dir := t.TempDir()
		target := filepath.Join(dir, "target.txt")
		link := filepath.Join(dir, "link.txt")
		if err := os.WriteFile(target, []byte("same\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("os.Symlink() error = %v", err)
		}

		res, err := callWriteFile(t, writefile.New(dir, fsutil.NewMutationQueue()), writefile.Args{Path: "link.txt", Content: "same\n"})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if !res.Changed {
			t.Fatal("Changed = false, want true")
		}
		if !res.ReplacedSymlink {
			t.Fatal("ReplacedSymlink = false, want true")
		}

		linkInfo, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("os.Lstat() error = %v", err)
		}
		if linkInfo.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("link mode = %v, want regular file", linkInfo.Mode())
		}

		linkData, err := os.ReadFile(link)
		if err != nil {
			t.Fatalf("os.ReadFile(link) error = %v", err)
		}
		if string(linkData) != "same\n" {
			t.Fatalf("link content = %q, want same", linkData)
		}

		targetData, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("os.ReadFile(target) error = %v", err)
		}
		if string(targetData) != "same\n" {
			t.Fatalf("target content = %q, want unchanged target", targetData)
		}
	})
}

func TestResultArtifacts(t *testing.T) {
	tests := []struct {
		name    string
		before  *string
		content string
		want    string
	}{
		{
			name:    "overwrite shows removals and additions",
			before:  new("old\n"),
			content: "new\n",
			want:    "@@ -1,1 +1,1 @@\n-old\n+new\n",
		},
		{
			name:   "empty overwrite shows deletions",
			before: new("old\n"),
			want:   "@@ -1,1 +0,0 @@\n-old\n",
		},
		{name: "unchanged content", before: new("same\n"), content: "same\n"},
		{name: "empty creation"},
		{name: "unchanged empty file", before: new("")},
		{
			name:    "multiline content is pure additions",
			content: "first\nsecond\n",
			want:    "@@ -0,0 +1,2 @@\n+first\n+second\n",
		},
		{
			name:    "missing trailing newline is preserved",
			content: "last line",
			want:    "@@ -0,0 +1,1 @@\n+last line\n\\ No newline at end of file\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.before != nil {
				if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(*tt.before), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			result, err := callWriteFile(t, writefile.New(dir, fsutil.NewMutationQueue()), writefile.Args{Path: "file.txt", Content: tt.content})
			if err != nil {
				t.Fatal(err)
			}
			artifacts := result.Artifacts()
			if tt.want == "" {
				if len(artifacts) != 0 {
					t.Fatalf("Artifacts() = %v, want none", artifacts)
				}
				return
			}
			if len(artifacts) != 1 {
				t.Fatalf("len(Artifacts()) = %d, want 1", len(artifacts))
			}

			artifact, ok := artifacts[0].(tool.UnifiedDiffArtifact)
			if !ok {
				t.Fatalf("Artifacts()[0] type = %T, want tool.UnifiedDiffArtifact", artifacts[0])
			}
			if artifact.Path != "file.txt" {
				t.Fatalf("artifact Path = %q, want file.txt", artifact.Path)
			}
			if artifact.Diff != tt.want {
				t.Fatalf("artifact Diff = %q, want %q", artifact.Diff, tt.want)
			}
		})
	}

	t.Run("empty content has no artifact", func(t *testing.T) {
		artifacts := (writefile.Result{Path: "empty.txt"}).Artifacts()
		if len(artifacts) != 0 {
			t.Fatalf("len(Artifacts()) = %d, want 0", len(artifacts))
		}
	})
}

func callWriteFile(t *testing.T, writeTool *writefile.Tool, args writefile.Args) (writefile.Result, error) {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	result, err := writeTool.Call(t.Context(), raw)
	if err != nil {
		return writefile.Result{}, err
	}

	res, ok := result.(writefile.Result)
	if !ok {
		t.Fatalf("Call() result type = %T, want writefile.Result", result)
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
