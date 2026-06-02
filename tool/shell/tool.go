package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/fsutil"
)

type Args struct {
	Command        string            `json:"command" jsonschema:"Command passed to sh -c."`
	Workdir        string            `json:"workdir,omitempty" jsonschema:"Working directory. Relative paths resolve from the current working directory; absolute paths are allowed."`
	TimeoutMS      int               `json:"timeout_ms,omitempty" jsonschema:"Timeout in milliseconds. Defaults to 30000 and is capped at 90000."`
	Stdin          string            `json:"stdin,omitempty" jsonschema:"Optional standard input for the command."`
	Env            map[string]string `json:"env,omitempty" jsonschema:"Optional extra environment variables. Invalid names are ignored."`
	MaxOutputBytes int64             `json:"max_output_bytes,omitempty" jsonschema:"Maximum bytes to capture from stdout and stderr. Extra output is truncated."`
}

func (a Args) Validate() error {
	if strings.TrimSpace(a.Command) == "" {
		return tool.Error{Code: "invalid_args", Message: "command must not be empty"}
	}
	if strings.ContainsRune(a.Command, '\x00') {
		return tool.Error{Code: "invalid_args", Message: "command must not contain NUL bytes"}
	}
	if strings.ContainsRune(a.Workdir, '\x00') {
		return tool.Error{Code: "invalid_args", Message: "workdir must not contain NUL bytes"}
	}
	return nil
}

type Result struct {
	Command         string `json:"command"`
	Workdir         string `json:"workdir"`
	ExitCode        int    `json:"exit_code"`
	Success         bool   `json:"success"`
	Stdout          string `json:"stdout,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	DurationMS      int64  `json:"duration_ms"`
	TimedOut        bool   `json:"timed_out,omitempty"`
	CleanupTimedOut bool   `json:"cleanup_timed_out,omitempty"`
}

func (r Result) Artifacts() []tool.Artifact {
	artifacts := make([]tool.Artifact, 0, 2)
	if r.Stdout != "" {
		artifacts = append(artifacts, tool.ShellStreamArtifact{
			Stream:  tool.ShellStreamStdout,
			Content: r.Stdout,
		})
	}
	if r.Stderr != "" {
		artifacts = append(artifacts, tool.ShellStreamArtifact{
			Stream:  tool.ShellStreamStderr,
			Content: r.Stderr,
		})
	}
	return artifacts
}

func New(cwd string) *Tool {
	return &Tool{cwd: cwd, policy: AllowAll{}}
}

type Tool struct {
	cwd    string
	policy Policy
}

func (t *Tool) Name() string {
	return "shell"
}

func (t *Tool) Description() string {
	return "Run a shell command. Use for listing, searching, git, tests, builds, and other CLI tasks. Prefer read_file for exact file contents and edit_file/write_file for file modifications."
}

func (t *Tool) Parameters() *jsonschema.Schema {
	return jsonschema.FromType[Args]()
}

func (t *Tool) Call(ctx context.Context, rawArgs json.RawMessage) (any, error) {
	return tool.CallTyped(ctx, rawArgs, t.call)
}

func (t *Tool) CallIncremental(ctx context.Context, rawArgs json.RawMessage, emit func(tool.Artifact) error) (any, error) {
	return tool.CallTyped(ctx, rawArgs, func(ctx context.Context, args Args) (Result, error) {
		return t.callIncremental(ctx, args, emit)
	})
}

func (t *Tool) CallTitle(rawArgs json.RawMessage) (string, error) {
	args, err := tool.DecodeArgs[Args](rawArgs)
	if err != nil {
		return "", err
	}
	return "$ " + args.Command, nil
}

const (
	defaultTimeout        = 30_000 * time.Millisecond
	maxTimeout            = 90_000 * time.Millisecond
	cleanupTimeout        = 2_000 * time.Millisecond
	defaultMaxOutputBytes = 128 * 1024
	maxOutputBytes        = 8 * 1024 * 1024
)

func (t *Tool) call(ctx context.Context, args Args) (Result, error) {
	return t.callIncremental(ctx, args, nil)
}

func (t *Tool) callIncremental(ctx context.Context, args Args, emit func(tool.Artifact) error) (Result, error) {
	if t.policy != nil {
		if err := t.policy.Allow(args); err != nil {
			return Result{}, err
		}
	}

	workdir := args.Workdir
	if workdir == "" {
		workdir = "."
	}

	resolved, err := fsutil.ResolvePath(t.cwd, workdir)
	if err != nil {
		return Result{}, err
	}

	timeout := defaultTimeout
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}

	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command("sh", "-c", args.Command)

	configureProcessGroup(cmd)

	cmd.Dir = resolved.Abs

	if args.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(args.Stdin)
	}

	// FIXME might want to be more careful with this env
	cmd.Env = buildEnv(args.Env)

	var stdout, stderr limitedBuffer
	stdout.limit = outputLimit(args.MaxOutputBytes, defaultMaxOutputBytes)
	stderr.limit = outputLimit(args.MaxOutputBytes, defaultMaxOutputBytes)

	cmd.Stdout = newStreamingWriter(tool.ShellStreamStdout, &stdout, emit)
	cmd.Stderr = newStreamingWriter(tool.ShellStreamStderr, &stderr, emit)

	start := time.Now()

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("failed to start command: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var waitErr error
	timedOut := false
	cleanupTimedOut := false

	select {
	case waitErr = <-done:
	case <-cmdCtx.Done():
		killProcessGroup(cmd)
		select {
		case waitErr = <-done:
		case <-time.After(cleanupTimeout):
			cleanupTimedOut = true
		}
		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			timedOut = true
		} else {
			return Result{}, cmdCtx.Err()
		}
	}

	duration := time.Since(start)

	exitCode := 0
	success := true

	if timedOut {
		success = false
		exitCode = -1
	} else if waitErr != nil {
		success = false
		if exitErr, ok := errors.AsType[*exec.ExitError](waitErr); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return Result{}, waitErr
		}
	}

	return Result{
		Command:         args.Command,
		Workdir:         resolved.Display,
		ExitCode:        exitCode,
		Success:         success,
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		StdoutTruncated: stdout.truncated,
		StderrTruncated: stderr.truncated,
		DurationMS:      duration.Milliseconds(),
		TimedOut:        timedOut,
		CleanupTimedOut: cleanupTimedOut,
	}, nil
}

func buildEnv(extra map[string]string) []string {
	env := os.Environ()

	for k, v := range extra {
		if !validEnvKey(k) {
			continue
		}
		env = append(env, k+"="+v)
	}

	return env
}

func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		if r == '=' || r == '\x00' {
			return false
		}
	}
	return true
}
