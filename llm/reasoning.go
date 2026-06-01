package llm

type ReasoningLevel string

const (
	ReasoningLevelOff       ReasoningLevel = "off"
	ReasoningLevelLow       ReasoningLevel = "low"
	ReasoningLevelMedium    ReasoningLevel = "medium"
	ReasoningLevelHigh      ReasoningLevel = "high"
	ReasoningLevelExtraHigh ReasoningLevel = "xhigh"
)

func IsValidReasoningLevel(level ReasoningLevel) bool {
	switch level {
	case ReasoningLevelOff,
		ReasoningLevelLow,
		ReasoningLevelMedium,
		ReasoningLevelHigh,
		ReasoningLevelExtraHigh:
		return true
	default:
		return false
	}
}

func ReasoningLevels() []ReasoningLevel {
	return []ReasoningLevel{
		ReasoningLevelOff,
		ReasoningLevelLow,
		ReasoningLevelMedium,
		ReasoningLevelHigh,
		ReasoningLevelExtraHigh,
	}
}
