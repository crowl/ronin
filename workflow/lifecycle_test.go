package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealedWorkspacesRejectChanges(t *testing.T) {
	for _, kind := range []string{"lane", "integration"} {
		for _, change := range []string{"commit", "dirty", "branch"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				rt, lane, integration := preparedWorktrees(t)
				workspace := lane
				if kind == "integration" {
					workspace = integration
				}
				switch change {
				case "commit":
					gitCommand(t, workspace.Path, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "chore: move sealed tip")
				case "dirty":
					if err := os.WriteFile(filepath.Join(workspace.Path, "dirty"), []byte("change"), 0600); err != nil {
						t.Fatal(err)
					}
				case "branch":
					gitCommand(t, workspace.Path, "checkout", "-b", "unexpected")
				}
				var err error
				if kind == "lane" {
					integration.Sealed = false
					_, err = rt.squash(integration, lane, "fix: integrate")
				} else {
					err = rt.promote(integration)
				}
				if err == nil || !(strings.Contains(err.Error(), "after sealing") || strings.Contains(err.Error(), "assigned branch") || strings.Contains(err.Error(), "must be clean")) {
					t.Fatalf("error = %v", err)
				}
				if got := strings.TrimSpace(gitCommand(t, rt.manifest.PrimaryRoot, "rev-parse", "HEAD")); got != rt.manifest.BaseHead {
					t.Fatal("primary changed")
				}
			})
		}
	}
}

func TestPromotionSurvivesCleanupFailure(t *testing.T) {
	rt, lane, integration := preparedWorktrees(t)
	gitCommand(t, rt.manifest.PrimaryRoot, "worktree", "lock", lane.Path)
	err := rt.promote(integration)
	if err == nil || !strings.Contains(err.Error(), "promotion succeeded but cleanup failed") {
		t.Fatalf("error = %v", err)
	}
	if !rt.promoted || rt.cleanupComplete {
		t.Fatal("promotion and cleanup state conflated")
	}
	if got := strings.TrimSpace(gitCommand(t, rt.manifest.PrimaryRoot, "rev-parse", "HEAD")); got != integration.SealedHead {
		t.Fatal("wrong promoted commit")
	}
	recovery := rt.recover()
	if !strings.Contains(recovery, "Promotion succeeded") || !strings.Contains(recovery, lane.Path) || strings.Contains(recovery, "worktree remove \""+integration.Path+"\"") {
		t.Fatalf("recovery = %s", recovery)
	}
	if err := rt.promote(integration); err == nil || !strings.Contains(err.Error(), "already promoted") {
		t.Fatalf("repeat promotion = %v", err)
	}
	data, err := os.ReadFile(rt.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"promoted_head": "`+integration.SealedHead+`"`) {
		t.Fatal("promotion not persisted")
	}
}

func TestPromotionManifestFailurePreservesOutcome(t *testing.T) {
	rt, _, integration := preparedWorktrees(t)
	original := rt.manifestPath
	rt.manifestPath = filepath.Join(original, "invalid-child")
	err := rt.promote(integration)
	if err == nil || !strings.Contains(err.Error(), "promotion succeeded but recording promotion failed") {
		t.Fatalf("error = %v", err)
	}
	if !rt.promoted || rt.cleanupComplete {
		t.Fatal("lost successful promotion")
	}
	if got := strings.TrimSpace(gitCommand(t, rt.manifest.PrimaryRoot, "rev-parse", "HEAD")); got != integration.SealedHead {
		t.Fatal("primary did not advance")
	}
	if recovery := rt.recover(); !strings.Contains(recovery, "Do not promote again") || !strings.Contains(recovery, "Could not update manifest") {
		t.Fatalf("recovery = %s", recovery)
	}
}

func TestSealManifestFailureDoesNotPublishSeal(t *testing.T) {
	rt, lane, _ := preparedWorktrees(t)
	lane.Sealed = false
	lane.SealedHead = ""
	rt.manifestPath = filepath.Join(rt.manifestPath, "invalid-child")
	_, err := rt.recordSeal(lane)
	if err == nil || !strings.Contains(err.Error(), "publish workflow manifest") {
		t.Fatalf("error = %v", err)
	}
	if lane.Sealed || lane.SealedHead != "" {
		t.Fatal("failed seal became visible")
	}
}

func preparedWorktrees(t *testing.T) (*worktreeRuntime, *managedWorktree, *managedWorktree) {
	t.Helper()
	repo := initTestRepository(t)
	rt := newWorktreeRuntime(t.Context())
	rt.setWorkingDir(repo)
	preflight, err := rt.inspectPrimary()
	if err != nil {
		t.Fatal(err)
	}
	rt.preflight = &preflight
	if err := rt.executionGate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rt.manifest.RunRoot) })
	lane, err := rt.createWorktree("lane", "lane", "")
	if err != nil {
		t.Fatal(err)
	}
	integration, err := rt.createWorktree("integration", "integration", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane.Path, "change"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.seal(lane); err != nil {
		t.Fatal(err)
	}
	head, err := rt.squash(integration, lane, "fix: integrate lane")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := rt.squashRepairs(integration, head, "fix: repairs"); err != nil {
		t.Fatal(err)
	}
	return rt, lane, integration
}
