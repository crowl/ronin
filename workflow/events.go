package workflow

import "github.com/crowl/ronin/tool"

// Status is the terminal outcome of a workflow run.
type Status string

const (
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Result is the compact outcome retained after a workflow run.
type Result struct {
	Name    string
	Input   string
	Status  Status
	Summary string
}

// Event reports live workflow execution progress.
type Event interface{ workflowEvent() }

type Started struct{ Name string }
type Log struct{ Text string }
type AgentStarted struct {
	Invocation int
	Request    AgentRequest
}
type AgentEventReceived struct {
	Invocation int
	Event      AgentEvent
}
type AgentFinished struct {
	Invocation int
	Text       string
	Error      string
}
type Finished struct{ Result Result }

func (Started) workflowEvent()            {}
func (Log) workflowEvent()                {}
func (AgentStarted) workflowEvent()       {}
func (AgentEventReceived) workflowEvent() {}
func (AgentFinished) workflowEvent()      {}
func (Finished) workflowEvent()           {}

// TextArtifacts extracts display artifacts from agent progress.
func TextArtifacts(event AgentEvent) []tool.Artifact {
	switch event := event.(type) {
	case AgentThinkingDelta:
		return []tool.Artifact{tool.TextArtifact{Text: event.Text}}
	case AgentTextDelta:
		return []tool.Artifact{tool.TextArtifact{Text: event.Text}}
	case AgentToolOutput:
		return []tool.Artifact{event.Artifact}
	case AgentToolFailed:
		return []tool.Artifact{tool.TextArtifact{Text: event.Error}}
	default:
		return nil
	}
}
