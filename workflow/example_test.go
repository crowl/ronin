package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const examplePlan = `{"tasks":[{"id":"feature","objective":"Add feature.txt with the requested behavior; preserve existing interfaces.","acceptance":["feature.txt contains implemented"],"depends_on":[],"ownership":["feature.txt"],"verification":["inspect feature.txt"],"commit_message":"feat: implement feature"}],"integration_commit_message":"fix: reconcile feature"}`

func TestExampleStartsWithPlannerBeforeDirtyTreeGate(t *testing.T) {
	t.Parallel()
	repo := initTestRepository(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("user work"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err := runConcurrentWorkflowExample(t, repo, func(_ context.Context, req AgentRequest) (AgentResult, error) {
		calls++
		if req.Name != "Planning" || !req.ReadOnly || req.OutputSchema == nil || !strings.Contains(req.Prompt, "Implement the requested behavior.") || strings.Contains(req.Prompt, "design") {
			t.Errorf("first request = %#v", req)
		}
		return AgentResult{Output: []byte(examplePlan)}, nil
	})
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "must be clean") {
		t.Fatalf("calls = %d, error = %v; want planner only then dirty-tree rejection", calls, err)
	}
	content, err := os.ReadFile(filepath.Join(repo, "dirty.txt"))
	if err != nil || string(content) != "user work" {
		t.Fatalf("user file changed: %q, %v", content, err)
	}
}

func TestExamplePlannerHandoffAndNamedRepairCycles(t *testing.T) {
	t.Parallel()
	repo := initTestRepository(t)
	gitCommand(t, repo, "config", "user.name", "Test")
	gitCommand(t, repo, "config", "user.email", "test@example.com")
	var mu sync.Mutex
	var names []string
	output, err := runConcurrentWorkflowExample(t, repo, func(_ context.Context, req AgentRequest) (AgentResult, error) {
		mu.Lock()
		defer mu.Unlock()
		names = append(names, req.Name)
		if req.Name == "Planning" {
			return AgentResult{Output: []byte(examplePlan)}, nil
		}
		if !strings.Contains(req.Prompt, "feature.txt contains implemented") || strings.Contains(req.Prompt, "Overall design") {
			return AgentResult{}, errors.New("missing plan contracts or obsolete design handoff")
		}
		switch req.Name {
		case "Implementing: feature (cycle 1)", "Implementing: feature (cycle 2)":
			if req.Name == "Implementing: feature (cycle 2)" && !strings.Contains(req.Prompt, "Fix lane finding") {
				return AgentResult{}, errors.New("missing lane feedback")
			}
			if err := os.WriteFile(filepath.Join(req.Workspace, "feature.txt"), []byte("implemented\n"), 0o600); err != nil {
				return AgentResult{}, err
			}
			return AgentResult{Text: "internal implementation report"}, nil
		case "Reviewing: feature (cycle 1)":
			return AgentResult{Text: "Fix lane finding\nSTATUS: CHANGES_REQUIRED"}, nil
		case "Integration repair (cycle 1)":
			if !strings.Contains(req.Prompt, "Fix acceptance finding") {
				return AgentResult{}, errors.New("missing acceptance feedback")
			}
		case "Acceptance (cycle 1)":
			return AgentResult{Text: "Fix acceptance finding\nSTATUS: CHANGES_REQUIRED"}, nil
		}
		return AgentResult{Text: "internal report\nSTATUS: APPROVED"}, nil
	})
	var done *DoneError
	if !errors.As(err, &done) {
		t.Fatalf("example error = %v, output = %s", err, output)
	}
	want := []string{"Planning", "Implementing: feature (cycle 1)", "Reviewing: feature (cycle 1)", "Implementing: feature (cycle 2)", "Reviewing: feature (cycle 2)", "Integration repair", "Integration review (cycle 1)", "Acceptance (cycle 1)", "Integration repair (cycle 1)", "Integration review (cycle 2)", "Acceptance (cycle 2)"}
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Fatalf("invocations = %q, want %q", names, want)
	}
	if strings.Contains(output, "internal report") || strings.Contains(done.Error(), "internal report") {
		t.Fatalf("agent reports leaked into final feedback: %s, %v", output, done)
	}
	content, err := os.ReadFile(filepath.Join(repo, "feature.txt"))
	if err != nil || string(content) != "implemented\n" {
		t.Fatalf("promoted content = %q, %v", content, err)
	}
	if branches := gitCommand(t, repo, "branch", "--format=%(refname:short)"); strings.Contains(branches, "ronin/") {
		t.Fatalf("workflow branches remain: %s", branches)
	}
}

func TestAgentDisplayNameValidation(t *testing.T) {
	t.Parallel()
	for _, function := range []string{"run_agent", "start_agent"} {
		t.Run(function, func(t *testing.T) {
			_, err := runScriptWithAgent(t, `ronin.`+function+`({ name = {}, prompt = "test" })`, func(context.Context, AgentRequest) (AgentResult, error) {
				t.Error("invalid name reached agent")
				return AgentResult{}, nil
			})
			if err == nil || !strings.Contains(err.Error(), "name must be a string") {
				t.Fatalf("invalid name error = %v", err)
			}
		})
	}
}
