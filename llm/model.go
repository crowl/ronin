package llm

import "fmt"

type Model struct {
	Provider      string
	Name          string
	ContextWindow uint32
}

func (m Model) String() string {
	return fmt.Sprintf("%s:%s", m.Provider, m.Name)
}
