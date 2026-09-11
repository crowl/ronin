package agenttools_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/crowl/ronin/internal/agenttools"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/tool/readfile"
)

func TestConversationReadHistoryIsIsolated(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, readOnly := range []bool{false, true} {
			t.Run(fmtName(managed, readOnly), func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte("source\n"), 0600); err != nil {
					t.Fatal(err)
				}
				factory := agenttools.NewFactory()
				first := reader(t, factory.New(dir, readOnly, managed, nil))
				second := reader(t, factory.New(dir, readOnly, managed, nil))
				read := func(tool runtime.Tool) readfile.Result {
					t.Helper()
					result, err := tool.Call(t.Context(), json.RawMessage(`{"path":"source.txt"}`))
					if err != nil {
						t.Fatal(err)
					}
					return result.(readfile.Result)
				}
				if read(first).Content != "source\n" || !read(first).ContentOmitted {
					t.Fatal("first conversation should suppress its repeated read")
				}
				if got := read(second); got.ContentOmitted || got.Content != "source\n" {
					t.Fatal("fresh conversation did not receive content")
				}
				first.(runtime.ContextResetter).ResetContext()
				if !read(second).ContentOmitted {
					t.Fatal("reset leaked into another conversation")
				}
				if read(first).Content != "source\n" {
					t.Fatal("reset did not restore content")
				}
			})
		}
	}
}

func fmtName(managed, readOnly bool) string {
	name := "ordinary"
	if managed {
		name = "managed"
	}
	if readOnly {
		name += "/read-only"
	} else {
		name += "/writable"
	}
	return name
}

func reader(t *testing.T, tools []runtime.Tool) runtime.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == "read_file" {
			return tool
		}
	}
	t.Fatal("read tool missing")
	return nil
}
