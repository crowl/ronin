package runtime

import (
	"encoding/json"
	"math"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
)

func compactionMessageBytes(messages []session.Message) int {
	total := 0
	for _, message := range messages {
		_, content := session.HistoryMessage(message)
		total += len(content)
	}
	return total
}

// requestBytes includes otherwise easily missed system and tool-schema costs.
func requestBytes(system string, tools []llm.Tool, messages []llm.Message) int {
	total := len(system)
	for _, message := range messages {
		data, _ := json.Marshal(message)
		total += len(data)
		// Go errors usually marshal as {}, but providers send their text.
		if failure, ok := message.(llm.ToolErrorMessage); ok && failure.Error != nil {
			text, _ := json.Marshal(failure.Error.Error())
			total += len(text)
		}
	}
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

func (c *Conversation) estimatedContextTokens(messages []llm.Message) uint64 {
	ratio := 1.0 / 3 // Conservative initial estimate, refined upward from actual usage.
	if c.calibration.model == c.Model().String() {
		ratio = max(ratio, c.calibration.tokensPerByte)
	}
	size := requestBytes(c.systemPrompt, c.toolDefs, messages)
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
