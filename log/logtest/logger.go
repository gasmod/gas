package logtest

import (
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/gasmod/gas"
)

// Entry is one event a RecordingLogger received.
type Entry struct {
	// Fields holds the event's fields by key, the logger's own included.
	Fields map[string]any
	// Level is `trace`, `debug`, `info`, `warn` or `error`.
	Level string
	// Msg is the event's message.
	Msg string
}

// RecordingLogger is a gas.Logger that keeps every event it sends, as
// described in the package documentation. The zero value is ready to use.
// It is safe for concurrent use; its events are not.
type RecordingLogger struct {
	// root holds the shared entries, or is nil when this logger holds them
	// itself.
	root *RecordingLogger
	// fields are the fields With gave this logger, set on each event before
	// the event's own.
	fields map[string]any
	// entries holds the events sent so far, when root is nil.
	entries []Entry
	// mu guards entries.
	mu sync.Mutex
}

var _ gas.Logger = (*RecordingLogger)(nil)

// New returns an empty RecordingLogger.
func New() *RecordingLogger { return &RecordingLogger{} }

// Trace starts an event at level `trace`.
func (l *RecordingLogger) Trace(msg string) gas.LogEvent { return l.event("trace", msg) }

// Debug starts an event at level `debug`.
func (l *RecordingLogger) Debug(msg string) gas.LogEvent { return l.event("debug", msg) }

// Info starts an event at level `info`.
func (l *RecordingLogger) Info(msg string) gas.LogEvent { return l.event("info", msg) }

// Warn starts an event at level `warn`.
func (l *RecordingLogger) Warn(msg string) gas.LogEvent { return l.event("warn", msg) }

// Error starts an event at level `error`.
func (l *RecordingLogger) Error(msg string) gas.LogEvent { return l.event("error", msg) }

// With returns a context for a logger that records into l's entries, with
// the fields of l and those added to the context on every event.
func (l *RecordingLogger) With() gas.LoggerContext {
	return &recordingContext{root: l.store(), fields: maps.Clone(l.fields)}
}

// Flush does nothing.
func (*RecordingLogger) Flush() {}

// Entries returns a copy of the events sent so far, in order, by l and
// every logger that shares its entries. The copy holds copies of the
// field maps.
func (l *RecordingLogger) Entries() []Entry {
	r := l.store()

	r.mu.Lock()
	defer r.mu.Unlock()

	out := slices.Clone(r.entries)
	for i := range out {
		out[i].Fields = maps.Clone(out[i].Fields)
	}

	return out
}

// store returns the logger that holds l's entries.
func (l *RecordingLogger) store() *RecordingLogger {
	if l.root != nil {
		return l.root
	}

	return l
}

// event starts an event at level, holding the logger's fields.
func (l *RecordingLogger) event(level, msg string) gas.LogEvent {
	fields := maps.Clone(l.fields)
	if fields == nil {
		fields = map[string]any{}
	}

	return &recordingEvent{store: l.store(), entry: Entry{Fields: fields, Level: level, Msg: msg}}
}

// recordingContext collects the fields of a logger from
// RecordingLogger.With.
type recordingContext struct {
	// root is the logger that holds the entries.
	root *RecordingLogger
	// fields are the parent's fields and those added since.
	fields map[string]any
}

var _ gas.LoggerContext = (*recordingContext)(nil)

// Str adds a string field.
func (c *recordingContext) Str(key, val string) gas.LoggerContext { return c.set(key, val) }

// Int adds an int field.
func (c *recordingContext) Int(key string, val int) gas.LoggerContext { return c.set(key, val) }

// Int64 adds an int64 field.
func (c *recordingContext) Int64(key string, val int64) gas.LoggerContext {
	return c.set(key, val)
}

// Float64 adds a float64 field.
func (c *recordingContext) Float64(key string, val float64) gas.LoggerContext {
	return c.set(key, val)
}

// Bool adds a bool field.
func (c *recordingContext) Bool(key string, val bool) gas.LoggerContext { return c.set(key, val) }

// Err adds an error field.
func (c *recordingContext) Err(key string, val error) gas.LoggerContext { return c.set(key, val) }

// Duration adds a time.Duration field.
func (c *recordingContext) Duration(key string, val time.Duration) gas.LoggerContext {
	return c.set(key, val)
}

// Any adds a field of any type.
func (c *recordingContext) Any(key string, val any) gas.LoggerContext { return c.set(key, val) }

// Logger returns a logger that records into the shared entries with the
// fields added so far. Fields added to the context later do not reach it.
func (c *recordingContext) Logger() gas.Logger {
	return &RecordingLogger{root: c.root, fields: maps.Clone(c.fields)}
}

// set adds one field.
func (c *recordingContext) set(key string, val any) gas.LoggerContext {
	if c.fields == nil {
		c.fields = map[string]any{}
	}

	c.fields[key] = val

	return c
}

// recordingEvent collects fields until Send hands the event to its store.
type recordingEvent struct {
	// store receives the event on Send.
	store *RecordingLogger
	// entry is the event being built.
	entry Entry
	// sent is set by Send; every method is a no-op after it.
	sent bool
}

var _ gas.LogEvent = (*recordingEvent)(nil)

// Str records a string field.
func (e *recordingEvent) Str(key, val string) gas.LogEvent { return e.set(key, val) }

// Int records an int field.
func (e *recordingEvent) Int(key string, val int) gas.LogEvent { return e.set(key, val) }

// Int64 records an int64 field.
func (e *recordingEvent) Int64(key string, val int64) gas.LogEvent { return e.set(key, val) }

// Float64 records a float64 field.
func (e *recordingEvent) Float64(key string, val float64) gas.LogEvent { return e.set(key, val) }

// Bool records a bool field.
func (e *recordingEvent) Bool(key string, val bool) gas.LogEvent { return e.set(key, val) }

// Err records an error field.
func (e *recordingEvent) Err(key string, val error) gas.LogEvent { return e.set(key, val) }

// Duration records a time.Duration field.
func (e *recordingEvent) Duration(key string, val time.Duration) gas.LogEvent {
	return e.set(key, val)
}

// Any records a field of any type.
func (e *recordingEvent) Any(key string, val any) gas.LogEvent { return e.set(key, val) }

// Send appends the event to the entries. Send does nothing the second
// time.
func (e *recordingEvent) Send() {
	if e.sent {
		return
	}

	e.sent = true

	e.store.mu.Lock()
	defer e.store.mu.Unlock()

	e.store.entries = append(e.store.entries, e.entry)
}

// set records one field, unless the event was sent.
func (e *recordingEvent) set(key string, val any) gas.LogEvent {
	if !e.sent {
		e.entry.Fields[key] = val
	}

	return e
}
