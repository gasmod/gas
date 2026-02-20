package gas

import "time"

// EventData carries typed event payloads through the event bus.
// Each accessor returns (value, found) so callers can distinguish
// between "not present" and "present but zero".
type EventData struct {
	data map[string]any
}

// NewEventData creates an empty EventData.
func NewEventData() EventData {
	return EventData{data: make(map[string]any)}
}

// Set stores a key-value pair. Returns the EventData for chaining.
func (e EventData) Set(key string, value any) EventData {
	e.data[key] = value
	return e
}

// Get returns the value for key, or (nil, false) if not present.
func (e EventData) Get(key string) (any, bool) {
	v, ok := e.data[key]
	return v, ok
}

// GetString returns the string value for key, or ("", false) if not
// present or not a string.
func (e EventData) GetString(key string) (string, bool) {
	v, ok := e.data[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// GetInt returns the int value for key.
func (e EventData) GetInt(key string) (int, bool) {
	v, ok := e.data[key]
	if !ok {
		return 0, false
	}
	i, ok := v.(int)
	return i, ok
}

// GetBool returns the bool value for key.
func (e EventData) GetBool(key string) (value, exists bool) {
	v, ok := e.data[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// GetFloat64 returns the float64 value for key.
func (e EventData) GetFloat64(key string) (float64, bool) {
	v, ok := e.data[key]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}

// GetTime returns the time.Time value for key.
func (e EventData) GetTime(key string) (time.Time, bool) {
	v, ok := e.data[key]
	if !ok {
		return time.Time{}, false
	}
	t, ok := v.(time.Time)
	return t, ok
}

// GetStringSlice returns the []string value for key.
func (e EventData) GetStringSlice(key string) ([]string, bool) {
	v, ok := e.data[key]
	if !ok {
		return nil, false
	}
	s, ok := v.([]string)
	return s, ok
}

// Raw returns the underlying map for advanced use cases.
func (e EventData) Raw() map[string]any {
	return e.data
}
