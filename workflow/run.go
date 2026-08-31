package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	lua "github.com/Shopify/go-lua"
	"github.com/crowl/ronin/tool/fsutil"
)

const controlSignalMarker = "__ronin_workflow_control_signal__"
const maxReadBytes int64 = 1 << 20

const (
	approvedMarker = "STATUS: APPROVED"
	changesMarker  = "STATUS: CHANGES_REQUIRED"
)

type controlSignal struct {
	kind    controlSignalKind
	message string
}

type controlSignalKind string

const (
	controlSignalDone controlSignalKind = "done"
	controlSignalFail controlSignalKind = "fail"
)

// DoneError reports an intentional successful workflow stop.
type DoneError struct {
	Message string
}

func (e *DoneError) Error() string {
	if e == nil || e.Message == "" {
		return "workflow done"
	}
	return "workflow done: " + e.Message
}

// FailureError reports an intentional failed workflow stop.
type FailureError struct {
	Message string
}

func (e *FailureError) Error() string {
	if e == nil || e.Message == "" {
		return "workflow failed"
	}
	return "workflow failed: " + e.Message
}

// Run executes a named workflow and reports structured progress.
func Run(ctx context.Context, item Workflow, workingDir, input string, agent AgentFunc, emit func(Event)) Result {
	result := Result{Name: item.Name, Input: input, Status: StatusCompleted, Summary: "Workflow completed"}
	if emit != nil {
		emit(Started{Name: item.Name})
	}
	if len(input) > 1<<20 {
		result.Status = StatusFailed
		result.Summary = "workflow input exceeds the 1 MiB limit"
		if emit != nil {
			emit(Finished{Result: result})
		}
		return result
	}

	err := runFile(ctx, item.Path, workingDir, input, io.Discard, agent, emit)
	if err != nil {
		var doneErr *DoneError
		var failureErr *FailureError
		switch {
		case ctx.Err() != nil:
			result.Status = StatusCancelled
			result.Summary = "Workflow cancelled"
		case errors.Is(err, context.Canceled):
			result.Status = StatusCancelled
			result.Summary = "Workflow cancelled"
		case errors.As(err, &doneErr):
			if doneErr.Message != "" {
				result.Summary = doneErr.Message
			}
		case errors.As(err, &failureErr):
			result.Status = StatusFailed
			result.Summary = failureErr.Message
			if result.Summary == "" {
				result.Summary = "Workflow failed"
			}
		default:
			result.Status = StatusFailed
			result.Summary = err.Error()
		}
	}
	if emit != nil {
		emit(Finished{Result: result})
	}
	return result
}

// RunFile loads and executes a Lua workflow script.
func RunFile(path string, out io.Writer) error {
	return RunFileWithAgent(context.Background(), path, out, nil)
}

// RunFileWithAgent loads and executes a Lua workflow script with ronin.run_agent available.
func RunFileWithAgent(ctx context.Context, path string, out io.Writer, agent AgentFunc) error {
	return RunFileWithAgentInWorkingDir(ctx, path, "", out, agent)
}

// RunFileWithAgentInWorkingDir loads and executes a Lua workflow script with an explicit workflow working directory.
func RunFileWithAgentInWorkingDir(ctx context.Context, path, workingDir string, out io.Writer, agent AgentFunc) error {
	return RunFileWithAgentInputInWorkingDir(ctx, path, workingDir, "", out, agent)
}

// RunFileWithAgentInputInWorkingDir loads and executes a Lua workflow script with an explicit input and working directory.
func RunFileWithAgentInputInWorkingDir(ctx context.Context, path, workingDir, input string, out io.Writer, agent AgentFunc) error {
	return runFile(ctx, path, workingDir, input, out, agent, nil)
}

func runFile(ctx context.Context, path, workingDir, input string, out io.Writer, agent AgentFunc, emit func(Event)) error {
	if out == nil {
		out = io.Discard
	}
	if ctx == nil {
		ctx = context.Background()
	}

	script, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read workflow script %q: %w", path, err)
	}

	runtimeWorkingDir := workingDir
	if strings.TrimSpace(runtimeWorkingDir) == "" {
		runtimeWorkingDir = filepath.Dir(path)
	}
	if absWorkingDir, err := filepath.Abs(runtimeWorkingDir); err == nil {
		runtimeWorkingDir = absWorkingDir
	}

	state := lua.NewState()
	lua.Require(state, "base", lua.BaseOpen, true)
	state.PushNil()
	state.SetGlobal("dofile")
	state.PushNil()
	state.SetGlobal("loadfile")
	state.PushNil()
	state.SetGlobal("pcall")
	state.PushNil()
	state.SetGlobal("xpcall")
	state.PushNil()
	state.SetGlobal("rawset")
	lua.Require(state, "table", lua.TableOpen, true)
	lua.Require(state, "string", lua.StringOpen, true)
	lua.Require(state, "math", lua.MathOpen, true)

	var signal *controlSignal
	agentRuntime := newAgentRuntime(ctx, agent, emit)
	agentRuntime.worktrees.setWorkingDir(runtimeWorkingDir)
	registerRonin(state, out, &signal, agentRuntime, runtimeWorkingDir, input, emit)

	if err := lua.LoadBuffer(state, string(script), path, ""); err != nil {
		return fmt.Errorf("parse workflow script %q: %w", path, err)
	}

	if err := state.ProtectedCall(0, 0, 0); err != nil {
		agentRuntime.shutdown()
		recovery := agentRuntime.worktrees.recover()
		if signal != nil && strings.Contains(err.Error(), controlSignalMarker) {
			message := signal.message
			if recovery != "" {
				message = strings.TrimSpace(message + "\n\n" + recovery)
			}
			switch signal.kind {
			case controlSignalDone:
				return &DoneError{Message: message}
			case controlSignalFail:
				return &FailureError{Message: message}
			}
		}
		if recovery != "" {
			return fmt.Errorf("run workflow script %q: %w\n\n%s", path, err, recovery)
		}
		return fmt.Errorf("run workflow script %q: %w", path, err)
	}
	agentRuntime.shutdown()
	if recovery := agentRuntime.worktrees.recover(); recovery != "" {
		return fmt.Errorf("workflow ended without promoting its managed integration worktree\n\n%s", recovery)
	}

	return nil
}

func registerRonin(state *lua.State, out io.Writer, signal **controlSignal, agent *agentRuntime, workingDir, input string, emit func(Event)) {
	state.NewTable()
	state.PushGoFunction(logFunction(out, emit))
	state.SetField(-2, "log")
	state.PushGoFunction(runAgentFunction(agent))
	state.SetField(-2, "run_agent")
	state.PushGoFunction(startAgentFunction(agent))
	state.SetField(-2, "start_agent")
	state.PushGoFunction(waitAnyFunction(agent))
	state.SetField(-2, "wait_any")
	state.PushGoFunction(gitPreflightFunction(agent.worktrees))
	state.SetField(-2, "git_preflight")
	state.PushGoFunction(gitExecutionGateFunction(agent.worktrees))
	state.SetField(-2, "git_execution_gate")
	state.PushGoFunction(createWorktreeFunction(agent.worktrees))
	state.SetField(-2, "create_worktree")
	state.PushGoFunction(sealWorktreeFunction(agent.worktrees))
	state.SetField(-2, "seal_worktree")
	state.PushGoFunction(squashWorktreeFunction(agent.worktrees))
	state.SetField(-2, "squash_worktree")
	state.PushGoFunction(worktreeHeadFunction(agent.worktrees))
	state.SetField(-2, "worktree_head")
	state.PushGoFunction(squashRepairsFunction(agent.worktrees))
	state.SetField(-2, "squash_repairs")
	state.PushGoFunction(promoteWorktreeFunction(agent.worktrees))
	state.SetField(-2, "promote_worktree")
	state.PushGoFunction(validateCommitFunction())
	state.SetField(-2, "valid_commit")
	state.PushGoFunction(readFunction(workingDir))
	state.SetField(-2, "read")
	state.PushGoFunction(requireInputFunction(input, signal))
	state.SetField(-2, "require_input")
	state.PushGoFunction(approvedFunction())
	state.SetField(-2, "approved")
	state.PushGoFunction(doneFunction(signal))
	state.SetField(-2, "done")
	state.PushGoFunction(failFunction(signal))
	state.SetField(-2, "fail")
	state.NewTable()
	state.PushBoolean(false)
	state.SetField(-2, "__metatable")
	state.PushGoFunction(workflowIndexFunction(input))
	state.SetField(-2, "__index")
	state.PushGoFunction(workflowNewIndexFunction())
	state.SetField(-2, "__newindex")
	state.SetMetaTable(-2)
	state.SetGlobal("ronin")
}

func workflowIndexFunction(input string) lua.Function {
	return func(state *lua.State) int {
		key, ok := state.ToString(2)
		if ok && key == "input" {
			state.PushString(input)
			return 1
		}
		state.PushNil()
		return 1
	}
}

func workflowNewIndexFunction() lua.Function {
	return func(state *lua.State) int {
		key, ok := state.ToString(2)
		if ok && key == "input" {
			panic(lua.RuntimeError("ronin.input is read-only"))
		}
		state.PushValue(2)
		state.PushValue(3)
		state.RawSet(1)
		return 0
	}
}

func logFunction(out io.Writer, emit func(Event)) lua.Function {
	return func(state *lua.State) int {
		text := formatLogValue(state, 1, map[any]bool{})
		_, _ = fmt.Fprintln(out, text)
		if emit != nil {
			emit(Log{Text: text})
		}
		return 0
	}
}

func readFunction(workingDir string) lua.Function {
	return func(state *lua.State) int {
		path, err := readPathArgument(state, 1)
		if err != nil {
			panic(lua.RuntimeError("ronin.read: " + err.Error()))
		}

		result, err := readWorkflowFile(workingDir, path)
		if err != nil {
			panic(lua.RuntimeError("ronin.read: " + err.Error()))
		}

		state.NewTable()
		state.PushBoolean(true)
		state.SetField(-2, "ok")
		state.PushString(result.Path)
		state.SetField(-2, "path")
		state.PushNumber(float64(result.Size))
		state.SetField(-2, "size")
		state.PushString(result.SHA256)
		state.SetField(-2, "sha256")
		state.PushString(result.Content)
		state.SetField(-2, "content")
		return 1
	}
}

func requireInputFunction(input string, signal **controlSignal) lua.Function {
	return func(state *lua.State) int {
		message, err := requiredStringArgument(state, 1, "ronin.require_input", "message")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		if strings.TrimSpace(input) == "" {
			*signal = &controlSignal{kind: controlSignalFail, message: message}
			lua.Errorf(state, controlSignalMarker)
			panic("unreachable")
		}
		state.PushString(input)
		return 1
	}
}

func approvedFunction() lua.Function {
	return func(state *lua.State) int {
		text, err := requiredStringArgument(state, 1, "ronin.approved", "text")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		state.PushBoolean(approved(text))
		return 1
	}
}

func approved(text string) bool {
	trimmed := strings.TrimRight(text, " \n\r\t\f\v")
	if trimmed != approvedMarker && !strings.HasSuffix(trimmed, "\n"+approvedMarker) {
		return false
	}

	preceding := strings.TrimSuffix(trimmed, approvedMarker)
	return !strings.Contains(preceding, approvedMarker) && !strings.Contains(preceding, changesMarker)
}

func requiredStringArgument(state *lua.State, index int, function, argument string) (string, error) {
	if state.IsNoneOrNil(index) {
		return "", fmt.Errorf("%s %s is required", function, argument)
	}
	if state.TypeOf(index) != lua.TypeString {
		return "", fmt.Errorf("%s %s must be a string", function, argument)
	}
	value, _ := state.ToString(index)
	return value, nil
}

func doneFunction(signal **controlSignal) lua.Function {
	return func(state *lua.State) int {
		*signal = &controlSignal{kind: controlSignalDone, message: stringArgument(state, 1)}
		lua.Errorf(state, controlSignalMarker)
		panic("unreachable")
	}
}

func failFunction(signal **controlSignal) lua.Function {
	return func(state *lua.State) int {
		*signal = &controlSignal{kind: controlSignalFail, message: stringArgument(state, 1)}
		lua.Errorf(state, controlSignalMarker)
		panic("unreachable")
	}
}

func stringArgument(state *lua.State, index int) string {
	if state.IsNoneOrNil(index) {
		return ""
	}
	if value, ok := state.ToString(index); ok {
		return value
	}
	return formatLogValue(state, index, map[any]bool{})
}

func readPathArgument(state *lua.State, index int) (string, error) {
	if state.IsNoneOrNil(index) {
		return "", fmt.Errorf("path is required")
	}
	if state.TypeOf(index) != lua.TypeString {
		return "", fmt.Errorf("path must be a string")
	}
	path, _ := state.ToString(index)
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.ContainsRune(path, '\x00') {
		return "", fmt.Errorf("path must not contain NUL bytes")
	}
	return path, nil
}

type workflowReadResult struct {
	Path    string
	Size    int64
	SHA256  string
	Content string
}

func readWorkflowFile(workingDir, path string) (workflowReadResult, error) {
	resolved, err := fsutil.ResolvePath(workingDir, path)
	if err != nil {
		return workflowReadResult{}, err
	}

	file, err := os.Open(resolved.Abs)
	if err != nil {
		if os.IsNotExist(err) {
			return workflowReadResult{}, fmt.Errorf("file does not exist")
		}
		return workflowReadResult{}, err
	}
	defer func() {
		_ = file.Close()
	}()

	info, err := file.Stat()
	if err != nil {
		return workflowReadResult{}, err
	}
	if info.IsDir() {
		return workflowReadResult{}, fmt.Errorf("path is a directory")
	}
	if !info.Mode().IsRegular() {
		return workflowReadResult{}, fmt.Errorf("path is not a regular file")
	}
	if info.Size() > maxReadBytes {
		return workflowReadResult{}, fmt.Errorf("file exceeds maximum size of %d bytes", maxReadBytes)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxReadBytes+1))
	if err != nil {
		return workflowReadResult{}, err
	}
	if int64(len(data)) > maxReadBytes {
		return workflowReadResult{}, fmt.Errorf("file exceeds maximum size of %d bytes", maxReadBytes)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return workflowReadResult{}, fmt.Errorf("file appears to be binary")
	}

	sum := sha256.Sum256(data)
	return workflowReadResult{
		Path:    resolved.Display,
		Size:    int64(len(data)),
		SHA256:  hex.EncodeToString(sum[:]),
		Content: string(data),
	}, nil
}

func formatLogValue(state *lua.State, index int, seen map[any]bool) string {
	switch state.TypeOf(index) {
	case lua.TypeNil:
		return "nil"
	case lua.TypeBoolean:
		if state.ToBoolean(index) {
			return "true"
		}
		return "false"
	case lua.TypeNumber, lua.TypeString:
		value, _ := state.ToString(index)
		return value
	case lua.TypeTable:
		data := luaTableToAny(state, index, seen)
		bytes, err := json.MarshalIndent(data, "", "  ")
		if err == nil {
			return string(bytes)
		}
	}
	return state.TypeOf(index).String()
}

func luaTableToAny(state *lua.State, index int, seen map[any]bool) any {
	value := state.ToValue(index)
	if seen[value] {
		return "<cycle>"
	}
	seen[value] = true
	defer delete(seen, value)

	absoluteIndex := index
	if index < 0 {
		absoluteIndex = state.Top() + index + 1
	}

	result := make(map[string]any)
	state.PushNil()
	for state.Next(absoluteIndex) {
		key := formatLogValue(state, -2, seen)
		result[key] = luaValueToAny(state, -1, seen)
		state.Pop(1)
	}
	return result
}

func luaValueToAny(state *lua.State, index int, seen map[any]bool) any {
	switch state.TypeOf(index) {
	case lua.TypeNil:
		return nil
	case lua.TypeBoolean:
		return state.ToBoolean(index)
	case lua.TypeString:
		value, _ := state.ToString(index)
		return value
	case lua.TypeNumber:
		value, _ := state.ToNumber(index)
		return value
	case lua.TypeTable:
		return luaTableToAny(state, index, seen)
	default:
		return state.TypeOf(index).String()
	}
}
