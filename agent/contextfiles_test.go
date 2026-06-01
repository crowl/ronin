package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crowl/ronin/agent"
)

func TestLoadContextFiles(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	root := filepath.Join(dir, "repo")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(config) error = %v", err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(sub) error = %v", err)
	}
	writeFile(t, filepath.Join(configDir, "AGENTS.md"), "global")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root")
	writeFile(t, filepath.Join(sub, "AGENTS.md"), "sub")

	files, err := agent.LoadContextFiles(configDir, sub)
	if err != nil {
		t.Fatalf("LoadContextFiles() error = %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("len(files) = %d, want 3", len(files))
	}
	if files[0].Content != "global" || files[1].Content != "root" || files[2].Content != "sub" {
		t.Fatalf("contents = %q, %q, %q; want global, root, sub", files[0].Content, files[1].Content, files[2].Content)
	}

	writeFile(t, filepath.Join(sub, "AGENTS.md"), strings.Repeat("x", agent.MaxContextFileBytes+1))
	_, err = agent.LoadContextFiles(configDir, sub)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("LoadContextFiles() error = %v, want exceeds error", err)
	}
}
