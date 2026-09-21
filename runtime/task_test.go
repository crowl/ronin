package runtime

import (
	"context"
	"testing"

	"github.com/crowl/ronin/plugin"
)

func TestWithUserTask(t *testing.T) {
	ctx := withUserTask(context.Background(), "fix the nil panic in parseFlags")
	if plugin.Task(ctx) != "fix the nil panic in parseFlags" {
		t.Fatalf("task = %q", plugin.Task(ctx))
	}
}
