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
	Disposition   string
	Confidence    float64
	Irreversible  float64
	Impact        float64
	Probabilities map[string]float64
}

func judgmentFrom(answers map[string]Answer) Judgment {
	j := Judgment{Disposition: "allow"}
	if a, ok := answers["disposition"]; ok && a.Type == "choice" && a.Choice != "" {
		j.Disposition = a.Choice
		j.Confidence = a.Confidence
		j.Probabilities = a.Probabilities
	}
	if a, ok := answers["irreversible"]; ok && a.Type == "noul" {
		j.Irreversible = a.Noul
	}
	if a, ok := answers["impact"]; ok && a.Type == "score" {
		j.Impact = a.Score
	}
	return j
}

// Decide maps Jev answers onto an action. Thresholds live here so they can be
// tested without the TypeSafe API. A deny choice is honored only when
// confidence meets minConfidence. High irreversible probability with a
// confident disposition other than allow is also a deny.
func Decide(j Judgment, minConfidence float64) Action {
	if j.Confidence < minConfidence {
		return ActionAllow
	}
	if j.Disposition == "deny" {
		return ActionDeny
	}
	if j.Irreversible >= 0.8 && j.Disposition != "allow" {
		return ActionDeny
	}
	return ActionAllow
}

func denyReason(j Judgment) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("disposition=%s", j.Disposition))
	if j.Confidence > 0 {
		parts = append(parts, fmt.Sprintf("confidence=%.2f", j.Confidence))
	}
	parts = append(parts, fmt.Sprintf("irreversible=%.2f", j.Irreversible))
	parts = append(parts, fmt.Sprintf("impact=%.2f", j.Impact))
	return "Jev policy denied this tool call (" + strings.Join(parts, " ") + ")"
}
