package antigravity

import (
	"strings"

	"github.com/crowl/ronin/llm"
)

// Antigravity selects reasoning effort by routing to a different backend model
// rather than by a request field, so each public model maps to one runtime id
// per level. Models absent from the table are sent through unchanged, which is
// what lets a newly entitled model work before this table knows about it.
var routing = map[string]map[llm.ReasoningLevel]string{
	"gemini-3.1-pro": {
		llm.ReasoningLevelOff:       "gemini-3.1-pro-low",
		llm.ReasoningLevelLow:       "gemini-3.1-pro-low",
		llm.ReasoningLevelMedium:    "gemini-3.1-pro-low",
		llm.ReasoningLevelHigh:      "gemini-pro-agent",
		llm.ReasoningLevelExtraHigh: "gemini-pro-agent",
	},
	"gemini-3.8-flash":  flashRouting("gemini-3.8-flash"),
	"gemini-3.7-flash":  flashRouting("gemini-3.7-flash"),
	"gemini-3.6-flash":  flashRouting("gemini-3.6-flash"),
	"gemini-3.5-flash":  flashRouting("gemini-3.5-flash"),
	"claude-sonnet-4-6": uniformRouting("claude-sonnet-4-6"),
	"claude-opus-4-6":   uniformRouting("claude-opus-4-6-thinking"),
	"gpt-oss-120b":      uniformRouting("gpt-oss-120b-medium"),
}

func flashRouting(name string) map[llm.ReasoningLevel]string {
	return map[llm.ReasoningLevel]string{
		llm.ReasoningLevelOff:       name + "-low",
		llm.ReasoningLevelLow:       name + "-low",
		llm.ReasoningLevelMedium:    name + "-medium",
		llm.ReasoningLevelHigh:      name + "-high",
		llm.ReasoningLevelExtraHigh: name + "-high",
	}
}

func uniformRouting(name string) map[llm.ReasoningLevel]string {
	levels := make(map[llm.ReasoningLevel]string, len(llm.ReasoningLevels()))
	for _, level := range llm.ReasoningLevels() {
		levels[level] = name
	}
	return levels
}

// runtimeModel resolves the backend model id for a public model and level.
func runtimeModel(model string, level llm.ReasoningLevel) string {
	levels, known := routing[model]
	if !known {
		return model
	}
	if runtime, mapped := levels[level]; mapped {
		return runtime
	}
	return model
}

type thinkingConfig struct {
	IncludeThoughts bool `json:"includeThoughts"`
	ThinkingBudget  int  `json:"thinkingBudget"`
}

// thinkingFor derives the budget the runtime model accepts. The rules are
// per-family because the backend rejects budgets outside each family's range,
// and a rejected budget fails the whole request rather than degrading.
func thinkingFor(runtime string, level llm.ReasoningLevel) *thinkingConfig {
	switch {
	case strings.HasPrefix(runtime, "gemini-3.1-pro"), runtime == "gemini-pro-agent":
		if level == llm.ReasoningLevelOff {
			return &thinkingConfig{}
		}
		if level == llm.ReasoningLevelHigh || level == llm.ReasoningLevelExtraHigh {
			return &thinkingConfig{IncludeThoughts: true, ThinkingBudget: 10_001}
		}
		return &thinkingConfig{IncludeThoughts: true, ThinkingBudget: 1_001}

	case strings.Contains(runtime, "argon"), strings.HasPrefix(runtime, "gemini-4"):
		// This family rejects a zero budget outright, so reasoning is
		// disabled by omitting the config rather than by sending zero.
		if level == llm.ReasoningLevelOff {
			return nil
		}
		return &thinkingConfig{IncludeThoughts: true, ThinkingBudget: dynamicBudget(level)}

	case strings.HasPrefix(runtime, "gemini-"):
		if level == llm.ReasoningLevelOff {
			return &thinkingConfig{}
		}
		return &thinkingConfig{IncludeThoughts: true, ThinkingBudget: dynamicBudget(level)}
	}
	// Claude and GPT-OSS are served through the same envelope but carry their
	// reasoning in the runtime model id alone.
	return nil
}

func dynamicBudget(level llm.ReasoningLevel) int {
	switch level {
	case llm.ReasoningLevelHigh, llm.ReasoningLevelExtraHigh:
		return -1 // the backend sizes the budget itself
	case llm.ReasoningLevelMedium:
		return 4_000
	default:
		return 1_000
	}
}
