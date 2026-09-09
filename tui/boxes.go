package tui

import (
	"time"

	"github.com/crowl/ronin/tool"
)

type userMessageBox struct{ Text string }

type assistantMessageBox struct{ Text string }

type assistantThinkingBox struct{ Text string }

type toolCallBox struct {
	DisplayBytes     int
	DisplayTruncated bool
	Revision         uint64
	ToolCallID       string
	Title            string
	Artifacts        []tool.Artifact
	Error            string
	StartedAt        time.Time
	EndedAt          time.Time
}

type workflowStep struct {
	Invocation int
	Name       string
	Status     string
	Error      string
	StartedAt  time.Time
	EndedAt    time.Time
}

type workflowBox struct {
	Name           string
	Input          string
	Status         string
	Summary        string
	Active         []workflowStep
	Recent         []workflowStep
	Completed      int
	LatestActivity string
	StartedAt      time.Time
	EndedAt        time.Time
}

type systemMessageBox struct{ Text string }

type shellOutputBox struct {
	ID              string
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
	Notice          string
	Error           string
}

type errorMessageBox struct{ Text string }

// Box is a sealed interface to mark all application box types
type box interface{ box() }

func (b userMessageBox) box()       {}
func (b assistantMessageBox) box()  {}
func (b assistantThinkingBox) box() {}
func (b toolCallBox) box()          {}
func (b workflowBox) box()          {}
func (b systemMessageBox) box()     {}
func (b shellOutputBox) box()       {}
func (b errorMessageBox) box()      {}
