package workflow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crowl/ronin/workflow"
)

func TestLoadCatalog(t *testing.T) {
	t.Run("missing directory is empty", func(t *testing.T) {
		catalog, err := workflow.LoadCatalog(filepath.Join(t.TempDir(), "missing"))
		if err != nil {
			t.Fatalf("LoadCatalog() error = %v", err)
		}
		if got := catalog.Workflows(); len(got) != 0 {
			t.Fatalf("Workflows() = %#v, want empty", got)
		}
	})

	t.Run("loads flat Lua files in name order", func(t *testing.T) {
		dir := t.TempDir()
		writeWorkflow(t, dir, "review.lua", `ronin.log("review")`)
		writeWorkflow(t, dir, "implement.lua", `ronin.done("done")`)
		writeWorkflow(t, dir, "README.md", "ignored")
		if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		writeWorkflow(t, filepath.Join(dir, "nested"), "ignored.lua", `ronin.log("ignored")`)

		catalog, err := workflow.LoadCatalog(dir)
		if err != nil {
			t.Fatalf("LoadCatalog() error = %v", err)
		}
		got := catalog.Workflows()
		if len(got) != 2 || got[0].Name != "implement" || got[1].Name != "review" {
			t.Fatalf("Workflows() = %#v, want implement and review", got)
		}
		if item, ok := catalog.Lookup("review"); !ok || item.Path != filepath.Join(dir, "review.lua") {
			t.Fatalf("Lookup(review) = %#v, %v", item, ok)
		}
	})

	t.Run("rejects invalid names and malformed scripts", func(t *testing.T) {
		for name, file := range map[string]string{
			"invalid name": "Bad.lua",
			"malformed":    "bad.lua",
		} {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				content := `ronin.log("ok")`
				if name == "malformed" {
					content = "local value ="
				}
				writeWorkflow(t, dir, file, content)
				_, err := workflow.LoadCatalog(dir)
				if err == nil {
					t.Fatal("LoadCatalog() error = nil, want error")
				}
				if name == "malformed" && !strings.Contains(err.Error(), "parse workflow") {
					t.Fatalf("LoadCatalog() error = %v, want parse workflow", err)
				}
			})
		}
	})
}

func writeWorkflow(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
