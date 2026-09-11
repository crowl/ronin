package session

// DisplayHistory projects effective messages and the local execution audit.
// Context replacement removes old model messages, never local shell records.
// This projection must not be sent to a model.
func DisplayHistory(events []Event) []Event {
	var history []Event
	for _, event := range events {
		switch event.Type {
		case EventMessage, EventShellCommand, EventShellOutput, EventShellStatus:
			history = append(history, event)
		case EventCompaction, EventContextReset:
			kept := history[:0]
			for _, old := range history {
				if old.Type != EventMessage {
					kept = append(kept, old)
				}
			}
			history = kept
			for _, message := range event.Compacted {
				history = append(history, Event{Type: EventMessage, Message: message})
			}
		}
	}
	return history
}
