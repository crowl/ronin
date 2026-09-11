package runtime

import (
	"encoding/json"
	"math"

	"github.com/crowl/ronin/llm"
)

func compactionMessageBytes(messages []llm.Message) int {
	total := 0
	for _, message := range messages {
		_, content := historyMessage(message)
		total += len(content)
	}
	return total
}

// requestBytes includes otherwise easily missed system and tool-schema costs.
func requestBytes(system string, tools []llm.Tool, messages []llm.Message) int {
	total := len(system) + compactionMessageBytes(messages)
	for _, t := range tools {
		schema, _ := json.Marshal(t.Parameters())
		total += len(t.Name()) + len(t.Description()) + len(schema)
	}
	return total
}

type contextCalibration struct {
	model         string
	tokensPerByte float64
}

func (c *Conversation) estimatedContextTokens() uint64 {
	ratio := 1.0 / 3 // Conservative initial estimate, refined upward from actual usage.
	if c.calibration.model == c.Model().String() {
		ratio = max(ratio, c.calibration.tokensPerByte)
	}
	size := requestBytes(c.systemPrompt, c.toolDefs, llm.ProjectMessagesForProvider(c.messages, c.Model().Provider))
	// Leave room for output even when provider defaults are not explicit.
	reserve := min(8192, int(c.Model().ContextWindow)/8)
	return uint64(math.Ceil(float64(size)*ratio*1.15)) + uint64(reserve)
}
func (c *Conversation) calibrateContext(size int, usage llm.Usage) {
	if size <= 0 || usage.InputTokens == 0 {
		return
	}
	model := c.Model().String()
	if c.calibration.model != model {
		c.calibration = contextCalibration{model: model}
	}
	c.calibration.tokensPerByte = max(c.calibration.tokensPerByte, float64(usage.InputTokens)/float64(size))
}
