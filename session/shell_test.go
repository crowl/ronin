package session

import (
	"reflect"
	"strings"
	"testing"
)

func TestShellEventCodec(t *testing.T) {
	for _, event := range []Event{
		{Type: EventShellCommand, ShellCommand: &ShellCommandEntry{ID: "1", Command: "echo hi", WorkingDir: "/tmp"}},
		{Type: EventShellOutput, ShellOutput: &ShellOutputEntry{ID: "1", Stream: ShellStderr, Text: "problem", Truncated: true}},
		{Type: EventShellStatus, ShellStatus: &ShellStatusEntry{ID: "1", Status: ShellFailed, ExitCode: 2, HasExitCode: true}},
	} {
		tag, payload, err := EncodeEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeEvent(tag, payload)
		if err != nil || !reflect.DeepEqual(event, decoded) {
			t.Fatalf("round trip: %+v, %v", decoded, err)
		}
		if len(Reconstruct([]Event{decoded})) != 0 {
			t.Fatal("local history became context")
		}
	}
	for _, tag := range []EventType{EventShellCommand, EventShellOutput, EventShellStatus} {
		if _, err := DecodeEvent(string(tag), []byte("null")); err == nil {
			t.Errorf("accepted invalid %s", tag)
		}
	}
	if _, _, err := EncodeEvent(Event{Type: EventShellOutput, ShellOutput: &ShellOutputEntry{ID: "1", Stream: ShellStdout, Text: strings.Repeat("x", MaxShellOutputBytes+1)}}); err == nil {
		t.Fatal("accepted oversized output")
	}
}
