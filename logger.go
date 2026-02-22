package gas

import (
	"context"
	"time"
)

type loggerKey struct{}

// WithLogger returns a copy of ctx with the given Logger attached.
func WithLogger(ctx context.Context, logger Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}

// LoggerFromContext returns the Logger stored in ctx, or nil if none is present.
func LoggerFromContext(ctx context.Context) Logger {
	l, _ := ctx.Value(loggerKey{}).(Logger)
	return l
}

// Logger is the provider interface for structured logging in the Gas ecosystem.
// Implementations produce LogEvent values at a given severity level. Each method
// returns a LogEvent that can be enriched with typed fields before calling Send.
//
// Use With to derive a sub-logger that carries persistent fields across all
// subsequent log events.
//
// Call Flush to ensure all buffered log entries are written (e.g. before shutdown).
type Logger interface {
	// Trace starts a log event at the TRACE level.
	Trace(msg string) LogEvent
	// Debug starts a log event at the DEBUG level.
	Debug(msg string) LogEvent
	// Info starts a log event at the INFO level.
	Info(msg string) LogEvent
	// Warn starts a log event at the WARN level.
	Warn(msg string) LogEvent
	// Error starts a log event at the ERROR level.
	Error(msg string) LogEvent

	// With returns a LoggerContext for building a sub-logger with persistent fields.
	With() LoggerContext

	// SetBaseFields returns a MutableLoggerContext for attaching persistent fields
	// directly to this logger instance. Unlike With, which branches into a new logger,
	// SetBaseFields mutates the receiver so that all subsequent log events carry the
	// accumulated fields. Intended for use by request-scoped middleware.
	SetBaseFields() MutableLoggerContext

	// Flush writes any buffered log entries to the underlying output.
	Flush()
}

// LoggerContext is a builder for deriving a sub-logger with persistent fields.
// Each field method returns the same LoggerContext for chaining. Call Logger to
// obtain the resulting Logger that carries all accumulated fields.
type LoggerContext interface {
	Str(key, val string) LoggerContext
	Int(key string, val int) LoggerContext
	Int64(key string, val int64) LoggerContext
	Float64(key string, val float64) LoggerContext
	Bool(key string, val bool) LoggerContext
	Err(key string, val error) LoggerContext
	Duration(key string, val time.Duration) LoggerContext
	Any(key string, val any) LoggerContext
	// Logger returns a new Logger that includes all fields added to this context.
	Logger() Logger
}

// MutableLoggerContext is a builder for attaching persistent fields directly to
// an existing Logger instance. Unlike LoggerContext (returned by With), which
// produces a new branched Logger, MutableLoggerContext.Apply() mutates the
// receiver in place. Intended for use in request-scoped middleware where the
// same Logger instance is shared across the whole request.
type MutableLoggerContext interface {
	Str(key, val string) MutableLoggerContext
	Int(key string, val int) MutableLoggerContext
	Int64(key string, val int64) MutableLoggerContext
	Float64(key string, val float64) MutableLoggerContext
	Bool(key string, val bool) MutableLoggerContext
	Err(key string, val error) MutableLoggerContext
	Duration(key string, val time.Duration) MutableLoggerContext
	Any(key string, val any) MutableLoggerContext
	// Apply mutates the originating Logger in place with all accumulated fields.
	Apply()
}

// LogEvent is a single structured log entry. Field methods return the same
// LogEvent for chaining. Call Send to finalize and emit the event.
type LogEvent interface {
	Str(key, val string) LogEvent
	Int(key string, val int) LogEvent
	Int64(key string, val int64) LogEvent
	Float64(key string, val float64) LogEvent
	Bool(key string, val bool) LogEvent
	Err(key string, val error) LogEvent
	Duration(key string, val time.Duration) LogEvent
	Any(key string, val any) LogEvent
	// Send finalizes and emits the log event.
	Send()
}
