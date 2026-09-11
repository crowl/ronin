package session

import "github.com/crowl/ronin/llm"

// Accounting accumulates billed journal events, never copied context snapshots.
// Missing usage or pricing makes the total incomplete without discarding known costs.
type Accounting struct {
	Tokens llm.Usage
	Cost   llm.SessionCost
}

func NewAccounting() Accounting { return Accounting{Cost: llm.SessionCost{Available: true}} }

func (a *Accounting) Apply(event Event) {
	var usage *llm.Usage
	switch event.Type {
	case EventMessage:
		assistant, ok := event.Message.(llm.AssistantMessage)
		if !ok {
			return
		}
		usage = &assistant.Usage
	case EventUsage:
		if event.Usage == nil {
			return
		}
		usage = event.Usage.Usage
	default:
		return
	}
	if usage == nil {
		a.Cost.Available = false
		return
	}
	a.Tokens.AddTokens(*usage)
	if usage.Cost.Available {
		a.Cost.Total += usage.Cost.Total
	} else {
		a.Cost.Available = false
	}
}

func Account(events []Event) Accounting {
	a := NewAccounting()
	for _, event := range events {
		a.Apply(event)
	}
	return a
}
