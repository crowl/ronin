package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
)

func TestRegistry(t *testing.T) {
	t.Run("load returns fresh instances", func(t *testing.T) {
		model := llm.Model{Provider: "test", Name: "fresh"}
		calls := 0
		if err := llm.RegisterModel(model, func(level llm.ReasoningLevel) (llm.ModelClient, error) {
			calls++
			return &fakeModelClient{model: model, reasoningLevel: level}, nil
		}); err != nil {
			t.Fatalf("register: %v", err)
		}

		first, err := llm.LoadModelClient(model, llm.ReasoningLevelLow)
		if err != nil {
			t.Fatalf("first load: %v", err)
		}
		second, err := llm.LoadModelClient(model, llm.ReasoningLevelHigh)
		if err != nil {
			t.Fatalf("second load: %v", err)
		}

		if first == second {
			t.Fatalf("expected distinct llm instances")
		}
		if calls != 2 {
			t.Fatalf("factory calls = %d, want 2", calls)
		}
		if first.ReasoningLevel() != llm.ReasoningLevelLow {
			t.Fatalf("first reasoning level = %q, want %q", first.ReasoningLevel(), llm.ReasoningLevelLow)
		}
		if second.ReasoningLevel() != llm.ReasoningLevelHigh {
			t.Fatalf("second reasoning level = %q, want %q", second.ReasoningLevel(), llm.ReasoningLevelHigh)
		}
	})

	t.Run("duplicate registration rejected", func(t *testing.T) {
		model := llm.Model{Provider: "test", Name: "duplicate"}
		factory := func(llm.ReasoningLevel) (llm.ModelClient, error) {
			return &fakeModelClient{model: model}, nil
		}
		if err := llm.RegisterModel(model, factory); err != nil {
			t.Fatalf("register: %v", err)
		}
		err := llm.RegisterModel(model, factory)
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("duplicate register error = %v, want duplicate error", err)
		}
	})

	t.Run("nil factory rejected", func(t *testing.T) {
		err := llm.RegisterModel(llm.Model{Provider: "test", Name: "nil-factory"}, nil)
		if err == nil || !strings.Contains(err.Error(), "nil") {
			t.Fatalf("nil factory error = %v, want nil error", err)
		}
	})

	t.Run("factory errors propagate", func(t *testing.T) {
		model := llm.Model{Provider: "test", Name: "factory-error"}
		expected := errors.New("boom")
		if err := llm.RegisterModel(model, func(llm.ReasoningLevel) (llm.ModelClient, error) {
			return nil, expected
		}); err != nil {
			t.Fatalf("register: %v", err)
		}
		_, err := llm.LoadModelClient(model, llm.ReasoningLevelMedium)
		if !errors.Is(err, expected) {
			t.Fatalf("load error = %v, want wrapped expected error", err)
		}
	})

	t.Run("models sorted", func(t *testing.T) {
		models := []llm.Model{
			{Provider: "sort-b", Name: "2"},
			{Provider: "sort-a", Name: "2"},
			{Provider: "sort-a", Name: "1"},
		}
		for _, model := range models {
			registeredModel := model
			if err := llm.RegisterModel(registeredModel, func(level llm.ReasoningLevel) (llm.ModelClient, error) {
				return &fakeModelClient{model: registeredModel, reasoningLevel: level}, nil
			}); err != nil {
				t.Fatalf("register %s: %v", model, err)
			}
		}

		allModels := llm.Models()
		var got []llm.Model
		for _, model := range allModels {
			if strings.HasPrefix(model.Provider, "sort-") {
				got = append(got, model)
			}
		}
		want := []llm.Model{
			{Provider: "sort-a", Name: "1"},
			{Provider: "sort-a", Name: "2"},
			{Provider: "sort-b", Name: "2"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sorted models = %#v, want %#v", got, want)
		}
	})
}

type fakeModelClient struct {
	model          llm.Model
	reasoningLevel llm.ReasoningLevel
}

func (l *fakeModelClient) Model() llm.Model {
	return l.model
}

func (l *fakeModelClient) ReasoningLevel() llm.ReasoningLevel {
	return l.reasoningLevel
}

func (l *fakeModelClient) SetReasoningLevel(level llm.ReasoningLevel) error {
	l.reasoningLevel = level
	return nil
}

func (l *fakeModelClient) PredictNext(context.Context, llm.PredictNextRequest) (<-chan llm.PredictionEvent, <-chan error) {
	panic("not implemented")
}

func (l *fakeModelClient) PredictNextStructured(context.Context, llm.PredictNextStructuredRequest) (json.RawMessage, error) {
	panic("not implemented")
}
