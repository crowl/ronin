package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	lua "github.com/Shopify/go-lua"
)

var (
	workspaceIDPattern       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
	conventionalCommitHeader = regexp.MustCompile(`^(feat|fix|refactor|test|docs|build|ci|perf|chore)(\([a-z0-9][a-z0-9._/-]*\))?(!)?: (.+)$`)
)

type gitPreflight struct {
	Root      string
	CommonDir string
	Branch    string
	Head      string
}

type managedWorktree struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Handle string `json:"handle"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Sealed bool   `json:"sealed"`
}

type worktreeManifest struct {
	RunID       string             `json:"run_id"`
	PrimaryRoot string             `json:"primary_root"`
	BaseBranch  string             `json:"base_branch"`
	BaseHead    string             `json:"base_head"`
	RunRoot     string             `json:"run_root"`
	Worktrees   []*managedWorktree `json:"worktrees"`
}

type worktreeRuntime struct {
	ctx          context.Context
	workingDir   string
	preflight    *gitPreflight
	manifest     *worktreeManifest
	manifestPath string
	byHandle     map[string]*managedWorktree
	promoted     bool
}

func newWorktreeRuntime(ctx context.Context) *worktreeRuntime {
	return &worktreeRuntime{ctx: ctx, byHandle: make(map[string]*managedWorktree)}
}

func (rt *worktreeRuntime) setWorkingDir(workingDir string) {
	rt.workingDir = workingDir
}

func gitPreflightFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		preflight, err := rt.inspectPrimary()
		if err != nil {
			panic(lua.RuntimeError("ronin.git_preflight: " + err.Error()))
		}
		rt.preflight = &preflight
		state.NewTable()
		setLuaStringField(state, "root", preflight.Root)
		setLuaStringField(state, "branch", preflight.Branch)
		setLuaStringField(state, "head", preflight.Head)
		return 1
	}
}

func gitExecutionGateFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		if err := rt.executionGate(); err != nil {
			panic(lua.RuntimeError("ronin.git_execution_gate: " + err.Error()))
		}
		state.NewTable()
		setLuaStringField(state, "run_id", rt.manifest.RunID)
		setLuaStringField(state, "base_head", rt.manifest.BaseHead)
		setLuaStringField(state, "branch", rt.manifest.BaseBranch)
		return 1
	}
}

func createWorktreeFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		if !state.IsTable(1) {
			panic(lua.RuntimeError("ronin.create_worktree expects a table"))
		}
		id, _, err := readAgentStringField(state, 1, "id", true)
		if err != nil {
			panic(lua.RuntimeError(strings.Replace(err.Error(), "ronin.run_agent", "ronin.create_worktree", 1)))
		}
		kind, ok, err := readAgentStringField(state, 1, "kind", false)
		if err != nil {
			panic(lua.RuntimeError(strings.Replace(err.Error(), "ronin.run_agent", "ronin.create_worktree", 1)))
		}
		if !ok {
			kind = "lane"
		}
		base := ""
		if from, hasFrom, err := readAgentStringField(state, 1, "from", false); err != nil {
			panic(lua.RuntimeError(strings.Replace(err.Error(), "ronin.run_agent", "ronin.create_worktree", 1)))
		} else if hasFrom {
			parent, err := rt.workspace(from)
			if err != nil {
				panic(lua.RuntimeError("ronin.create_worktree: " + err.Error()))
			}
			if parent.Kind != "integration" {
				panic(lua.RuntimeError("ronin.create_worktree: from must identify the integration workspace"))
			}
			base, err = rt.git(parent.Path, "rev-parse", "HEAD")
			if err != nil {
				panic(lua.RuntimeError("ronin.create_worktree: resolve integration HEAD: " + err.Error()))
			}
			base = strings.TrimSpace(base)
		}
		workspace, err := rt.createWorktree(id, kind, base)
		if err != nil {
			panic(lua.RuntimeError("ronin.create_worktree: " + err.Error()))
		}
		pushWorkspace(state, workspace)
		return 1
	}
}

func sealWorktreeFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		handle, err := requiredStringArgument(state, 1, "ronin.seal_worktree", "workspace")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		workspace, err := rt.workspace(handle)
		if err != nil {
			panic(lua.RuntimeError("ronin.seal_worktree: " + err.Error()))
		}
		tip, err := rt.seal(workspace)
		if err != nil {
			panic(lua.RuntimeError("ronin.seal_worktree: " + err.Error()))
		}
		state.NewTable()
		state.PushBoolean(true)
		state.SetField(-2, "ok")
		setLuaStringField(state, "head", tip)
		return 1
	}
}

func squashWorktreeFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		integrationHandle, err := requiredStringArgument(state, 1, "ronin.squash_worktree", "integration workspace")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		laneHandle, err := requiredStringArgument(state, 2, "ronin.squash_worktree", "lane workspace")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		message, err := requiredStringArgument(state, 3, "ronin.squash_worktree", "commit message")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		integration, err := rt.workspace(integrationHandle)
		if err != nil {
			panic(lua.RuntimeError("ronin.squash_worktree: " + err.Error()))
		}
		lane, err := rt.workspace(laneHandle)
		if err != nil {
			panic(lua.RuntimeError("ronin.squash_worktree: " + err.Error()))
		}
		head, err := rt.squash(integration, lane, message)
		if err != nil {
			panic(lua.RuntimeError("ronin.squash_worktree: " + err.Error()))
		}
		state.NewTable()
		state.PushBoolean(true)
		state.SetField(-2, "ok")
		setLuaStringField(state, "head", head)
		return 1
	}
}

func worktreeHeadFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		handle, err := requiredStringArgument(state, 1, "ronin.worktree_head", "workspace")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		workspace, err := rt.workspace(handle)
		if err != nil {
			panic(lua.RuntimeError("ronin.worktree_head: " + err.Error()))
		}
		head, err := rt.git(workspace.Path, "rev-parse", "HEAD")
		if err != nil {
			panic(lua.RuntimeError("ronin.worktree_head: " + err.Error()))
		}
		state.PushString(strings.TrimSpace(head))
		return 1
	}
}

func squashRepairsFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		handle, err := requiredStringArgument(state, 1, "ronin.squash_repairs", "workspace")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		base, err := requiredStringArgument(state, 2, "ronin.squash_repairs", "base")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		message, err := requiredStringArgument(state, 3, "ronin.squash_repairs", "commit message")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		workspace, err := rt.workspace(handle)
		if err != nil {
			panic(lua.RuntimeError("ronin.squash_repairs: " + err.Error()))
		}
		head, changed, err := rt.squashRepairs(workspace, base, message)
		if err != nil {
			panic(lua.RuntimeError("ronin.squash_repairs: " + err.Error()))
		}
		state.NewTable()
		state.PushBoolean(true)
		state.SetField(-2, "ok")
		state.PushBoolean(changed)
		state.SetField(-2, "changed")
		setLuaStringField(state, "head", head)
		return 1
	}
}

func promoteWorktreeFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		handle, err := requiredStringArgument(state, 1, "ronin.promote_worktree", "workspace")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		workspace, err := rt.workspace(handle)
		if err != nil {
			panic(lua.RuntimeError("ronin.promote_worktree: " + err.Error()))
		}
		if err := rt.promote(workspace); err != nil {
			panic(lua.RuntimeError("ronin.promote_worktree: " + err.Error()))
		}
		state.PushBoolean(true)
		return 1
	}
}

func validateCommitFunction() lua.Function {
	return func(state *lua.State) int {
		message, err := requiredStringArgument(state, 1, "ronin.valid_commit", "message")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		state.PushBoolean(validateConventionalCommit(message) == nil)
		return 1
	}
}

func (rt *worktreeRuntime) inspectPrimary() (gitPreflight, error) {
	root, err := rt.git(rt.workingDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitPreflight{}, fmt.Errorf("working directory is not inside a Git worktree: %w", err)
	}
	root = strings.TrimSpace(root)
	branch, err := rt.git(root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) == "" {
		return gitPreflight{}, fmt.Errorf("primary worktree must be on an attached branch")
	}
	head, err := rt.git(root, "rev-parse", "HEAD")
	if err != nil {
		return gitPreflight{}, fmt.Errorf("read HEAD: %w", err)
	}
	commonDir, err := rt.git(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return gitPreflight{}, fmt.Errorf("resolve Git common directory: %w", err)
	}
	return gitPreflight{
		Root:      root,
		CommonDir: strings.TrimSpace(commonDir),
		Branch:    strings.TrimSpace(branch),
		Head:      strings.TrimSpace(head),
	}, nil
}

func (rt *worktreeRuntime) executionGate() error {
	if rt.preflight == nil {
		return fmt.Errorf("git_preflight must run before design and planning")
	}
	if rt.manifest != nil {
		return fmt.Errorf("execution gate already passed")
	}
	if err := rt.waitForGitLock(); err != nil {
		return err
	}
	current, err := rt.inspectPrimary()
	if err != nil {
		return err
	}
	if current.Root != rt.preflight.Root || current.Branch != rt.preflight.Branch || current.Head != rt.preflight.Head {
		return fmt.Errorf("primary branch or HEAD changed during design/planning: recorded %s at %s, now %s at %s", rt.preflight.Branch, rt.preflight.Head, current.Branch, current.Head)
	}
	status, err := rt.git(current.Root, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return fmt.Errorf("inspect primary worktree: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("primary worktree must be clean before execution; dirty entries:\n%s", strings.TrimRight(status, "\n"))
	}

	runID, err := randomRunID()
	if err != nil {
		return fmt.Errorf("create workflow run ID: %w", err)
	}
	runRoot, err := os.MkdirTemp("", "ronin-workflow-"+runID+"-")
	if err != nil {
		return fmt.Errorf("create workflow worktree directory: %w", err)
	}
	manifestDir := filepath.Join(current.CommonDir, "ronin-workflows", runID)
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		_ = os.RemoveAll(runRoot)
		return fmt.Errorf("create workflow manifest directory: %w", err)
	}
	rt.manifest = &worktreeManifest{
		RunID:       runID,
		PrimaryRoot: current.Root,
		BaseBranch:  current.Branch,
		BaseHead:    current.Head,
		RunRoot:     runRoot,
	}
	rt.manifestPath = filepath.Join(manifestDir, "manifest.json")
	if err := rt.writeManifest(); err != nil {
		_ = os.RemoveAll(runRoot)
		_ = os.RemoveAll(manifestDir)
		rt.manifest = nil
		return err
	}
	return nil
}

func (rt *worktreeRuntime) waitForGitLock() error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		lockPath := filepath.Join(rt.preflight.CommonDir, "index.lock")
		_, err := os.Stat(lockPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect Git index lock: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Git index remains locked at %q", lockPath)
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-rt.ctx.Done():
			timer.Stop()
			return rt.ctx.Err()
		case <-timer.C:
		}
	}
}

func (rt *worktreeRuntime) createWorktree(id, kind, base string) (*managedWorktree, error) {
	if rt.manifest == nil {
		return nil, fmt.Errorf("execution gate has not passed")
	}
	if !workspaceIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid workspace id %q: use 1-64 lowercase letters, digits, and single hyphens", id)
	}
	if kind != "lane" && kind != "integration" {
		return nil, fmt.Errorf("kind must be lane or integration")
	}
	for _, existing := range rt.manifest.Worktrees {
		if existing.ID == id {
			return nil, fmt.Errorf("workspace id %q already exists", id)
		}
		if kind == "integration" && existing.Kind == "integration" {
			return nil, fmt.Errorf("an integration workspace already exists")
		}
	}

	if base == "" {
		base = rt.manifest.BaseHead
	}
	if _, err := rt.git(rt.manifest.PrimaryRoot, "merge-base", "--is-ancestor", rt.manifest.BaseHead, base); err != nil {
		return nil, fmt.Errorf("workspace base is not descended from the recorded primary HEAD: %w", err)
	}
	handle := "workspace:" + rt.manifest.RunID + ":" + id
	branch := "ronin/" + rt.manifest.RunID + "/" + id
	path := filepath.Join(rt.manifest.RunRoot, id)
	if _, err := rt.git(rt.manifest.PrimaryRoot, "worktree", "add", "-b", branch, path, base); err != nil {
		return nil, fmt.Errorf("create %s worktree %q: %w", kind, id, err)
	}
	workspace := &managedWorktree{ID: id, Kind: kind, Handle: handle, Path: path, Branch: branch, Base: base}
	rt.manifest.Worktrees = append(rt.manifest.Worktrees, workspace)
	rt.byHandle[handle] = workspace
	if err := rt.writeManifest(); err != nil {
		return nil, err
	}
	return workspace, nil
}

func (rt *worktreeRuntime) resolveWorkspace(handle string) (string, error) {
	workspace, err := rt.workspace(handle)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	return workspace.Path, nil
}

func (rt *worktreeRuntime) workspace(handle string) (*managedWorktree, error) {
	workspace := rt.byHandle[handle]
	if workspace == nil {
		return nil, fmt.Errorf("unknown managed workspace %q", handle)
	}
	return workspace, nil
}

func (rt *worktreeRuntime) seal(workspace *managedWorktree) (string, error) {
	if workspace.Kind != "lane" {
		return "", fmt.Errorf("only lane worktrees can be sealed")
	}
	if workspace.Sealed {
		return "", fmt.Errorf("workspace %q is already sealed", workspace.ID)
	}
	if err := rt.verifyWorkspaceBranch(workspace); err != nil {
		return "", err
	}
	if _, err := rt.git(workspace.Path, "merge-base", "--is-ancestor", workspace.Base, "HEAD"); err != nil {
		return "", fmt.Errorf("workspace branch is no longer descended from recorded base: %w", err)
	}
	if _, err := rt.git(workspace.Path, "add", "--all"); err != nil {
		return "", fmt.Errorf("stage lane changes: %w", err)
	}
	staged, err := rt.git(workspace.Path, "diff", "--cached", "--quiet")
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return "", fmt.Errorf("inspect staged lane changes: %w", err)
		}
		if _, err := rt.gitCommit(workspace.Path, "chore(ronin): seal lane "+workspace.ID); err != nil {
			return "", fmt.Errorf("commit remaining lane changes: %w", err)
		}
	} else if strings.TrimSpace(staged) != "" {
		return "", fmt.Errorf("unexpected output while inspecting staged lane changes")
	}
	count, err := rt.git(workspace.Path, "rev-list", "--count", workspace.Base+"..HEAD")
	if err != nil {
		return "", fmt.Errorf("inspect lane commits: %w", err)
	}
	if strings.TrimSpace(count) == "0" {
		return "", fmt.Errorf("lane %q produced no commits", workspace.ID)
	}
	status, err := rt.status(workspace.Path)
	if err != nil {
		return "", err
	}
	if status != "" {
		return "", fmt.Errorf("lane %q remains dirty after sealing:\n%s", workspace.ID, status)
	}
	if err := rt.rejectLaneChanges(workspace); err != nil {
		return "", err
	}
	workspace.Sealed = true
	if err := rt.writeManifest(); err != nil {
		return "", err
	}
	head, err := rt.git(workspace.Path, "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}

func (rt *worktreeRuntime) rejectLaneChanges(workspace *managedWorktree) error {
	changed, err := rt.git(workspace.Path, "diff", "--name-only", workspace.Base+"..HEAD")
	if err != nil {
		return fmt.Errorf("inspect lane paths: %w", err)
	}
	for path := range strings.SplitSeq(strings.TrimSpace(changed), "\n") {
		if path == "" {
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(path))
		if clean == ".git" || strings.HasPrefix(clean, ".git/") || clean == ".gitmodules" {
			return fmt.Errorf("lane %q changed protected Git metadata path %q", workspace.ID, path)
		}
	}
	return nil
}

func (rt *worktreeRuntime) squash(integration, lane *managedWorktree, message string) (string, error) {
	if integration.Kind != "integration" || lane.Kind != "lane" {
		return "", fmt.Errorf("squash requires an integration workspace and a lane workspace")
	}
	if integration.Sealed {
		return "", fmt.Errorf("integration workspace is sealed")
	}
	if !lane.Sealed {
		return "", fmt.Errorf("lane %q must be sealed before integration", lane.ID)
	}
	if err := validateConventionalCommit(message); err != nil {
		return "", err
	}
	if err := rt.verifyWorkspaceBranch(integration); err != nil {
		return "", err
	}
	if status, err := rt.status(integration.Path); err != nil {
		return "", err
	} else if status != "" {
		return "", fmt.Errorf("integration worktree is dirty before squash:\n%s", status)
	}
	if _, err := rt.git(integration.Path, "merge", "--squash", "--no-commit", lane.Branch); err != nil {
		return "", fmt.Errorf("squash lane %q; integration worktree retained for recovery: %w", lane.ID, err)
	}
	_, err := rt.git(integration.Path, "diff", "--cached", "--quiet")
	if err == nil {
		return "", fmt.Errorf("lane %q has no changes relative to the integration branch", lane.ID)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		return "", fmt.Errorf("inspect squashed lane %q: %w", lane.ID, err)
	}
	if _, err := rt.gitCommit(integration.Path, message); err != nil {
		return "", fmt.Errorf("commit squashed lane %q: %w", lane.ID, err)
	}
	head, err := rt.git(integration.Path, "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}

func (rt *worktreeRuntime) squashRepairs(workspace *managedWorktree, base, message string) (string, bool, error) {
	if workspace.Kind != "integration" {
		return "", false, fmt.Errorf("repairs can only be squashed in the integration workspace")
	}
	if err := validateConventionalCommit(message); err != nil {
		return "", false, err
	}
	if err := rt.verifyWorkspaceBranch(workspace); err != nil {
		return "", false, err
	}
	if workspace.Sealed {
		return "", false, fmt.Errorf("integration workspace repairs are already sealed")
	}
	if status, err := rt.status(workspace.Path); err != nil {
		return "", false, err
	} else if status != "" {
		if _, err := rt.git(workspace.Path, "add", "--all"); err != nil {
			return "", false, fmt.Errorf("stage integration repairs: %w", err)
		}
		if _, err := rt.gitCommit(workspace.Path, "chore(ronin): checkpoint integration repairs"); err != nil {
			return "", false, fmt.Errorf("commit integration repairs: %w", err)
		}
	}
	if _, err := rt.git(workspace.Path, "merge-base", "--is-ancestor", base, "HEAD"); err != nil {
		return "", false, fmt.Errorf("repair base is not an ancestor of integration HEAD: %w", err)
	}
	count, err := rt.git(workspace.Path, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return "", false, fmt.Errorf("inspect integration repairs: %w", err)
	}
	if strings.TrimSpace(count) == "0" {
		workspace.Sealed = true
		if err := rt.writeManifest(); err != nil {
			return "", false, err
		}
		return base, false, nil
	}
	if _, err := rt.git(workspace.Path, "reset", "--soft", base); err != nil {
		return "", false, fmt.Errorf("prepare integration repair squash: %w", err)
	}
	if _, err := rt.gitCommit(workspace.Path, message); err != nil {
		return "", false, fmt.Errorf("commit integration repair squash: %w", err)
	}
	head, err := rt.git(workspace.Path, "rev-parse", "HEAD")
	if err == nil {
		workspace.Sealed = true
		if manifestErr := rt.writeManifest(); manifestErr != nil {
			return "", false, manifestErr
		}
	}
	return strings.TrimSpace(head), true, err
}

func (rt *worktreeRuntime) promote(integration *managedWorktree) error {
	if integration.Kind != "integration" {
		return fmt.Errorf("only the integration workspace can be promoted")
	}
	if !integration.Sealed {
		return fmt.Errorf("integration repairs must be sealed with squash_repairs before promotion")
	}
	if rt.promoted {
		return fmt.Errorf("integration workspace was already promoted")
	}
	if status, err := rt.status(integration.Path); err != nil {
		return err
	} else if status != "" {
		return fmt.Errorf("integration worktree must be clean before promotion:\n%s", status)
	}
	current, err := rt.inspectPrimary()
	if err != nil {
		return err
	}
	if current.Root != rt.manifest.PrimaryRoot || current.Branch != rt.manifest.BaseBranch || current.Head != rt.manifest.BaseHead {
		return fmt.Errorf("primary branch or HEAD changed before promotion")
	}
	if status, err := rt.status(current.Root); err != nil {
		return err
	} else if status != "" {
		return fmt.Errorf("primary worktree became dirty before promotion:\n%s", status)
	}
	if _, err := rt.git(current.Root, "merge", "--ff-only", integration.Branch); err != nil {
		return fmt.Errorf("fast-forward primary branch: %w", err)
	}
	if err := rt.cleanupSuccess(); err != nil {
		return fmt.Errorf("promotion succeeded but cleanup failed: %w", err)
	}
	rt.promoted = true
	return nil
}

func (rt *worktreeRuntime) recover() string {
	if rt.manifest == nil || rt.promoted {
		return ""
	}
	var retained []string
	var cleanupErrors []string
	for _, workspace := range rt.manifest.Worktrees {
		status, err := rt.recoveryStatus(workspace.Path)
		if err != nil {
			retained = append(retained, fmt.Sprintf("worktree %s at %s; branch %s", workspace.ID, workspace.Path, workspace.Branch))
			cleanupErrors = append(cleanupErrors, err.Error())
			continue
		}
		if status != "" {
			retained = append(retained, fmt.Sprintf("dirty worktree %s at %s; branch %s", workspace.ID, workspace.Path, workspace.Branch))
			continue
		}
		if _, err := rt.recoveryGit(rt.manifest.PrimaryRoot, "worktree", "remove", workspace.Path); err != nil {
			retained = append(retained, fmt.Sprintf("worktree %s at %s; branch %s", workspace.ID, workspace.Path, workspace.Branch))
			cleanupErrors = append(cleanupErrors, err.Error())
			continue
		}
		retained = append(retained, fmt.Sprintf("branch %s", workspace.Branch))
	}
	_ = rt.writeManifest()
	sort.Strings(retained)
	var b strings.Builder
	b.WriteString("Workflow recovery artifacts retained:\n")
	for _, item := range retained {
		b.WriteString("- ")
		b.WriteString(item)
		b.WriteString("\n")
	}
	b.WriteString("Manifest: ")
	b.WriteString(rt.manifestPath)
	b.WriteString("\nManual cleanup after inspection:\n")
	for _, workspace := range rt.manifest.Worktrees {
		fmt.Fprintf(&b, "- git -C %q worktree remove %q\n", rt.manifest.PrimaryRoot, workspace.Path)
		fmt.Fprintf(&b, "- git -C %q branch -D %q\n", rt.manifest.PrimaryRoot, workspace.Branch)
	}
	if len(cleanupErrors) > 0 {
		b.WriteString("Cleanup errors:\n- ")
		b.WriteString(strings.Join(cleanupErrors, "\n- "))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func (rt *worktreeRuntime) cleanupSuccess() error {
	var errs []error
	for _, workspace := range slices.Backward(rt.manifest.Worktrees) {
		if _, err := rt.git(rt.manifest.PrimaryRoot, "worktree", "remove", workspace.Path); err != nil {
			errs = append(errs, fmt.Errorf("remove worktree %s: %w", workspace.ID, err))
			continue
		}
		if _, err := rt.git(rt.manifest.PrimaryRoot, "branch", "-D", workspace.Branch); err != nil {
			errs = append(errs, fmt.Errorf("remove branch %s: %w", workspace.Branch, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if err := os.RemoveAll(rt.manifest.RunRoot); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Dir(rt.manifestPath)); err != nil {
		return err
	}
	return nil
}

func (rt *worktreeRuntime) verifyWorkspaceBranch(workspace *managedWorktree) error {
	branch, err := rt.git(workspace.Path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) != workspace.Branch {
		return fmt.Errorf("workspace %q is not on assigned branch %q", workspace.ID, workspace.Branch)
	}
	return nil
}

func (rt *worktreeRuntime) fingerprint(path string) (string, error) {
	if path == "" {
		path = rt.workingDir
	}
	head, err := rt.git(path, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	branch, err := rt.git(path, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return "", err
	}
	status, err := rt.status(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(head) + "\n" + strings.TrimSpace(branch) + "\n" + status, nil
}

func (rt *worktreeRuntime) status(path string) (string, error) {
	status, err := rt.git(path, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return "", fmt.Errorf("inspect worktree %q: %w", path, err)
	}
	return strings.TrimRight(status, "\n"), nil
}

func (rt *worktreeRuntime) recoveryStatus(path string) (string, error) {
	status, err := rt.recoveryGit(path, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return "", fmt.Errorf("inspect worktree %q: %w", path, err)
	}
	return strings.TrimRight(status, "\n"), nil
}

func (rt *worktreeRuntime) gitCommit(path, message string) (string, error) {
	return rt.git(path,
		"-c", "user.name=Ronin Workflow",
		"-c", "user.email=workflow@ronin.local",
		"commit", "--no-gpg-sign", "-m", message,
	)
}

func (rt *worktreeRuntime) git(path string, args ...string) (string, error) {
	return rt.runGit(rt.ctx, path, args...)
}

func (rt *worktreeRuntime) recoveryGit(path string, args ...string) (string, error) {
	return rt.runGit(context.Background(), path, args...)
}

func (rt *worktreeRuntime) runGit(ctx context.Context, path string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	commandArgs := append([]string{"-C", path}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	output, err := cmd.CombinedOutput()
	text := string(output)
	if err != nil {
		return text, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(text))
	}
	return text, nil
}

func (rt *worktreeRuntime) writeManifest() error {
	data, err := json.MarshalIndent(rt.manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workflow manifest: %w", err)
	}
	temporary := rt.manifestPath + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write workflow manifest: %w", err)
	}
	if err := os.Rename(temporary, rt.manifestPath); err != nil {
		return fmt.Errorf("publish workflow manifest: %w", err)
	}
	return nil
}

func validateConventionalCommit(message string) error {
	message = strings.TrimSpace(message)
	header, _, _ := strings.Cut(message, "\n")
	matches := conventionalCommitHeader.FindStringSubmatch(header)
	if matches == nil {
		return fmt.Errorf("commit message must use Conventional Commits: <type>(<scope>): <description>")
	}
	description := matches[4]
	if strings.HasSuffix(description, ".") {
		return fmt.Errorf("Conventional Commit description must not end with a period")
	}
	first := description[0]
	if first < 'a' || first > 'z' {
		return fmt.Errorf("Conventional Commit description must start with a lowercase letter")
	}
	if matches[3] == "!" && !strings.Contains(message, "\nBREAKING CHANGE: ") {
		return fmt.Errorf("breaking Conventional Commit requires a BREAKING CHANGE footer")
	}
	return nil
}

func randomRunID() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102t150405") + "-" + hex.EncodeToString(value[:]), nil
}

func pushWorkspace(state *lua.State, workspace *managedWorktree) {
	state.NewTable()
	setLuaStringField(state, "id", workspace.ID)
	setLuaStringField(state, "kind", workspace.Kind)
	setLuaStringField(state, "handle", workspace.Handle)
	setLuaStringField(state, "branch", workspace.Branch)
}

func setLuaStringField(state *lua.State, name, value string) {
	state.PushString(value)
	state.SetField(-2, name)
}
