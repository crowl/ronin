package streamretry

import (
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"

	"github.com/crowl/ronin/llm"
)

func TestPredictStructuredCancellationDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		attempts := 0
		done := make(chan error, 1)
		go func() {
			_, err := PredictStructured(ctx, func(context.Context) (*llm.StructuredResult, error) {
				attempts++
				return nil, io.ErrUnexpectedEOF
			})
			done <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want canceled", err)
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1", attempts)
		}
	})
}
