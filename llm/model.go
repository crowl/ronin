package llm

import "fmt"

type ReasoningMode string

const (
	ReasoningModeNone   ReasoningMode = "none"
	ReasoningModeEffort ReasoningMode = "effort"
	ReasoningModeBudget ReasoningMode = "budget"
)

type ReasoningSet uint8

type ModelPricing struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64

	HasInput      bool
	HasOutput     bool
	HasCacheRead  bool
	HasCacheWrite bool
}

type Model struct {
	Provider           string
	Name               string
	ContextWindow      uint32
	ReasoningMode      ReasoningMode
	SupportedReasoning ReasoningSet
	Pricing            ModelPricing
}

func (m Model) String() string {
	return fmt.Sprintf("%s:%s", m.Provider, m.Name)
}

func (m Model) EffectiveReasoningMode() ReasoningMode {
	return m.ReasoningMode
}

func (m Model) SupportsReasoning(level ReasoningLevel) bool {
	if m.SupportedReasoning == 0 {
		if level == "" {
			return true
		}
		return IsValidReasoningLevel(level)
	}
	return m.SupportedReasoning.Supports(level)
}

func (m Model) ReasoningLevels() []ReasoningLevel {
	if m.SupportedReasoning == 0 {
		return ReasoningLevels()
	}
	return m.SupportedReasoning.Levels()
}

func (m Model) NearestReasoningLevel(requested ReasoningLevel) (ReasoningLevel, bool) {
	if m.SupportedReasoning == 0 {
		if IsValidReasoningLevel(requested) {
			return requested, true
		}
		return "", false
	}
	return NearestReasoningLevel(requested, m.SupportedReasoning)
}

func NewReasoningSet(levels ...ReasoningLevel) ReasoningSet {
	var set ReasoningSet
	for _, level := range levels {
		if rank, ok := reasoningLevelRank(level); ok {
			set |= 1 << rank
		}
	}
	return set
}

func (s ReasoningSet) Supports(level ReasoningLevel) bool {
	rank, ok := reasoningLevelRank(level)
	return ok && s&(1<<rank) != 0
}

func (s ReasoningSet) Levels() []ReasoningLevel {
	levels := ReasoningLevels()
	result := make([]ReasoningLevel, 0, len(levels))
	for _, level := range levels {
		if s.Supports(level) {
			result = append(result, level)
		}
	}
	return result
}
