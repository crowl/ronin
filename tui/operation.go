package tui

// operationKind is the mutually exclusive foreground activity. Cancellation
// requests do not clear it: only worker completion makes the UI idle.
type operationKind uint8

const (
	operationIdle operationKind = iota
	operationPrompt
	operationWorkflow
	operationCompaction
	operationMCP
	operationShell
)

type operationState struct {
	kind  operationKind
	label string
}

func (m *appModel) busy() bool        { return m.operation.kind != operationIdle }
func (m *appModel) shellActive() bool { return m.operation.kind == operationShell }

func (m *appModel) beginOperation(kind operationKind, label string) {
	m.operation = operationState{kind: kind, label: label}
	m.indicatorFrame = 0
}

// completeOperation owns queue consumption. Shell input is rejected while busy;
// cancelled workflows discard queued input. Other operations preserve the
// existing behavior of submitting queued input even after cancellation.
func (m *appModel) completeOperation(cancelled bool) modelUpdate {
	kind := m.operation.kind
	m.operation = operationState{}
	m.statusBarCache.Reset()
	next := m.steeringPrompt
	m.steeringPrompt = ""
	update := modelUpdate{Render: true}
	if kind != operationShell && !(kind == operationWorkflow && cancelled) && next != "" {
		update.Action = submitPromptAction{Prompt: next}
	}
	return update
}
