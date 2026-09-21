package runtime

import (
	"context"

	"github.com/crowl/ronin/plugin"
)

// withUserTask records the prompt so tool gates can judge relevance.
func withUserTask(ctx context.Context, prompt string) context.Context {
	return plugin.WithTask(ctx, prompt)
}
