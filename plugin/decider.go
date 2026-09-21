package plugin

import "context"

// Question is one typed decision question. Criteria is a map for noul and
// choice questions, and a string slice for score questions.
type Question struct {
	Type         string
	Instructions string
	Criteria     any
}

// Answer is one typed decision answer. Unused fields stay zero for other types.
type Answer struct {
	Type          string
	Noul          float64
	Choice        string
	Score         float64
	Confidence    float64
	Probabilities map[string]float64
}

// Decider answers a set of questions about state. Implementations may call a
// remote model. Callers own which questions they ask and how answers become a
// verdict.
type Decider interface {
	Decide(ctx context.Context, state any, questions map[string]Question) (map[string]Answer, error)
}
