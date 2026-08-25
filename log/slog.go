//nolint:revive // intentional package name
package log

import (
	"context"
	"log/slog"
	"time"

	"github.com/gasmod/gas"
)

// SlogLogger adapts a standard library [slog.Logger] to the gas.Logger interface.
// It requires no external dependencies beyond the Go standard library.
// Since slog has no Trace level, both Trace and Debug map to [slog.LevelDebug].
type SlogLogger struct {
	logger               *slog.Logger
	eventInitialCapacity int
}

var _ gas.Logger = (*SlogLogger)(nil)

// SlogLoggerCtor defines a constructor function that returns an implementation of the gas.Logger interface.
type SlogLoggerCtor func() *SlogLogger

// SlogLoggerOption is a functional option type for configuring an instance of SlogLogger.
type SlogLoggerOption func(*SlogLogger)

// WithSlogInstance sets the provided slog.Logger instance to the SlogLogger.
func WithSlogInstance(logger *slog.Logger) SlogLoggerOption {
	return func(l *SlogLogger) { l.logger = logger }
}

// WithEventInitialCapacity sets the initial capacity for event attributes in a SlogLogger instance.
func WithEventInitialCapacity(capacity int) SlogLoggerOption {
	return func(l *SlogLogger) { l.eventInitialCapacity = capacity }
}

// NewSlogLogger returns a SlogLoggerCtor that constructs a SlogLogger with the provided SlogLoggerOption values.
func NewSlogLogger(opts ...SlogLoggerOption) SlogLoggerCtor {
	return func() *SlogLogger {
		l := &SlogLogger{logger: slog.Default(), eventInitialCapacity: 5}
		for _, opt := range opts {
			opt(l)
		}
		return l
	}
}

// Trace creates a log event with a debug level and the provided message, enabling attribute chaining before sending.
func (l *SlogLogger) Trace(msg string) gas.LogEvent {
	return &SlogLogEvent{
		logger: l.logger,
		lvl:    slog.LevelDebug,
		msg:    msg,
		attrs:  make([]slog.Attr, 0, l.eventInitialCapacity),
	}
}

// Debug creates a new log event with debug level and the specified message.
func (l *SlogLogger) Debug(msg string) gas.LogEvent {
	return &SlogLogEvent{
		logger: l.logger,
		lvl:    slog.LevelDebug,
		msg:    msg,
		attrs:  make([]slog.Attr, 0, l.eventInitialCapacity),
	}
}

// Info creates an info-level log event with the specified message and initializes its attributes list.
func (l *SlogLogger) Info(msg string) gas.LogEvent {
	return &SlogLogEvent{
		logger: l.logger,
		lvl:    slog.LevelInfo,
		msg:    msg,
		attrs:  make([]slog.Attr, 0, l.eventInitialCapacity),
	}
}

// Warn creates a log event with warn level and a specified message. It returns the constructed log event.
func (l *SlogLogger) Warn(msg string) gas.LogEvent {
	return &SlogLogEvent{
		logger: l.logger,
		lvl:    slog.LevelWarn,
		msg:    msg,
		attrs:  make([]slog.Attr, 0, l.eventInitialCapacity),
	}
}

// Error creates a log event with error level and the specified message.
func (l *SlogLogger) Error(msg string) gas.LogEvent {
	return &SlogLogEvent{
		logger: l.logger,
		lvl:    slog.LevelError,
		msg:    msg,
		attrs:  make([]slog.Attr, 0, l.eventInitialCapacity),
	}
}

// Flush ensures that all buffered log entries are written to their destination immediately.
func (l *SlogLogger) Flush() {}

// With creates and returns a new SlogLoggerContext for structured logging with accumulated attributes.
func (l *SlogLogger) With() gas.LoggerContext {
	return &SlogLoggerContext{
		logger:               l.logger,
		attrs:                make([]any, 0, l.eventInitialCapacity),
		eventInitialCapacity: l.eventInitialCapacity,
	}
}

// SetBaseFields initializes and returns a mutable logger context for adding attributes to the originating logger.
func (l *SlogLogger) SetBaseFields() gas.MutableLoggerContext {
	return &SlogMutableLoggerContext{
		attrs:        make([]any, 0, l.eventInitialCapacity),
		originLogger: l,
	}
}

// SlogMutableLoggerContext accumulates fields and on Apply mutates the
// originating SlogLogger in-place. Implements gas.MutableLoggerContext.
type SlogMutableLoggerContext struct {
	originLogger *SlogLogger
	attrs        []any
}

var _ gas.MutableLoggerContext = (*SlogMutableLoggerContext)(nil)

// Str adds a string attribute to the logger context with the specified key and value and returns the updated context.
func (c *SlogMutableLoggerContext) Str(key, val string) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.String(key, val))
	return c
}

// Int adds an integer attribute with the given key and value to the logger context and returns the updated context.
func (c *SlogMutableLoggerContext) Int(key string, val int) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Int(key, val))
	return c
}

// Int64 appends an int64 attribute with the specified key and value to the logger context and returns the updated context.
func (c *SlogMutableLoggerContext) Int64(key string, val int64) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Int64(key, val))
	return c
}

// Float64 adds a float64 attribute with the given key and value to the logging context and returns the updated context.
func (c *SlogMutableLoggerContext) Float64(key string, val float64) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Float64(key, val))
	return c
}

// Bool adds a boolean attribute with the given key and value to the logger context and returns the updated context.
func (c *SlogMutableLoggerContext) Bool(key string, val bool) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Bool(key, val))
	return c
}

// Err adds an error value to the logger context with the specified key and returns the updated context.
func (c *SlogMutableLoggerContext) Err(key string, val error) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Any(key, val))
	return c
}

// Duration adds a duration attribute to the context with the specified key and value, returning the updated context.
func (c *SlogMutableLoggerContext) Duration(key string, val time.Duration) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Duration(key, val))
	return c
}

// Any adds a key-value pair with a value of any type to the logger context and returns the updated context.
func (c *SlogMutableLoggerContext) Any(key string, val any) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Any(key, val))
	return c
}

// Apply updates the originating logger by appending the accumulated attributes in the current context.
func (c *SlogMutableLoggerContext) Apply() {
	c.originLogger.logger = c.originLogger.logger.With(c.attrs...)
}

// SlogLoggerContext accumulates structured fields and produces a sub-logger
// with those fields baked in when Logger is called. Implements gas.LoggerContext.
type SlogLoggerContext struct {
	logger               *slog.Logger
	attrs                []any
	eventInitialCapacity int
}

var _ gas.LoggerContext = (*SlogLoggerContext)(nil)

// Str adds a string key-value pair to the logger context and returns the updated context.
func (c *SlogLoggerContext) Str(key, val string) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.String(key, val))
	return c
}

// Int adds an integer attribute with the specified key and value to the logger context and returns the updated context.
func (c *SlogLoggerContext) Int(key string, val int) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Int(key, val))
	return c
}

// Int64 appends a key-value pair where the value is an int64 to the logger context and returns the updated context.
func (c *SlogLoggerContext) Int64(key string, val int64) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Int64(key, val))
	return c
}

// Float64 adds a float64 key-value pair to the logger context and returns the updated context.
func (c *SlogLoggerContext) Float64(key string, val float64) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Float64(key, val))
	return c
}

// Bool adds a boolean key-value pair to the logger context and returns the updated logger context.
func (c *SlogLoggerContext) Bool(key string, val bool) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Bool(key, val))
	return c
}

// Err adds an error value to the logger context with the specified key and returns the updated logger context.
func (c *SlogLoggerContext) Err(key string, val error) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Any(key, val))
	return c
}

// Duration adds a time duration attribute to the logger context with the specified key and value.
func (c *SlogLoggerContext) Duration(key string, val time.Duration) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Duration(key, val))
	return c
}

// Any adds a key-value pair to the context using a value of any type and returns the updated LoggerContext.
func (c *SlogLoggerContext) Any(key string, val any) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Any(key, val))
	return c
}

// Logger creates a new logger instance with accumulated attributes and returns it as a gas.Logger implementation.
func (c *SlogLoggerContext) Logger() gas.Logger {
	return &SlogLogger{
		logger:               c.logger.With(c.attrs...),
		eventInitialCapacity: c.eventInitialCapacity,
	}
}

// SlogLogEvent collects structured fields and emits them as a single log record
// via [slog.Logger.LogAttrs] when Send is called. Implements gas.LogEvent.
type SlogLogEvent struct {
	logger *slog.Logger
	msg    string
	attrs  []slog.Attr
	lvl    slog.Level
}

var _ gas.LogEvent = (*SlogLogEvent)(nil)

// Str adds a string attribute with the specified key and value to the log event and returns the updated log event.
func (e *SlogLogEvent) Str(key, value string) gas.LogEvent {
	e.attrs = append(e.attrs, slog.String(key, value))
	return e
}

// Int adds an integer-typed attribute with the specified key and value to the log event and returns the updated event.
func (e *SlogLogEvent) Int(key string, value int) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Int(key, value))
	return e
}

// Int64 adds a key-value pair, where the value is of int64 type, to the log event's attributes.
func (e *SlogLogEvent) Int64(key string, value int64) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Int64(key, value))
	return e
}

// Float64 adds a float64 attribute to the log event with the given key and value.
func (e *SlogLogEvent) Float64(key string, value float64) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Float64(key, value))
	return e
}

// Bool adds a boolean attribute with the specified key and value to the log event and returns the updated log event.
func (e *SlogLogEvent) Bool(key string, value bool) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Bool(key, value))
	return e
}

// Err adds an error attribute to the log event with the specified key and error value and returns the updated log event.
func (e *SlogLogEvent) Err(key string, value error) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Any(key, value))
	return e
}

// Duration adds a time.Duration attribute to the log event with the specified key and value.
func (e *SlogLogEvent) Duration(key string, value time.Duration) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Duration(key, value))
	return e
}

// Any adds a key-value pair with a value of any type to the log event's attributes and returns the modified log event.
func (e *SlogLogEvent) Any(key string, value any) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Any(key, value))
	return e
}

// Send emits the constructed log event with its attributes to the logger.
func (e *SlogLogEvent) Send() {
	e.logger.LogAttrs(context.Background(), e.lvl, e.msg, e.attrs...)
}
