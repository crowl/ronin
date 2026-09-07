package tui

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/session"
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
				if !strings.Contains(app.model.boxes[app.model.shellOutputIndex].(systemMessageBox).Text, "[output truncated]") {
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
			if len(restored.boxes) < 3 {
				t.Fatal("shell history not displayed")
			}
		})
	}
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
