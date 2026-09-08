package readfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crowl/ronin/tool/fsutil"
	"github.com/crowl/ronin/tool/readfile"
)

func TestReadSuppressionBoundaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	const content = "first\nsecond\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := readfile.New(dir, fsutil.NewReadCache())
	read := func(args readfile.Args) readfile.Result {
		t.Helper()
		args.Path = "source.txt"
		result, err := callReadFile(t, reader, args)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	// Neither metadata nor a range establishes that complete content was returned.
	read(readfile.Args{Mode: readfile.ReadModeMetadata})
	rangeArgs := readfile.Args{Range: &readfile.Range{StartLine: 1, EndLine: 1}, KnownSHA256: sha256Hex(content)}
	for range 2 {
		if got := read(rangeArgs); got.ContentOmitted || got.Content != "first\n" {
			t.Fatalf("range result = %#v", got)
		}
	}
	first := read(readfile.Args{})
	if first.Content != content {
		t.Fatal("range or metadata suppressed full read")
	}
	if !read(readfile.Args{}).ContentOmitted {
		t.Fatal("repeated full read not suppressed")
	}
	if got := read(rangeArgs); got.ContentOmitted || got.Content != "first\n" {
		t.Fatal("full read suppressed range")
	}
	if got := read(readfile.Args{Mode: readfile.ReadModeFull, KnownSHA256: first.SHA256}); got.Content != content {
		t.Fatal("full mode did not bypass suppression")
	}
	if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := read(readfile.Args{}); got.Content != "changed\n" || got.SHA256 == first.SHA256 {
		t.Fatal("changed file was not refreshed")
	}
	reader.ResetContext()
	if got := read(readfile.Args{}); got.Content != "changed\n" {
		t.Fatal("reset did not restore content")
	}
}
