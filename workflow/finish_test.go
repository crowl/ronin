package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinishWorktreeGuards(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"unsealed", "lane", "dirty", "moved head", "changed primary", "cleanup failure"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			repo := initTestRepository(t)
			rt := newWorktreeRuntime(context.Background())
			rt.setWorkingDir(repo)
			preflight, err := rt.inspectPrimary()
			if err != nil {
				t.Fatal(err)
			}
			rt.preflight = &preflight
			if err := rt.executionGate(); err != nil {
				t.Fatal(err)
			}
			integration, err := rt.createWorktree("integration", "integration", "")
			if err != nil {
				t.Fatal(err)
			}
			// Always remove test-owned artifacts, including intentionally dirty ones.
			t.Cleanup(func() {
				for _, workspace := range rt.manifest.Worktrees {
					if !workspace.WorktreeRemoved {
						gitCommand(t, repo, "worktree", "remove", "--force", workspace.Path)
					}
				}
				_ = os.RemoveAll(rt.manifest.RunRoot)
			})
			if _, _, err := rt.squashRepairs(integration, integration.Base, "fix: finalize result"); err != nil {
				t.Fatal(err)
			}
			want := ""
			switch scenario {
			case "unsealed":
				integration.Sealed = false
				want = "sealed integration"
			case "lane":
				integration.Kind = "lane"
				want = "sealed integration"
			case "dirty":
				if err := os.WriteFile(filepath.Join(integration.Path, "dirty.txt"), []byte("dirty"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "dirty after sealing"
			case "moved head":
				gitCommand(t, integration.Path, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "fix: move sealed head")
				want = "changed after sealing"
			case "changed primary":
				gitCommand(t, repo, "checkout", "-b", "user-branch")
				want = "primary branch or HEAD changed"
			case "cleanup failure":
				lane, err := rt.createWorktree("dirty-lane", "lane", "")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(lane.Path, "dirty.txt"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "cleanup failed"
			}
			err = rt.finish(integration)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("finish error = %v, want %q", err, want)
			}
			if got := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD")); got != preflight.Head {
				t.Fatalf("primary HEAD changed to %s", got)
			}
			if scenario == "cleanup failure" {
				recovery := rt.recover()
				if !strings.Contains(recovery, "Result branch "+integration.Branch+" retained") {
					t.Fatalf("missing result in recovery: %s", recovery)
				}
				if strings.Contains(recovery, "branch -D \""+integration.Branch+"\"") {
					t.Fatalf("recovery deletes result: %s", recovery)
				}
				gitCommand(t, repo, "rev-parse", "--verify", integration.Branch)
				if err := rt.finish(integration); err == nil || !strings.Contains(err.Error(), "already finalized") {
					t.Fatalf("second finish = %v", err)
				}
			}
		})
	}
}
