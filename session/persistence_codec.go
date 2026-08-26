package session

// EncodeEvent serializes an event into its journal type tag and payload bytes.
// It is intended for persistence implementations; callers should normally use
// Store.Append rather than encoding journal records directly.
func EncodeEvent(event Event) (string, []byte, error) {
	return encodeEvent(event)
}

// DecodeEvent reconstructs an event from its stored type tag and payload.
// It is intended for persistence implementations.
func DecodeEvent(eventType string, payload []byte) (Event, error) {
	return decodeEvent(eventType, payload)
}
