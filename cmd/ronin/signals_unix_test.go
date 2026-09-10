//go:build unix

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestShutdownSignals(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			marker := filepath.Join(t.TempDir(), "cleanup")
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestShutdownSignalHelper$")
			cmd.Env = append(os.Environ(), "RONIN_SIGNAL_TEST_MARKER="+marker)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "ready" {
				_ = cmd.Wait()
				t.Fatalf("child did not become ready: %q (%v)", scanner.Text(), scanner.Err())
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("child did not exit gracefully: %v", err)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "cleaned up" {
				t.Fatalf("cleanup marker = %q, error = %v", data, err)
			}
		})
	}
}

func TestShutdownSignalHelper(t *testing.T) {
	marker := os.Getenv("RONIN_SIGNAL_TEST_MARKER")
	if marker == "" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	defer func() {
		if err := os.WriteFile(marker, []byte("cleaned up"), 0o600); err != nil {
			t.Error(err)
		}
	}()
	fmt.Println("ready")
	<-ctx.Done()
	if ctx.Err() != context.Canceled {
		t.Fatalf("context error = %v, want cancellation", ctx.Err())
	}
}
