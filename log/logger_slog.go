package log

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"github.com/gasmod/gas"
)

// LevelTrace is the slog level of the Trace events of a SlogLogger and a
// ShippingLogger, below slog.LevelDebug. The slog handlers print it as
// `DEBUG-4`.
const LevelTrace = slog.LevelDebug - 4

// callersSkip is the frames runtime.Callers skips in Send: itself and
// Send.
const callersSkip = 2

// disabledEvent is the event returned for a level the handler does not
// enable. It is gas.NopLogger's, shared, so a disabled event allocates
// nothing and ignores its fields.
var disabledEvent = (&gas.NopLogger{}).Info("")

// SlogLogger is a gas.Logger over a *slog.Logger, as described in the
// package documentation. It is safe for concurrent use; its events are
// not.
type SlogLogger struct {
	// logger receives the records.
	logger *slog.Logger
	// flush is what Flush calls, or nil. A ShippingLogger sets it, and the
	// loggers from With keep it, so their Flush reaches the same sender.
	flush func()
}

var _ gas.Logger = (*SlogLogger)(nil)

// SlogOption configures a SlogLogger.
type SlogOption func(*SlogLogger)

// WithSlogLogger sets the *slog.Logger the SlogLogger writes to. A nil
// logger fails the constructor with ErrNilLogger.
//
// Default: slog.Default() when the constructor runs.
func WithSlogLogger(logger *slog.Logger) SlogOption {
	return func(l *SlogLogger) { l.logger = logger }
}

// NewSlogLogger returns the constructor of a SlogLogger with opts applied,
// in the shape the gas container expects. The constructor returns an error
// wrapping ErrNilLogger when WithSlogLogger was given nil.
func NewSlogLogger(opts ...SlogOption) func() (*SlogLogger, error) {
	return func() (*SlogLogger, error) {
		l := &SlogLogger{logger: slog.Default()}
		for _, opt := range opts {
			opt(l)
		}

		if l.logger == nil {
			return nil, fmt.Errorf("%s: %w", errPrefix, ErrNilLogger)
		}

		return l, nil
	}
}

// Trace starts an event at LevelTrace.
func (l *SlogLogger) Trace(msg string) gas.LogEvent { return l.event(LevelTrace, msg) }

// Debug starts an event at slog.LevelDebug.
func (l *SlogLogger) Debug(msg string) gas.LogEvent { return l.event(slog.LevelDebug, msg) }

// Info starts an event at slog.LevelInfo.
func (l *SlogLogger) Info(msg string) gas.LogEvent { return l.event(slog.LevelInfo, msg) }

// Warn starts an event at slog.LevelWarn.
func (l *SlogLogger) Warn(msg string) gas.LogEvent { return l.event(slog.LevelWarn, msg) }

// Error starts an event at slog.LevelError.
func (l *SlogLogger) Error(msg string) gas.LogEvent { return l.event(slog.LevelError, msg) }

// Flush does nothing for a SlogLogger of its own, which buffers nothing.
// For a logger from a ShippingLogger it is the ShippingLogger's Flush.
func (l *SlogLogger) Flush() {
	if l.flush != nil {
		l.flush()
	}
}

// With returns a context for a logger that writes to the same handler with
// the fields added to the context.
func (l *SlogLogger) With() gas.LoggerContext {
	return &slogContext{logger: l.logger, flush: l.flush}
}

// event starts an event at level, or returns disabledEvent when the
// handler does not enable level.
func (l *SlogLogger) event(level slog.Level, msg string) gas.LogEvent {
	if !l.logger.Enabled(context.Background(), level) {
		return disabledEvent
	}

	return &slogEvent{logger: l.logger, msg: msg, level: level}
}

// slogContext collects the fields of a logger from SlogLogger.With.
type slogContext struct {
	// logger is the logger With was called on.
	logger *slog.Logger
	// flush is the flush of that logger.
	flush func()
	// attrs are the fields added so far, as slog.Attr values.
	attrs []any
}

var _ gas.LoggerContext = (*slogContext)(nil)

// Str adds a string field.
func (c *slogContext) Str(key, val string) gas.LoggerContext {
	return c.add(slog.String(key, val))
}

// Int adds an int field.
func (c *slogContext) Int(key string, val int) gas.LoggerContext {
	return c.add(slog.Int(key, val))
}

// Int64 adds an int64 field.
func (c *slogContext) Int64(key string, val int64) gas.LoggerContext {
	return c.add(slog.Int64(key, val))
}

// Float64 adds a float64 field.
func (c *slogContext) Float64(key string, val float64) gas.LoggerContext {
	return c.add(slog.Float64(key, val))
}

// Bool adds a bool field.
func (c *slogContext) Bool(key string, val bool) gas.LoggerContext {
	return c.add(slog.Bool(key, val))
}

// Err adds an error field, as slog.Any.
func (c *slogContext) Err(key string, val error) gas.LoggerContext {
	return c.add(slog.Any(key, val))
}

// Duration adds a time.Duration field.
func (c *slogContext) Duration(key string, val time.Duration) gas.LoggerContext {
	return c.add(slog.Duration(key, val))
}

// Any adds a field of any type, as slog.Any.
func (c *slogContext) Any(key string, val any) gas.LoggerContext {
	return c.add(slog.Any(key, val))
}

// Logger returns a logger with the fields added so far. Fields added to the
// context later do not reach it.
func (c *slogContext) Logger() gas.Logger {
	return &SlogLogger{logger: c.logger.With(c.attrs...), flush: c.flush}
}

// add appends one field.
func (c *slogContext) add(a slog.Attr) gas.LoggerContext {
	c.attrs = append(c.attrs, a)

	return c
}

// slogEvent collects the fields of one record until Send.
type slogEvent struct {
	// logger receives the record.
	logger *slog.Logger
	// msg is the record's message.
	msg string
	// attrs are the fields added so far.
	attrs []slog.Attr
	// level is the record's level.
	level slog.Level
	// sent is set by Send; every method is a no-op after it.
	sent bool
}

var _ gas.LogEvent = (*slogEvent)(nil)

// Str adds a string field.
func (e *slogEvent) Str(key, val string) gas.LogEvent { return e.add(slog.String(key, val)) }

// Int adds an int field.
func (e *slogEvent) Int(key string, val int) gas.LogEvent { return e.add(slog.Int(key, val)) }

// Int64 adds an int64 field.
func (e *slogEvent) Int64(key string, val int64) gas.LogEvent {
	return e.add(slog.Int64(key, val))
}

// Float64 adds a float64 field.
func (e *slogEvent) Float64(key string, val float64) gas.LogEvent {
	return e.add(slog.Float64(key, val))
}

// Bool adds a bool field.
func (e *slogEvent) Bool(key string, val bool) gas.LogEvent { return e.add(slog.Bool(key, val)) }

// Err adds an error field, as slog.Any.
func (e *slogEvent) Err(key string, val error) gas.LogEvent { return e.add(slog.Any(key, val)) }

// Duration adds a time.Duration field.
func (e *slogEvent) Duration(key string, val time.Duration) gas.LogEvent {
	return e.add(slog.Duration(key, val))
}

// Any adds a field of any type, as slog.Any.
func (e *slogEvent) Any(key string, val any) gas.LogEvent { return e.add(slog.Any(key, val)) }

// Send hands the record to the handler, with the program counter of its
// caller, so a handler's source is the line that called Send. The handler's
// error is discarded, as slog.Logger discards it. Send does nothing the
// second time.
func (e *slogEvent) Send() {
	if e.sent {
		return
	}

	e.sent = true

	var pcs [1]uintptr

	runtime.Callers(callersSkip, pcs[:])

	r := slog.NewRecord(time.Now(), e.level, e.msg, pcs[0])
	r.AddAttrs(e.attrs...)

	// slog.Logger discards the handler's error too; the event has no way to
	// return it, as described in the package documentation.
	_ = e.logger.Handler().Handle(context.Background(), r)

	e.attrs = nil
}

// add appends one field, unless the event was sent.
func (e *slogEvent) add(a slog.Attr) gas.LogEvent {
	if !e.sent {
		e.attrs = append(e.attrs, a)
	}

	return e
}
