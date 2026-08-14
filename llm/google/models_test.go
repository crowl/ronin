package google_test

import (
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/google"
)

func TestSetupRegistersGemini37Flash(t *testing.T) {
	if err := google.Setup("test-api-key"); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	want := llm.Model{
		Provider:      "google",
		Name:          "gemini-3.7-flash",
		ContextWindow: 1_048_576,
	}
	for _, model := range llm.Models() {
		if model != want {
			continue
		}

		client, err := llm.LoadModelClient(model, llm.ReasoningLevelOff)
		if err != nil {
			t.Fatalf("LoadModelClient() error = %v", err)
		}
		if got := client.Model(); got != want {
			t.Errorf("client model = %#v, want %#v", got, want)
		}
		return
	}

	t.Errorf("model %#v was not registered", want)
}
