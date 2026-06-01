package llm

type PredictionStarted struct{}

type ThinkingDelta struct{ Text string }

type TextDelta struct{ Text string }

type ToolCallRequested struct{ ToolCall ToolCallBlock }

type PredictionFinished struct {
	Usage      Usage
	StopReason StopReason
}

type Event interface{ event() }

func (PredictionStarted) event()  {}
func (ThinkingDelta) event()      {}
func (TextDelta) event()          {}
func (ToolCallRequested) event()  {}
func (PredictionFinished) event() {}
