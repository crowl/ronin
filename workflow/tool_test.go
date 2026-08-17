package workflow_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/workflow"
)

func TestTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "implement.lua")
	if err := os.WriteFile(path, []byte(`ronin.done(ronin.input)`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	catalog, err := workflow.LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	workflowTool := workflow.NewTool(catalog, t.TempDir(), nil)

	if workflowTool.Name() != "run_workflow" {
		t.Fatalf("Name() = %q", workflowTool.Name())
	}
	nameSchema := workflowTool.Parameters().Properties["name"]
	if len(nameSchema.Enum) != 1 || nameSchema.Enum[0] != "implement" {
		t.Fatalf("name enum = %#v", nameSchema.Enum)
	}

	var artifacts []tool.Artifact
	value, err := workflowTool.CallIncremental(t.Context(), json.RawMessage(`{"name":"implement","input":"build it"}`), func(artifact tool.Artifact) error {
		artifacts = append(artifacts, artifact)
		return nil
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	result, ok := value.(workflow.ToolResult)
	if !ok || result.Status != workflow.StatusCompleted || result.Summary != "build it" {
		t.Fatalf("Call() = %#v", value)
	}
	if len(artifacts) == 0 {
		t.Fatal("CallIncremental() emitted no progress artifacts")
	}

	_, err = workflowTool.Call(context.Background(), json.RawMessage(`{"name":"missing","input":"x"}`))
	if err == nil {
		t.Fatal("missing workflow error = nil")
	}
}
