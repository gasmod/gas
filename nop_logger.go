package gas

import "time"

// NopLogger is a Logger that discards everything. Its zero value is ready
// to use, and its events and contexts are shared values, so logging
// through it allocates nothing.
type NopLogger struct{}

var _ Logger = (*NopLogger)(nil)

// Trace returns an event that discards its fields.
func (*NopLogger) Trace(string) LogEvent { return nopEvent }

// Debug returns an event that discards its fields.
func (*NopLogger) Debug(string) LogEvent { return nopEvent }

// Info returns an event that discards its fields.
func (*NopLogger) Info(string) LogEvent { return nopEvent }

// Warn returns an event that discards its fields.
func (*NopLogger) Warn(string) LogEvent { return nopEvent }

// Error returns an event that discards its fields.
func (*NopLogger) Error(string) LogEvent { return nopEvent }

// Flush does nothing.
func (*NopLogger) Flush() {}

// With returns a context that discards its fields and whose Logger is a
// NopLogger.
func (*NopLogger) With() LoggerContext { return nopContext }

// nopLoggerContext is the LoggerContext of a NopLogger: every method
// returns the receiver, and Logger returns a NopLogger.
type nopLoggerContext struct{}

// nopContext is the one nopLoggerContext every NopLogger returns.
var nopContext = &nopLoggerContext{}

var _ LoggerContext = (*nopLoggerContext)(nil)

// Str returns c.
func (c *nopLoggerContext) Str(string, string) LoggerContext { return c }

// Int returns c.
func (c *nopLoggerContext) Int(string, int) LoggerContext { return c }

// Int64 returns c.
func (c *nopLoggerContext) Int64(string, int64) LoggerContext { return c }

// Float64 returns c.
func (c *nopLoggerContext) Float64(string, float64) LoggerContext { return c }

// Bool returns c.
func (c *nopLoggerContext) Bool(string, bool) LoggerContext { return c }

// Err returns c.
func (c *nopLoggerContext) Err(string, error) LoggerContext { return c }

// Duration returns c.
func (c *nopLoggerContext) Duration(string, time.Duration) LoggerContext { return c }

// Any returns c.
func (c *nopLoggerContext) Any(string, any) LoggerContext { return c }

// Logger returns a NopLogger.
func (*nopLoggerContext) Logger() Logger { return &NopLogger{} }

// nopLogEvent is the LogEvent of a NopLogger: every method returns the
// receiver, and Send does nothing.
type nopLogEvent struct{}

// nopEvent is the one nopLogEvent every NopLogger returns.
var nopEvent = &nopLogEvent{}

var _ LogEvent = (*nopLogEvent)(nil)

// Str returns e.
func (e *nopLogEvent) Str(string, string) LogEvent { return e }

// Int returns e.
func (e *nopLogEvent) Int(string, int) LogEvent { return e }

// Int64 returns e.
func (e *nopLogEvent) Int64(string, int64) LogEvent { return e }

// Float64 returns e.
func (e *nopLogEvent) Float64(string, float64) LogEvent { return e }

// Bool returns e.
func (e *nopLogEvent) Bool(string, bool) LogEvent { return e }

// Err returns e.
func (e *nopLogEvent) Err(string, error) LogEvent { return e }

// Duration returns e.
func (e *nopLogEvent) Duration(string, time.Duration) LogEvent { return e }

// Any returns e.
func (e *nopLogEvent) Any(string, any) LogEvent { return e }

// Send does nothing.
func (*nopLogEvent) Send() {}
