package streamretry

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"time"

	"github.com/crowl/ronin/llm"
)

const (
	maxAttempts = 3
	baseDelay   = 500 * time.Millisecond
	jitterMax   = 250 * time.Millisecond
)

// Predict runs a streaming prediction and retries an unexpected EOF only when
// the failed attempt emitted no substantive model output. PredictionStarted is
// forwarded at most once, and block lifecycle events from failed attempts are
// discarded until substantive output makes the attempt observable.
func Predict(ctx context.Context, eventBuffer int, attempt func(context.Context, chan<- llm.PredictionEvent) error) (<-chan llm.PredictionEvent, <-chan error) {
	events := make(chan llm.PredictionEvent, eventBuffer)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)

		started := false
		for attemptNumber := 1; attemptNumber <= maxAttempts; attemptNumber++ {
			substantive, err := runAttempt(ctx, events, &started, attempt)
			if err == nil {
				return
			}
			if !errors.Is(err, io.ErrUnexpectedEOF) || substantive || attemptNumber == maxAttempts {
				errs <- err
				return
			}
			if err := sleep(ctx, retryDelay(attemptNumber)); err != nil {
				errs <- err
				return
			}
		}
	}()
	return events, errs
}

// PredictStructured retries unexpected EOFs using the streaming retry policy.
// Unlike visible streaming output, buffered structured output can be discarded
// and retried even after partial text arrives. Each attempt must own fresh state
// and release its resources before returning. Only the final result is returned.
func PredictStructured(ctx context.Context, attempt func(context.Context) (*llm.StructuredResult, error)) (*llm.StructuredResult, error) {
	for attemptNumber := 1; ; attemptNumber++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := attempt(ctx)
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) || attemptNumber == maxAttempts {
			return result, err
		}
		if err := sleep(ctx, retryDelay(attemptNumber)); err != nil {
			return nil, err
		}
	}
}

func runAttempt(ctx context.Context, events chan<- llm.PredictionEvent, started *bool, attempt func(context.Context, chan<- llm.PredictionEvent) error) (bool, error) {
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	attemptEvents := make(chan llm.PredictionEvent, 16)
	attemptErr := make(chan error, 1)
	go func() {
		defer close(attemptEvents)
		attemptErr <- attempt(attemptCtx, attemptEvents)
	}()

	var pending []llm.PredictionEvent
	substantive := false
	for event := range attemptEvents {
		switch event.(type) {
		case llm.PredictionStarted:
			if *started {
				continue
			}
			if !forward(attemptCtx, events, event) {
				cancel()
				<-attemptErr
				return substantive, ctx.Err()
			}
			*started = true
		case llm.BlockStarted:
			if !substantive {
				pending = append(pending, event)
				continue
			}
			if !forward(attemptCtx, events, event) {
				cancel()
				<-attemptErr
				return substantive, ctx.Err()
			}
		default:
			if !substantive {
				substantive = true
				for _, pendingEvent := range pending {
					if !forward(attemptCtx, events, pendingEvent) {
						cancel()
						<-attemptErr
						return substantive, ctx.Err()
					}
				}
				pending = nil
			}
			if !forward(attemptCtx, events, event) {
				cancel()
				<-attemptErr
				return substantive, ctx.Err()
			}
		}
	}
	return substantive, <-attemptErr
}

func forward(ctx context.Context, events chan<- llm.PredictionEvent, event llm.PredictionEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func retryDelay(attempt int) time.Duration {
	delay := baseDelay * (1 << (attempt - 1))
	jitterLimit := min(delay/4, jitterMax)
	if jitterLimit <= 0 {
		return delay
	}
	return delay + time.Duration(rand.Int63n(int64(jitterLimit)+1))
}

func sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
