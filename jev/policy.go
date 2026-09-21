package jev

import (
	"fmt"
	"strings"
)

// Action is the harness-owned verdict derived from Jev answers.
type Action int

const (
	ActionAllow Action = iota
	ActionDeny
)

func (a Action) String() string {
	if a == ActionDeny {
		return "deny"
	}
	return "allow"
}

// Judgment is the subset of Jev answers the tool gate consumes.
type Judgment struct {
	Relevant     float64
	Irreversible float64
}

func judgmentFrom(answers map[string]Answer) (Judgment, error) {
	if answers == nil {
		return Judgment{}, fmt.Errorf("answers are missing")
	}
	relevant, err := noulAnswer(answers, "relevant")
	if err != nil {
		return Judgment{}, err
	}
	irreversible, err := noulAnswer(answers, "irreversible")
	if err != nil {
		return Judgment{}, err
	}
	return Judgment{Relevant: relevant, Irreversible: irreversible}, nil
}

func noulAnswer(answers map[string]Answer, name string) (float64, error) {
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

// Decide maps Jev answers onto an action. Thresholds live here so they can be
// tested without the TypeSafe API.
//
// Relevance is the primary signal: deny when P(relevant) is at most
// 1-minConfidence, i.e. Jev is at least minConfidence sure the call is off
// task. Irreversible side effects deny when that noul meets minConfidence.
func Decide(j Judgment, minConfidence float64) Action {
	if minConfidence < 0 {
		minConfidence = 0
	}
	if minConfidence > 1 {
		minConfidence = 1
	}
	if j.Relevant <= 1-minConfidence {
		return ActionDeny
	}
	if j.Irreversible >= minConfidence && j.Irreversible >= 0.8 {
		return ActionDeny
	}
	return ActionAllow
}

func denyReason(j Judgment) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("relevant=%.2f", j.Relevant))
	if j.Irreversible > 0 {
		parts = append(parts, fmt.Sprintf("irreversible=%.2f", j.Irreversible))
	}
	return "Jev policy denied this tool call (" + strings.Join(parts, " ") + ")"
}
