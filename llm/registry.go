package llm

import (
	"fmt"
	"maps"
	"slices"
	"sync"
)

type Factory func(ReasoningLevel) (Assistant, error)

var (
	registryMu sync.RWMutex
	registry   = make(map[Model]Factory)
)

func Models() []Model {
	registryMu.RLock()
	models := slices.Collect(maps.Keys(registry))
	registryMu.RUnlock()

	slices.SortFunc(models, func(a, b Model) int {
		if a.Provider != b.Provider {
			return compareStrings(a.Provider, b.Provider)
		}
		return compareStrings(a.Name, b.Name)
	})

	return models
}

func RegisterModel(model Model, factory Factory) error {
	if factory == nil {
		return fmt.Errorf("invalid(nil) llm factory for model: %s", model)
	}

	registryMu.Lock()
	defer registryMu.Unlock()
	if _, ok := registry[model]; ok {
		return fmt.Errorf("duplicate model registration: %s", model.String())
	}
	registry[model] = factory
	return nil
}

func LoadAssistant(model Model, level ReasoningLevel) (Assistant, error) {
	registryMu.RLock()
	factory, ok := registry[model]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown model: %s", model)
	}
	loaded, err := factory(level)
	if err != nil {
		return nil, fmt.Errorf("failed to load model: %s with reasoning level: %s: %w", model, level, err)
	}
	if loaded == nil {
		return nil, fmt.Errorf("invalid(nil) llm for model: %s", model)
	}
	return loaded, nil
}

func compareStrings(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
