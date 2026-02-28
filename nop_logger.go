package gas

import "time"

var nopLogger = &NopLogger{}

// NopLogger is a Logger implementation that silently discards all log output.
// It uses a singleton pattern — all instances share the same underlying value,
// resulting in zero allocations per log call.
type NopLogger struct{}

var _ Logger = (*NopLogger)(nil)

// NopLoggerCtor defines a constructor function that returns a nop-logger implementing the Logger interface.
type NopLoggerCtor func() *NopLogger

// NewNopLogger returns a NopLoggerCtor that constructs a nop-logger implementing the Logger interface.
func NewNopLogger() NopLoggerCtor {
	return func() *NopLogger {
		return nopLogger
	}
}

// Trace creates a no-op log event for a trace-level message and silently discards it.
func (l *NopLogger) Trace(string) LogEvent { return nopLogEvent }

// Debug logs a debug-level message and returns a no-op LogEvent for chaining.
func (l *NopLogger) Debug(string) LogEvent { return nopLogEvent }

// Info logs an informational message and returns a no-op LogEvent instance.
func (l *NopLogger) Info(string) LogEvent { return nopLogEvent }

// Warn logs a warning-level message and returns a no-op LogEvent instance.
func (l *NopLogger) Warn(string) LogEvent { return nopLogEvent }

// Error logs an error-level message with no operational effect, returning a no-op LogEvent.
func (l *NopLogger) Error(string) LogEvent { return nopLogEvent }

// Flush performs no operation as NopLogger discards all log output without buffering.
func (l *NopLogger) Flush() {}

// With creates a new LoggerContext for deriving a sub-logger with persistent fields across log events.
func (l *NopLogger) With() LoggerContext {
	return nopLoggerContext
}

// SetBaseFields returns a no-op MutableLoggerContext associated with the NopLogger instance.
func (l *NopLogger) SetBaseFields() MutableLoggerContext {
	return nopMutableLoggerContext
}

var nopLoggerContext = &NopLoggerContext{}

// NopLoggerContext is a LoggerContext that discards all fields.
// All methods return the receiver for chaining and Logger returns a [NopLogger].
type NopLoggerContext struct{}

var _ LoggerContext = (*NopLoggerContext)(nil)

// Str adds a string key-value pair to the logger context and returns the same context for chaining.
func (c *NopLoggerContext) Str(string, string) LoggerContext { return c }

// Int adds an integer field to the logger context with the specified key. Returns the same LoggerContext for chaining.
func (c *NopLoggerContext) Int(string, int) LoggerContext { return c }

// Int64 adds a key-value pair with an int64 value to the log context and returns the updated LoggerContext.
func (c *NopLoggerContext) Int64(string, int64) LoggerContext { return c }

// Float64 adds a float64 field with the specified key and value to the context and returns the LoggerContext for chaining.
func (c *NopLoggerContext) Float64(string, float64) LoggerContext { return c }

// Bool adds a boolean field to the logging context under the specified key for structured logging.
func (c *NopLoggerContext) Bool(string, bool) LoggerContext { return c }

// Err adds an error field to the logger context with the specified key and error value, returning the same context.
func (c *NopLoggerContext) Err(string, error) LoggerContext { return c }

// Duration adds a time.Duration field with the specified key and value to the logging context and returns it.
func (c *NopLoggerContext) Duration(string, time.Duration) LoggerContext { return c }

// Any adds a field with the given key and value to the context, supporting any type, and returns the LoggerContext.
func (c *NopLoggerContext) Any(string, any) LoggerContext { return c }

// Logger returns a no-op Logger instance that discards all log events.
func (c *NopLoggerContext) Logger() Logger { return nopLogger }

var nopMutableLoggerContext = &NopMutableLoggerContext{}

// NopMutableLoggerContext is a MutableLoggerContext that discards all fields.
// All methods return the receiver for chaining and Apply is a no-op.
type NopMutableLoggerContext struct{}

var _ MutableLoggerContext = (*NopMutableLoggerContext)(nil)

// Str adds a string field with the given key and value to the logger context and returns the context for chaining.
func (c *NopMutableLoggerContext) Str(string, string) MutableLoggerContext { return c }

// Int adds an integer field to the context and returns the updated MutableLoggerContext.
func (c *NopMutableLoggerContext) Int(string, int) MutableLoggerContext { return c }

// Int64 adds a key-value pair where the value is an int64 and returns the current MutableLoggerContext for chaining.
func (c *NopMutableLoggerContext) Int64(string, int64) MutableLoggerContext { return c }

// Float64 adds a float64 key-value pair to the context without storing it and returns the receiver for chaining.
func (c *NopMutableLoggerContext) Float64(string, float64) MutableLoggerContext { return c }

// Bool adds a boolean field with the given key and value to the logger context and returns the context for chaining.
func (c *NopMutableLoggerContext) Bool(string, bool) MutableLoggerContext { return c }

// Err adds an error field to the logger context and returns the same context for chaining.
func (c *NopMutableLoggerContext) Err(string, error) MutableLoggerContext { return c }

// Duration adds a duration field to the logging context with the specified key and value.
func (c *NopMutableLoggerContext) Duration(string, time.Duration) MutableLoggerContext { return c }

// Any adds a custom key-value pair with a generic value to the context and returns the current MutableLoggerContext.
func (c *NopMutableLoggerContext) Any(string, any) MutableLoggerContext { return c }

// Apply performs a no-op operation and returns without modifying any state or producing side effects.
func (c *NopMutableLoggerContext) Apply() {}

var nopLogEvent = &NopLogEvent{}

// NopLogEvent is a LogEvent that discards all fields and performs no
// action on Send. All methods return the receiver for chaining.
type NopLogEvent struct{}

var _ LogEvent = (*NopLogEvent)(nil)

// Str adds a string field with the given key and value to the log event and returns the event for chaining.
func (e *NopLogEvent) Str(string, string) LogEvent { return e }

// Int adds an integer field to the log event with the provided key and value, returning the LogEvent for chaining.
func (e *NopLogEvent) Int(string, int) LogEvent { return e }

// Int64 adds an int64 field to the log event with the specified key and value. It returns the LogEvent for chaining.
func (e *NopLogEvent) Int64(string, int64) LogEvent { return e }

// Float64 sets a float64 value for the specified key in the log event. It returns the LogEvent instance for chaining.
func (e *NopLogEvent) Float64(string, float64) LogEvent { return e }

// Bool adds a boolean field to the log event and returns the event for method chaining.
func (e *NopLogEvent) Bool(string, bool) LogEvent { return e }

// Err adds an error field to the log event with the specified key and value. Returns the log event for chaining.
func (e *NopLogEvent) Err(string, error) LogEvent { return e }

// Duration adds a field with a duration value to the log entry, identified by the given key.
func (e *NopLogEvent) Duration(string, time.Duration) LogEvent { return e }

// Any adds a key-value pair to the event, where the value can be of any type, returning the event for chaining.
func (e *NopLogEvent) Any(string, any) LogEvent { return e }

// Send finalizes the log event. In NopLogEvent, it performs no action and discards the event.
func (e *NopLogEvent) Send() {}
