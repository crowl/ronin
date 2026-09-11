package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
)

func TestContextBudgetIncludesInstructionsAndCalibration(t *testing.T) {
	c, err := NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}})
	if err != nil {
		t.Fatal(err)
	}
	baseline := c.estimatedContextTokens()
	c.systemPrompt = strings.Repeat("instructions ", 1000)
	if c.estimatedContextTokens() <= baseline {
		t.Fatal("system instructions omitted")
	}
	size := requestBytes(c.systemPrompt, c.toolDefs, c.messages)
	before := c.estimatedContextTokens()
	c.calibrateContext(size, llm.Usage{InputTokens: size})
	if c.estimatedContextTokens() <= before {
		t.Fatal("reported usage did not calibrate estimate")
	}
	c.calibrateContext(size, llm.Usage{InputTokens: 1})
	if c.estimatedContextTokens() <= before {
		t.Fatal("one low sample erased safety margin")
	}
}

func TestCompactionExtractsMiddleFailureAndPreservesVerbatimRequest(t *testing.T) {
	output, _ := json.Marshal(map[string]any{"exit_code": 1, "stdout": strings.Repeat("ok\n", 3000) + "FAIL: middle_test\n" + strings.Repeat("ok\n", 3000)})
	facts := compactionToolFacts("shell", string(output))
	if !strings.Contains(facts, "FAIL: middle_test") || !strings.Contains(facts, "exit_code=1") {
		t.Fatal(facts)
	}
	text := "Requirements:\n  keep indentation\n\nDo not remove this."
	line := compactionFactLine(0, llm.UserMessage{Text: text})
	if !strings.Contains(line, text) || !strings.Contains(line, "source=history:") {
		t.Fatal(line)
	}
}
