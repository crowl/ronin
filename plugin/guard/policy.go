package guard

import (
	"fmt"

	"github.com/crowl/ronin/plugin"
)

// verdict denies when the call is confidently off task. Shell calls are also
// denied when they confidently inspect or ad-hoc edit files outside the
// harness file tools. A shell response must include the bypass answer.
func verdict(answers map[string]plugin.Answer, shell bool) error {
	if answers == nil {
		return fmt.Errorf("guard evaluation returned an invalid response: answers are missing")
	}
	relevant, err := noulAnswer(answers, "relevant")
	if err != nil {
		return fmt.Errorf("guard evaluation returned an invalid response: %w", err)
	}
	if relevant <= 1-minConfidence {
		return fmt.Errorf("guard denied this tool call (relevant=%.2f)", relevant)
	}
	if !shell {
		return nil
	}
	bypass, err := noulAnswer(answers, "file_bypass")
	if err != nil {
		return fmt.Errorf("guard evaluation returned an invalid response: %w", err)
	}
	if bypass >= minConfidence {
		return fmt.Errorf("guard denied this shell command because it reads or writes files outside the harness file tools (file_bypass=%.2f)", bypass)
	}
	return nil
}

func noulAnswer(answers map[string]plugin.Answer, name string) (float64, error) {
	answer, ok := answers[name]
	if !ok {
		return 0, fmt.Errorf("%s answer is missing", name)
	}
	if answer.Type != "noul" {
		return 0, fmt.Errorf("%s answer has type %q, want noul", name, answer.Type)
	}
	if answer.Noul < 0 || answer.Noul > 1 {
		return 0, fmt.Errorf("%s probability %.4f is outside [0,1]", name, answer.Noul)
	}
	return answer.Noul, nil
}
