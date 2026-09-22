package guard

import (
	"fmt"
	"math"

	"github.com/crowl/ronin/plugin"
)

// verdict denies only when a valid relevance answer confidently marks the call
// off task. Missing or invalid answers are not explicit denials.
func verdict(answers map[string]plugin.Answer) error {
	answer, ok := answers["relevant"]
	if !ok || answer.Type != "noul" || math.IsNaN(answer.Noul) || answer.Noul < 0 || answer.Noul > 1 {
		return nil
	}
	if answer.Noul <= 1-minConfidence {
		return fmt.Errorf("guard denied this tool call (relevant=%.2f)", answer.Noul)
	}
	return nil
}
