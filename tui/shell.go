package tui

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"unicode"

	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/shell"
)

type shellHistory interface {
	RecordShellEvent(session.Event) error
	DisplayHistory() []session.Event
}

// parseShellSubmission recognizes only a literal leading exclamation mark.
func parseShellSubmission(text string) (string, bool) {
	if text == "" || text[0] != '!' {
		return "", false
	}
	command := text[1:]
	if strings.TrimSpace(command) == "" {
		return "", true
	}
	return command, true
}

// Shell output is plain text, never executable terminal control sequences.
func shellText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, strings.ToValidUTF8(text, "�"))
}

func (app *app) startShell(ctx context.Context, command string) {
	history, ok := app.conversation.(shellHistory)
	if !ok {
		app.model.boxes = append(app.model.boxes, errorMessageBox{Text: "Shell history is not supported by this conversation"})
		app.requestRender()
		return
	}
	if len(command) > session.MaxShellCommandBytes || strings.IndexByte(command, 0) >= 0 {
		app.model.boxes = append(app.model.boxes, errorMessageBox{Text: "Shell command exceeds limits or contains a NUL byte"})
		app.requestRender()
		return
	}
	id := rand.Text()
	cwd := app.conversation.CWD()
	app.model.startShell(command)
	runCtx, cancel := app.operationContext(ctx)
	app.requestRender()
	app.workers.Go(func() {
		defer cancel()
		start := session.Event{Type: session.EventShellCommand, ShellCommand: &session.ShellCommandEntry{ID: id, Command: command, WorkingDir: cwd}}
		var result shell.Result
		err := history.RecordShellEvent(start)
		if err == nil {
			// Bound streaming independently of the process buffers, and serialize the
			// stdout/stderr callbacks. Excess bytes are drained without UI events.
			var mu sync.Mutex
			remaining := map[tool.ShellStream]int{tool.ShellStreamStdout: session.MaxShellOutputBytes, tool.ShellStreamStderr: session.MaxShellOutputBytes}
			result, err = shell.Run(runCtx, command, cwd, session.MaxShellOutputBytes, func(artifact tool.Artifact) error {
				chunk, ok := artifact.(tool.ShellStreamArtifact)
				if !ok {
					return nil
				}
				mu.Lock()
				defer mu.Unlock()
				n := min(len(chunk.Content), remaining[chunk.Stream])
				if n == 0 {
					return nil
				}
				remaining[chunk.Stream] -= n
				select {
				case app.events <- shellOutputReceived{Stream: chunk.Stream, Text: chunk.Content[:n]}:
				case <-runCtx.Done():
				}
				return nil
			})
			for _, output := range []session.ShellOutputEntry{
				{ID: id, Stream: session.ShellStdout, Text: strings.ToValidUTF8(result.Stdout, "?"), Truncated: result.StdoutTruncated},
				{ID: id, Stream: session.ShellStderr, Text: strings.ToValidUTF8(result.Stderr, "?"), Truncated: result.StderrTruncated},
			} {
				err = errors.Join(err, history.RecordShellEvent(session.Event{Type: session.EventShellOutput, ShellOutput: &output}))
			}
			status := session.ShellStatusEntry{ID: id, Status: session.ShellSucceeded, ExitCode: result.ExitCode, HasExitCode: result.Command != "", DurationMS: result.DurationMS, CleanupFailed: result.CleanupTimedOut}
			switch {
			case errors.Is(err, context.Canceled):
				status.Status = session.ShellCanceled
			case result.Command == "":
				status.Status = session.ShellStartFailed
			case err != nil || !result.Success:
				status.Status = session.ShellFailed
			}
			if result.TimedOut {
				status.Error = "command timed out"
			}
			if err != nil {
				status.Error = truncateWorkflowText(err.Error(), session.MaxShellErrorBytes)
			}
			err = errors.Join(err, history.RecordShellEvent(session.Event{Type: session.EventShellStatus, ShellStatus: &status}))
		}
		select {
		case app.events <- shellCommandDone{Command: command, Result: result, Err: err}:
		case <-ctx.Done():
		}
	})
}

func (m *appModel) shellOutput(event shellOutputReceived) {
	if !m.shellActive() {
		return
	}
	box := m.boxes[m.shellOutputIndex].(shellOutputBox)
	switch event.Stream {
	case tool.ShellStreamStdout:
		box.Stdout += shellText(event.Text)
	case tool.ShellStreamStderr:
		box.Stderr += shellText(event.Text)
	}
	m.boxes[m.shellOutputIndex] = box
	m.boxLineCache.Reset()
}

func (m *appModel) appendShellHistory(event session.Event) {
	switch event.Type {
	case session.EventShellCommand:
		m.boxes = append(m.boxes, systemMessageBox{Text: "$ " + shellText(event.ShellCommand.Command)})
	case session.EventShellOutput:
		output := event.ShellOutput
		if output.Text == "" && !output.Truncated {
			return
		}
		var outputBox shellOutputBox
		if len(m.boxes) > 0 {
			outputBox, _ = m.boxes[len(m.boxes)-1].(shellOutputBox)
		}
		if outputBox.ID != output.ID {
			outputBox = shellOutputBox{ID: output.ID}
			m.boxes = append(m.boxes, outputBox)
		}
		switch output.Stream {
		case session.ShellStdout:
			outputBox.Stdout += shellText(output.Text)
			outputBox.StdoutTruncated = outputBox.StdoutTruncated || output.Truncated
		case session.ShellStderr:
			outputBox.Stderr += shellText(output.Text)
			outputBox.StderrTruncated = outputBox.StderrTruncated || output.Truncated
		}
		m.boxes[len(m.boxes)-1] = outputBox
	case session.EventShellStatus:
		// Completion status remains in history but is not part of shell output.
	}
}
