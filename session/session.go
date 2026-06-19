package session

import (
	"time"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
)

const Version = 1

type Store interface {
	LoadActive(workingDir string) (Session, bool, error)
	Load(id string) (Session, bool, error)
	List(workingDir string) ([]Ref, error)
	Create(workingDir string, metadata Metadata) (Session, error)
	Save(record Session) error
	Delete(id string) error
	Clear(workingDir string) error
}

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
	Version        int           `json:"version"`
	ID             string        `json:"id"`
	Title          string        `json:"title"`
	WorkingDir     string        `json:"working_dir"`
	ParentID       string        `json:"parent_id,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	Model          config.Model  `json:"model"`
	ReasoningLevel string        `json:"reasoning_level"`
	Messages       []llm.Message `json:"messages"`
}
