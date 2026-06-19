package acp

import (
	"fmt"

	"github.com/crowl/ronin/llm"
)

const (
	configIDModel     = "model"
	configIDReasoning = "reasoning"

	configCategoryModel     = "model"
	configCategoryReasoning = "thought_level"
	configTypeSelect        = "select"
)

func configOptions(conv Conversation) []configOption {
	return []configOption{
		modelConfigOption(conv.Model()),
		reasoningConfigOption(conv.ReasoningLevel()),
	}
}

func modelConfigOption(current llm.Model) configOption {
	models := llm.Models()
	options := make([]configOptionValue, 0, len(models))
	for _, model := range models {
		value := model.String()
		options = append(options, configOptionValue{
			Value:       value,
			Name:        value,
			Description: fmt.Sprintf("Use %s", value),
		})
	}
	return configOption{
		ID:           configIDModel,
		Name:         "Model",
		Description:  "Model used for future turns",
		Category:     configCategoryModel,
		Type:         configTypeSelect,
		CurrentValue: current.String(),
		Options:      options,
	}
}

func reasoningConfigOption(current llm.ReasoningLevel) configOption {
	levels := llm.ReasoningLevels()
	options := make([]configOptionValue, 0, len(levels))
	for _, level := range levels {
		value := string(level)
		options = append(options, configOptionValue{
			Value:       value,
			Name:        value,
			Description: fmt.Sprintf("Use %s reasoning", value),
		})
	}
	return configOption{
		ID:           configIDReasoning,
		Name:         "Reasoning",
		Description:  "Reasoning level used for future turns",
		Category:     configCategoryReasoning,
		Type:         configTypeSelect,
		CurrentValue: string(current),
		Options:      options,
	}
}

func modelByValue(value string) (llm.Model, bool) {
	for _, model := range llm.Models() {
		if value == model.String() {
			return model, true
		}
	}
	return llm.Model{}, false
}

func reasoningLevelByValue(value string) (llm.ReasoningLevel, bool) {
	level := llm.ReasoningLevel(value)
	if !llm.IsValidReasoningLevel(level) {
		return "", false
	}
	return level, true
}
