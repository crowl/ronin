package session

import (
	"time"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
)

const Version = 1

type Metadata struct {
	Title          string
	Model          config.Model
	ReasoningLevel string
}

type Workspace struct {
	Version         int    `json:"version"`
	WorkingDir      string `json:"working_dir"`
	ActiveSessionID string `json:"active_session_id"`
	Sessions        []Ref  `json:"sessions"`
}

type Ref struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	ParentID  string    `json:"parent_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Session struct {
	Version        int
	ID             string
	Title          string
	WorkingDir     string
	ParentID       string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Model          config.Model
	ReasoningLevel string
	Messages       []llm.Message
}
