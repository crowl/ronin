package tui

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/shell"
)

type shellConversationFake struct {
	fakeConversation
	history []session.Event
	saveErr error
}

func (c *shellConversationFake) RecordShellEvent(e session.Event) error {
	if c.saveErr != nil {
		return c.saveErr
	}
	c.history = append(c.history, e)
	return nil
}
func (c *shellConversationFake) DisplayHistory() []session.Event { return c.history }

func TestParseShellSubmission(t *testing.T) {
	for _, test := range []struct {
		input, command string
		recognized     bool
	}{
		{"!echo hi", "echo hi", true}, {"!", "", true}, {"! \n\t", "", true},
		{" !echo hi", "", false}, {"hello!", "", false}, {"", "", false},
	} {
		command, recognized := parseShellSubmission(test.input)
		if command != test.command || recognized != test.recognized {
			t.Errorf("parse(%q) = %q, %v", test.input, command, recognized)
		}
	}
}

func TestShellSubmissionLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell commands")
	}
	for _, test := range []struct {
		name, command       string
		cancel, saveFailure bool
		truncated           bool
	}{
		{name: "truncated", command: "head -c 200000 /dev/zero | tr '\\000' x", truncated: true},
		{name: "failure", command: "printf hello; printf problem >&2; exit 7"},
		{name: "cancel", command: "printf ready; sleep 30", cancel: true},
		{name: "save failure", command: "echo must-not-run", saveFailure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newTestApp(t, testAppConfig{})
			conversation := &shellConversationFake{}
			if test.saveFailure {
				conversation.saveErr = errors.New("disk full")
			}
			app.conversation = conversation
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			app.submitPrompt(ctx, "!"+test.command)
			app.submitPrompt(ctx, "normal prompt while shell runs")
			app.submitPrompt(ctx, "!echo busy")
			if app.model.steeringPrompt != "" {
				t.Fatal("shell operation queued steering")
			}
			streaming := false
		loop:
			for {
				select {
				case <-ctx.Done():
					t.Fatal("shell did not complete")
				case event := <-app.events:
					if _, ok := event.(shellOutputReceived); ok {
						streaming = true
						if test.cancel {
							app.cancelFunc()
						}
					}
					if err := app.handleAppEvent(ctx, event); err != nil {
						t.Fatal(err)
					}
					if _, ok := event.(shellCommandDone); ok {
						break loop
					}
				}
			}
			app.workers.Wait()
			if app.model.working || app.model.shellRunning {
				t.Fatal("busy flag not cleared")
			}
			if test.saveFailure {
				if streaming || len(conversation.history) != 0 {
					t.Fatal("executed after persistence failure")
				}
				return
			}
			if !streaming {
				t.Fatal("no live output")
			}
			if len(conversation.history) != 4 {
				t.Fatalf("history: %+v", conversation.history)
			}
			status := conversation.history[3].ShellStatus
			if test.cancel {
				if status.Status != session.ShellCanceled || !strings.Contains(conversation.history[1].ShellOutput.Text, "ready") {
					t.Fatalf("cancellation lost output/status: %+v", conversation.history)
				}
			} else if test.truncated {
				output := conversation.history[1].ShellOutput
				if !output.Truncated || len(output.Text) != session.MaxShellOutputBytes || status.Status != session.ShellSucceeded {
					t.Fatalf("truncation: %+v", status)
				}
				if !strings.Contains(renderedShellOutput(app.model.boxes), "[output truncated]") {
					t.Fatal("missing truncation marker")
				}
			} else if status.Status != session.ShellFailed || status.ExitCode != 7 {
				t.Fatalf("status: %+v", status)
			}
			restored, err := newAppModel([]Command{Exit{}})
			if err != nil {
				t.Fatal(err)
			}
			restored.populateInitialBoxes(conversation)
			if len(restored.boxes) < 2 {
				t.Fatal("shell history not displayed")
			}
		})
	}
}

func TestShellOutputDisplay(t *testing.T) {
	m, err := newAppModel([]Command{Exit{}})
	if err != nil {
		t.Fatal(err)
	}
	m.startShell("printf output")
	m.shellOutput(shellOutputReceived{Stream: tool.ShellStreamStdout, Text: "output"})
	m.shellOutput(shellOutputReceived{Stream: tool.ShellStreamStderr, Text: "problem"})
	m.finishShell("printf output", shell.Result{Command: "printf output", ExitCode: 7, Stdout: "output", Stderr: "problem"}, nil)

	output := m.boxes[m.shellOutputIndex].(shellOutputBox)
	lines := renderBoxLines(output, 80, false)
	plain := strings.Join(plainLines(lines), "\n")
	metadata := strings.ToLower(plain)
	if strings.Contains(metadata, "stdout:") || strings.Contains(metadata, "stderr:") || strings.Contains(metadata, "exit code") {
		t.Fatalf("shell metadata displayed:\n%s", plain)
	}
	if !strings.Contains(plain, "output") || !strings.Contains(plain, "problem") {
		t.Fatalf("shell output missing:\n%s", plain)
	}
	if !strings.Contains(lines[len(lines)-1], errorStyle.start()) {
		t.Fatalf("stderr is not red: %q", lines[len(lines)-1])
	}
}

func TestRenderShellOutputStreams(t *testing.T) {
	for _, test := range []struct {
		name string
		box  shellOutputBox
		red  bool
	}{
		{name: "stdout", box: shellOutputBox{Stdout: "output"}},
		{name: "stderr", box: shellOutputBox{Stderr: "problem"}, red: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			lines := renderBoxLines(test.box, 80, false)
			if got := strings.Join(plainLines(lines), "\n"); got != " "+test.box.Stdout+test.box.Stderr {
				t.Fatalf("rendered output = %q", got)
			}
			if got := strings.Contains(lines[0], errorStyle.start()); got != test.red {
				t.Fatalf("red style = %v, want %v: %q", got, test.red, lines[0])
			}
		})
	}
}

func TestShellOutputHistoryDisplay(t *testing.T) {
	c := &shellConversationFake{history: []session.Event{
		{Type: session.EventShellCommand, ShellCommand: &session.ShellCommandEntry{ID: "shell-1", Command: "test"}},
		{Type: session.EventShellOutput, ShellOutput: &session.ShellOutputEntry{ID: "shell-1", Stream: session.ShellStdout, Text: "output"}},
		{Type: session.EventShellOutput, ShellOutput: &session.ShellOutputEntry{ID: "shell-1", Stream: session.ShellStderr, Text: "problem"}},
		{Type: session.EventShellStatus, ShellStatus: &session.ShellStatusEntry{ID: "shell-1", Status: session.ShellFailed, ExitCode: 7, HasExitCode: true}},
	}}
	m, err := newAppModel([]Command{Exit{}})
	if err != nil {
		t.Fatal(err)
	}
	m.populateInitialBoxes(c)

	if len(m.boxes) != 2 {
		t.Fatalf("history boxes = %#v, want command and combined output", m.boxes)
	}
	output := m.boxes[1].(shellOutputBox)
	lines := renderBoxLines(output, 80, false)
	plain := strings.Join(plainLines(lines), "\n")
	metadata := strings.ToLower(plain)
	if strings.Contains(metadata, "stdout:") || strings.Contains(metadata, "stderr:") || strings.Contains(metadata, "exit code") {
		t.Fatalf("shell metadata displayed:\n%s", plain)
	}
	if !strings.Contains(lines[len(lines)-1], errorStyle.start()) {
		t.Fatalf("restored stderr is not red: %q", lines[len(lines)-1])
	}
}

func renderedShellOutput(boxes []box) string {
	var rendered []string
	for _, box := range boxes {
		if output, ok := box.(shellOutputBox); ok {
			rendered = append(rendered, plainLines(renderBoxLines(output, 80, false))...)
		}
	}
	return strings.Join(rendered, "\n")
}

func TestShellShutdownPersistsCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell commands")
	}
	app := newTestApp(t, testAppConfig{})
	c := &shellConversationFake{}
	app.conversation = c
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	app.submitPrompt(ctx, "!printf ready; sleep 30")
	select {
	case <-app.events:
	case <-time.After(5 * time.Second):
		t.Fatal("no streaming output")
	}
	cancel()
	done := make(chan struct{})
	go func() { app.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not clean up shell worker")
	}
	if len(c.history) != 4 || c.history[3].ShellStatus.Status != session.ShellCanceled {
		t.Fatalf("shutdown history: %+v", c.history)
	}
}

func TestShellHistoryInterruptedAndSafeText(t *testing.T) {
	c := &shellConversationFake{history: []session.Event{{Type: session.EventShellCommand, ShellCommand: &session.ShellCommandEntry{ID: "unfinished", Command: "echo test"}}}}
	m, err := newAppModel([]Command{Exit{}})
	if err != nil {
		t.Fatal(err)
	}
	m.populateInitialBoxes(c)
	last := m.boxes[len(m.boxes)-1].(systemMessageBox).Text
	if !strings.Contains(last, "interrupted") {
		t.Fatalf("status: %s", last)
	}
	if got := shellText("\x1b[2Jhi\x07\r\u009b"); strings.ContainsAny(got, "\x1b\x07\r\u009b") {
		t.Fatalf("unsafe output: %q", got)
	}
}
