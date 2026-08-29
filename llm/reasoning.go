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

func NearestReasoningLevel(requested ReasoningLevel, supported ReasoningSet) (ReasoningLevel, bool) {
	requestedRank, ok := reasoningLevelRank(requested)
	if !ok {
		return "", false
	}
	levels := supported.Levels()
	if len(levels) == 0 {
		return "", false
	}
	best := levels[0]
	bestRank, _ := reasoningLevelRank(best)
	bestDistance := abs(requestedRank - bestRank)
	for _, level := range levels[1:] {
		rank, _ := reasoningLevelRank(level)
		distance := abs(requestedRank - rank)
		if distance < bestDistance || distance == bestDistance && rank > bestRank {
			best = level
			bestRank = rank
			bestDistance = distance
		}
	}
	return best, true
}

func reasoningLevelRank(level ReasoningLevel) (int, bool) {
	for rank, candidate := range ReasoningLevels() {
		if candidate == level {
			return rank, true
		}
	}
	return 0, false
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
