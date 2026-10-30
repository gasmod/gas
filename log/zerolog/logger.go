package zerolog

import (
	"fmt"
	"time"

	rz "github.com/rs/zerolog"
	rzlog "github.com/rs/zerolog/log"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/log"
)

// ErrNilLogger indicates that WithLogger was given a nil logger. It is
// log.ErrNilLogger, so errors.Is matches either.
var ErrNilLogger = log.ErrNilLogger

// disabledEvent is the event returned for a level the logger does not
// enable. It is gas.NopLogger's, shared, so a disabled event allocates
// nothing and ignores its fields.
var disabledEvent = (&gas.NopLogger{}).Info("")

// Logger is a gas.Logger over a *zerolog.Logger, as described in the
// package documentation. It is safe for concurrent use; its events are
// not.
type Logger struct {
	// logger writes the events.
	logger *rz.Logger
}

var _ gas.Logger = (*Logger)(nil)

// Option configures a Logger.
type Option func(*Logger)

// WithLogger sets the *zerolog.Logger the Logger writes to. A nil logger
// fails the constructor with ErrNilLogger.
//
// Default: zerolog's global logger, github.com/rs/zerolog/log.Logger, read
// through a pointer, so a later assignment to it is seen.
func WithLogger(logger *rz.Logger) Option {
	return func(l *Logger) { l.logger = logger }
}

// New returns the constructor of a Logger with opts applied, in the shape
// the gas container expects. The constructor returns an error wrapping
// ErrNilLogger when WithLogger was given nil.
func New(opts ...Option) func() (*Logger, error) {
	return func() (*Logger, error) {
		l := &Logger{logger: &rzlog.Logger}
		for _, opt := range opts {
			opt(l)
		}

		if l.logger == nil {
			return nil, fmt.Errorf("gas/log: %w", ErrNilLogger)
		}

		return l, nil
	}
}

// Trace starts an event at zerolog.TraceLevel.
func (l *Logger) Trace(msg string) gas.LogEvent {
	return newEvent(l.logger.Trace(), msg) //nolint:zerologlint // Send dispatches it.
}

// Debug starts an event at zerolog.DebugLevel.
func (l *Logger) Debug(msg string) gas.LogEvent {
	return newEvent(l.logger.Debug(), msg) //nolint:zerologlint // Send dispatches it.
}

// Info starts an event at zerolog.InfoLevel.
func (l *Logger) Info(msg string) gas.LogEvent {
	return newEvent(l.logger.Info(), msg) //nolint:zerologlint // Send dispatches it.
}

// Warn starts an event at zerolog.WarnLevel.
func (l *Logger) Warn(msg string) gas.LogEvent {
	return newEvent(l.logger.Warn(), msg) //nolint:zerologlint // Send dispatches it.
}

// Error starts an event at zerolog.ErrorLevel.
func (l *Logger) Error(msg string) gas.LogEvent {
	return newEvent(l.logger.Error(), msg) //nolint:zerologlint // Send dispatches it.
}

// Flush does nothing: zerolog writes each event on Send. A writer that
// buffers, such as a diode, is flushed by its owner.
func (*Logger) Flush() {}

// With returns a context for a logger that writes to the same writer with
// the fields added to the context.
func (l *Logger) With() gas.LoggerContext {
	return &loggerContext{ctx: l.logger.With()}
}

// newEvent wraps evt, or returns disabledEvent for the nil event zerolog
// returns for a disabled level.
func newEvent(evt *rz.Event, msg string) gas.LogEvent {
	if evt == nil {
		return disabledEvent
	}

	return &event{evt: evt, msg: msg}
}

// loggerContext collects the fields of a logger from Logger.With.
type loggerContext struct {
	// ctx holds the fields added so far.
	ctx rz.Context
}

var _ gas.LoggerContext = (*loggerContext)(nil)

// Str adds a string field.
func (c *loggerContext) Str(key, val string) gas.LoggerContext {
	c.ctx = c.ctx.Str(key, val)

	return c
}

// Int adds an int field.
func (c *loggerContext) Int(key string, val int) gas.LoggerContext {
	c.ctx = c.ctx.Int(key, val)

	return c
}

// Int64 adds an int64 field.
func (c *loggerContext) Int64(key string, val int64) gas.LoggerContext {
	c.ctx = c.ctx.Int64(key, val)

	return c
}

// Float64 adds a float64 field.
func (c *loggerContext) Float64(key string, val float64) gas.LoggerContext {
	c.ctx = c.ctx.Float64(key, val)

	return c
}

// Bool adds a bool field.
func (c *loggerContext) Bool(key string, val bool) gas.LoggerContext {
	c.ctx = c.ctx.Bool(key, val)

	return c
}

// Err adds an error field, as zerolog's AnErr: a nil error adds nothing.
func (c *loggerContext) Err(key string, val error) gas.LoggerContext {
	c.ctx = c.ctx.AnErr(key, val)

	return c
}

// Duration adds a time.Duration field, as zerolog's Dur.
func (c *loggerContext) Duration(key string, val time.Duration) gas.LoggerContext {
	c.ctx = c.ctx.Dur(key, val)

	return c
}

// Any adds a field of any type: an error as Err does, anything else as
// zerolog's Interface.
func (c *loggerContext) Any(key string, val any) gas.LoggerContext {
	if err, ok := val.(error); ok {
		return c.Err(key, err)
	}

	c.ctx = c.ctx.Interface(key, val)

	return c
}

// Logger returns a logger with the fields added so far. Fields added to the
// context later do not reach it.
func (c *loggerContext) Logger() gas.Logger {
	return &Logger{logger: new(c.ctx.Logger())}
}

// event wraps a zerolog event until Send.
type event struct {
	// evt is the zerolog event, or nil once sent. zerolog pools its
	// events, so the pointer is dropped on Send: a later call cannot reach
	// the event zerolog hands out next.
	evt *rz.Event
	// msg is the event's message.
	msg string
}

var _ gas.LogEvent = (*event)(nil)

// Str adds a string field.
func (e *event) Str(key, val string) gas.LogEvent {
	e.evt.Str(key, val)

	return e
}

// Int adds an int field.
func (e *event) Int(key string, val int) gas.LogEvent {
	e.evt.Int(key, val)

	return e
}

// Int64 adds an int64 field.
func (e *event) Int64(key string, val int64) gas.LogEvent {
	e.evt.Int64(key, val)

	return e
}

// Float64 adds a float64 field.
func (e *event) Float64(key string, val float64) gas.LogEvent {
	e.evt.Float64(key, val)

	return e
}

// Bool adds a bool field.
func (e *event) Bool(key string, val bool) gas.LogEvent {
	e.evt.Bool(key, val)

	return e
}

// Err adds an error field, as zerolog's AnErr: a nil error adds nothing.
func (e *event) Err(key string, val error) gas.LogEvent {
	e.evt.AnErr(key, val)

	return e
}

// Duration adds a time.Duration field, as zerolog's Dur.
func (e *event) Duration(key string, val time.Duration) gas.LogEvent {
	e.evt.Dur(key, val)

	return e
}

// Any adds a field of any type: an error as Err does, anything else as
// zerolog's Interface.
func (e *event) Any(key string, val any) gas.LogEvent {
	if err, ok := val.(error); ok {
		return e.Err(key, err)
	}

	e.evt.Interface(key, val)

	return e
}

// Send writes the event, with zerolog's caller, when its logger adds one,
// set to the line that called Send. Send does nothing the second time.
func (e *event) Send() {
	if e.evt == nil {
		return
	}

	// One frame more than zerolog's own Msg: this method.
	e.evt.CallerSkipFrame(1).Msg(e.msg)

	e.evt = nil
}
