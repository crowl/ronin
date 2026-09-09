package streamretry

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/crowl/ronin/llm"
)

func TestPredictRetriesUnexpectedEOFBeforeSubstantiveOutput(t *testing.T) {
	attempts := 0
	events, errs := Predict(t.Context(), 8, func(ctx context.Context, attemptEvents chan<- llm.PredictionEvent) error {
		attempts++
		if err := send(ctx, attemptEvents, llm.PredictionStarted{}); err != nil {
			return err
		}
		if err := send(ctx, attemptEvents, llm.BlockStarted{Index: 0, Kind: llm.BlockKindText}); err != nil {
			return err
		}
		if attempts == 1 {
			return io.ErrUnexpectedEOF
		}
		if err := send(ctx, attemptEvents, llm.TextDelta{Index: 0, Text: "done"}); err != nil {
			return err
		}
		return send(ctx, attemptEvents, llm.PredictionFinished{})
	})

	got := drain(events)
	if err := <-errs; err != nil {
		t.Fatalf("Predict() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	want := []llm.PredictionEvent{
		llm.PredictionStarted{},
		llm.BlockStarted{Index: 0, Kind: llm.BlockKindText},
		llm.TextDelta{Index: 0, Text: "done"},
		llm.PredictionFinished{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestPredictDoesNotRetryUnexpectedEOFAfterSubstantiveOutput(t *testing.T) {
	attempts := 0
	events, errs := Predict(t.Context(), 8, func(ctx context.Context, attemptEvents chan<- llm.PredictionEvent) error {
		attempts++
		if err := send(ctx, attemptEvents, llm.TextDelta{Text: "partial"}); err != nil {
			return err
		}
		return io.ErrUnexpectedEOF
	})

	_ = drain(events)
	if err := <-errs; !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Predict() error = %v, want unexpected EOF", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func send(ctx context.Context, events chan<- llm.PredictionEvent, event llm.PredictionEvent) error {
	select {
	case events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func drain(events <-chan llm.PredictionEvent) []llm.PredictionEvent {
	var result []llm.PredictionEvent
	for event := range events {
		result = append(result, event)
	}
	return result
}
