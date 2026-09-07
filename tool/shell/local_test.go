package shell

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/tool"
)

func TestLocalRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell commands")
	}
	cwd := t.TempDir()
	result, err := Run(t.Context(), "pwd; printf hello | tr a-z A-Z; printf error >&2; exit 4", cwd, 1024, nil)
	if err != nil || result.ExitCode != 4 || result.Success || !strings.Contains(result.Stdout, cwd) || !strings.Contains(result.Stdout, "HELLO") || result.Stderr != "error" {
		t.Fatalf("Run = %+v, %v", result, err)
	}
	result, err = Run(t.Context(), "head -c 10000 /dev/zero", cwd, 100, nil)
	if err != nil || len(result.Stdout) != 100 || !result.StdoutTruncated {
		t.Fatalf("bounded output = %+v, %v", result, err)
	}
	if _, err := Run(t.Context(), "echo nope", cwd+"/missing", 100, nil); err == nil {
		t.Fatal("missing cwd succeeded")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err = Run(ctx, "printf ready; sleep 30", cwd, 100, func(tool.Artifact) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || result.Stdout != "ready" {
		t.Fatalf("cancel = %+v, %v", result, err)
	}
}
