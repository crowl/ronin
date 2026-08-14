package openai_test

import (
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/openai"
)

func TestSetupRegistersGPT56Models(t *testing.T) {
	if err := openai.Setup("test-api-key"); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	registered := make(map[string]llm.Model)
	for _, model := range llm.Models() {
		registered[model.Name] = model
	}

	for _, name := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		model, ok := registered[name]
		if !ok {
			t.Errorf("model %q was not registered", name)
			continue
		}
		if model.Provider != "openai" {
			t.Errorf("model %q provider = %q, want openai", name, model.Provider)
		}
		if model.ContextWindow != 272_000 {
			t.Errorf("model %q context window = %d, want 272000", name, model.ContextWindow)
		}
	}
}
