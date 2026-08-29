package llm

import (
	"fmt"
	"maps"
	"slices"
	"sync"
)

type Factory func(ReasoningLevel) (ModelClient, error)

var (
	registryMu sync.RWMutex
	registry   = make(map[string]registeredModel)
)

type registeredModel struct {
	model   Model
	factory Factory
}

func modelKey(model Model) string {
	return model.Provider + "\x00" + model.Name
}

func Models() []Model {
	registryMu.RLock()
	entries := slices.Collect(maps.Values(registry))
	registryMu.RUnlock()

	models := make([]Model, 0, len(entries))
	for _, entry := range entries {
		models = append(models, entry.model)
	}

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
	key := modelKey(model)
	if _, ok := registry[key]; ok {
		return fmt.Errorf("duplicate model registration: %s", model.String())
	}
	registry[key] = registeredModel{model: model, factory: factory}
	return nil
}

func LoadModelClient(model Model, level ReasoningLevel) (ModelClient, error) {
	registryMu.RLock()
	entry, ok := registry[modelKey(model)]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown model: %s", model)
	}
	if !entry.model.SupportsReasoning(level) {
		return nil, fmt.Errorf("reasoning level %q is not supported by model %s", level, entry.model)
	}
	loaded, err := entry.factory(level)
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
