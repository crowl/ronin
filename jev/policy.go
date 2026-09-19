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
	HasRelevant  bool
	Irreversible float64
}

func judgmentFrom(answers map[string]Answer) Judgment {
	var j Judgment
	if a, ok := answers["relevant"]; ok && a.Type == "noul" {
		j.Relevant = a.Noul
		j.HasRelevant = true
	}
	if a, ok := answers["irreversible"]; ok && a.Type == "noul" {
		j.Irreversible = a.Noul
	}
	return j
}

// Decide maps Jev answers onto an action. Thresholds live here so they can be
// tested without the TypeSafe API.
//
// Relevance is the primary signal: deny when P(relevant) is at most
// 1-minConfidence, i.e. Jev is at least minConfidence sure the call is off
// task. An empty judgment (no answers) allows. Irreversible side effects on
// a mutating tool still deny when that noul meets minConfidence.
func Decide(j Judgment, minConfidence float64) Action {
	if minConfidence < 0 {
		minConfidence = 0
	}
	if minConfidence > 1 {
		minConfidence = 1
	}
	if j.HasRelevant && j.Relevant <= 1-minConfidence {
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
