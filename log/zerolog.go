//nolint:revive // intentional package name
package log

import (
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/gasmod/gas"
)

// ZeroLogLogger adapts a [zerolog.Logger] to the gas.Logger interface.
// It provides high-performance structured JSON logging with full support
// for all gas log levels including Trace.
type ZeroLogLogger struct {
	logger *zerolog.Logger
}

var _ gas.Logger = (*ZeroLogLogger)(nil)

// ZeroLogLoggerCtor is a function type that constructs and returns a gas.Logger instance.
type ZeroLogLoggerCtor func() (*ZeroLogLogger, error)

// ZeroLogLoggerOption represents a functional option for configuring a ZeroLogLogger instance.
type ZeroLogLoggerOption func(*ZeroLogLogger)

// WithZeroLogInstance sets the underlying zerolog.Logger instance for the ZeroLogLogger.
func WithZeroLogInstance(logger *zerolog.Logger) ZeroLogLoggerOption {
	return func(l *ZeroLogLogger) { l.logger = logger }
}

// NewZeroLogLogger creates a new ZeroLogLoggerCtor with optional configuration applied via ZeroLogLoggerOption functions.
func NewZeroLogLogger(opts ...ZeroLogLoggerOption) ZeroLogLoggerCtor {
	return func() (*ZeroLogLogger, error) {
		l := &ZeroLogLogger{logger: &log.Logger}
		for _, opt := range opts {
			opt(l)
		}
		return l, nil
	}
}

// Trace creates a trace-level log event with the provided message and returns it for further field chaining.
func (l *ZeroLogLogger) Trace(msg string) gas.LogEvent {
	return &ZeroLogLogEvent{evt: l.logger.Trace(), msg: msg}
}

// Debug logs a message at the debug level and returns a LogEvent for adding structured fields before emitting.
func (l *ZeroLogLogger) Debug(msg string) gas.LogEvent {
	return &ZeroLogLogEvent{evt: l.logger.Debug(), msg: msg}
}

// Info logs an informational message and returns a LogEvent for adding structured fields before emitting.
func (l *ZeroLogLogger) Info(msg string) gas.LogEvent {
	return &ZeroLogLogEvent{evt: l.logger.Info(), msg: msg}
}

// Warn logs a warning-level message and returns a log event for attaching additional fields before sending.
func (l *ZeroLogLogger) Warn(msg string) gas.LogEvent {
	return &ZeroLogLogEvent{evt: l.logger.Warn(), msg: msg}
}

// Error logs a message at the error level and returns a log event for further customization.
func (l *ZeroLogLogger) Error(msg string) gas.LogEvent {
	return &ZeroLogLogEvent{evt: l.logger.Error(), msg: msg}
}

// Flush ensures that all buffered log entries are written to their destination immediately.
func (l *ZeroLogLogger) Flush() {}

// With creates and returns a new LoggerContext for structured logging with additional fields.
func (l *ZeroLogLogger) With() gas.LoggerContext {
	return &ZeroLogLoggerContext{ctx: l.logger.With()}
}

// ZeroLogLoggerContext wraps a [zerolog.Context] to implement gas.LoggerContext.
// Fields added via chained methods are baked into the sub-logger returned by Logger.
type ZeroLogLoggerContext struct {
	ctx zerolog.Context
}

var _ gas.LoggerContext = (*ZeroLogLoggerContext)(nil)

// Str adds a string key-value pair to the logger context and returns the updated LoggerContext.
func (c *ZeroLogLoggerContext) Str(key, val string) gas.LoggerContext {
	c.ctx = c.ctx.Str(key, val)
	return c
}

// Int adds an integer field with the given key and value to the logging context and returns the updated context.
func (c *ZeroLogLoggerContext) Int(key string, val int) gas.LoggerContext {
	c.ctx = c.ctx.Int(key, val)
	return c
}

// Int64 adds an int64 key-value pair to the logging context and returns the updated LoggerContext.
func (c *ZeroLogLoggerContext) Int64(key string, val int64) gas.LoggerContext {
	c.ctx = c.ctx.Int64(key, val)
	return c
}

// Float64 adds a float64 value with the specified key to the logger context and returns the updated context.
func (c *ZeroLogLoggerContext) Float64(key string, val float64) gas.LoggerContext {
	c.ctx = c.ctx.Float64(key, val)
	return c
}

// Bool adds a boolean field with the specified key and value to the logging context and returns the updated context.
func (c *ZeroLogLoggerContext) Bool(key string, val bool) gas.LoggerContext {
	c.ctx = c.ctx.Bool(key, val)
	return c
}

// Err adds an error field to the logging context with the specified key and value.
func (c *ZeroLogLoggerContext) Err(key string, val error) gas.LoggerContext {
	c.ctx = c.ctx.AnErr(key, val)
	return c
}

// Duration adds a time.Duration field to the logger context with the specified key and value.
func (c *ZeroLogLoggerContext) Duration(key string, val time.Duration) gas.LoggerContext {
	c.ctx = c.ctx.Dur(key, val)
	return c
}

// Any adds a field with the given key and a value of any type to the logging context.
func (c *ZeroLogLoggerContext) Any(key string, val any) gas.LoggerContext {
	c.ctx = c.ctx.Any(key, val)
	return c
}

// Logger creates a new structured logger instance with all chained fields applied.
func (c *ZeroLogLoggerContext) Logger() gas.Logger {
	return &ZeroLogLogger{logger: new(c.ctx.Logger())}
}

// ZeroLogLogEvent wraps a [zerolog.Event] to implement gas.LogEvent.
// Fields are chained fluently and the event is emitted when Send is called.
type ZeroLogLogEvent struct {
	evt *zerolog.Event
	msg string
}

var _ gas.LogEvent = (*ZeroLogLogEvent)(nil)

// Str adds a string field with the given key and value to the log event and returns the updated log event.
func (e *ZeroLogLogEvent) Str(key, value string) gas.LogEvent {
	e.evt.Str(key, value)
	return e
}

// Int adds an integer field to the log event with the specified key and value and returns the updated log event.
func (e *ZeroLogLogEvent) Int(key string, value int) gas.LogEvent {
	e.evt.Int(key, value)
	return e
}

// Int64 adds an int64 key-value pair to the log event and returns the updated event for method chaining.
func (e *ZeroLogLogEvent) Int64(key string, value int64) gas.LogEvent {
	e.evt.Int64(key, value)
	return e
}

// Float64 adds a float64 key-value pair to the log event and returns the updated log event.
func (e *ZeroLogLogEvent) Float64(key string, value float64) gas.LogEvent {
	e.evt.Float64(key, value)
	return e
}

// Bool adds a boolean field to the log event with the specified key and value.
func (e *ZeroLogLogEvent) Bool(key string, value bool) gas.LogEvent {
	e.evt.Bool(key, value)
	return e
}

// Err adds an error key-value pair to the log event and returns the updated log event.
func (e *ZeroLogLogEvent) Err(key string, value error) gas.LogEvent {
	e.evt.AnErr(key, value)
	return e
}

// Duration adds a key-value pair where the value is a time.Duration, returning the updated log event instance.
func (e *ZeroLogLogEvent) Duration(key string, value time.Duration) gas.LogEvent {
	e.evt.Dur(key, value)
	return e
}

// Any adds a key-value pair to the log event, where the value can be of any data type, and returns the updated event.
func (e *ZeroLogLogEvent) Any(key string, value any) gas.LogEvent {
	e.evt.Any(key, value)
	return e
}

// Send emits the configured log event with the current message and attributes.
func (e *ZeroLogLogEvent) Send() {
	e.evt.Msg(e.msg)
}
