package llm

type BlockKind string

const (
	BlockKindText     BlockKind = "text"
	BlockKindThinking BlockKind = "thinking"
	BlockKindToolCall BlockKind = "tool_call"
)

type PredictionStarted struct{}

type BlockStarted struct {
	Index int
	Kind  BlockKind
}

type ThinkingDelta struct {
	Index int
	Text  string
}

type TextDelta struct {
	Index int
	Text  string
}

type ToolCallArgumentsDelta struct {
	Index     int
	Arguments string
}

type BlockEnded struct {
	Index int
	Block AssistantBlock
}

type PredictionFinished struct {
	Usage      Usage
	StopReason StopReason
}

type PredictionEvent interface{ event() }

func (PredictionStarted) event()      {}
func (BlockStarted) event()           {}
func (ThinkingDelta) event()          {}
func (TextDelta) event()              {}
func (ToolCallArgumentsDelta) event() {}
func (BlockEnded) event()             {}
func (PredictionFinished) event()     {}
