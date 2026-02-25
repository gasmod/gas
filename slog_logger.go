package gas

import (
	"context"
	"log/slog"
	"time"
)

type slogLogger struct {
	logger *slog.Logger
}

func newSlogLogger(logger *slog.Logger) *slogLogger {
	return &slogLogger{logger: logger}
}

func (l *slogLogger) Trace(msg string) LogEvent {
	return &slogLogEvent{logger: l.logger, lvl: slog.LevelDebug, msg: msg, attrs: make([]slog.Attr, 0)}
}

func (l *slogLogger) Debug(msg string) LogEvent {
	return &slogLogEvent{logger: l.logger, lvl: slog.LevelDebug, msg: msg, attrs: make([]slog.Attr, 0)}
}

func (l *slogLogger) Info(msg string) LogEvent {
	return &slogLogEvent{logger: l.logger, lvl: slog.LevelInfo, msg: msg, attrs: make([]slog.Attr, 0)}
}

func (l *slogLogger) Warn(msg string) LogEvent {
	return &slogLogEvent{logger: l.logger, lvl: slog.LevelWarn, msg: msg, attrs: make([]slog.Attr, 0)}
}

func (l *slogLogger) Error(msg string) LogEvent {
	return &slogLogEvent{logger: l.logger, lvl: slog.LevelError, msg: msg, attrs: make([]slog.Attr, 0)}
}

func (l *slogLogger) Flush() {}

func (l *slogLogger) With() LoggerContext {
	return &slogLoggerContext{logger: l.logger, attrs: make([]any, 0)}
}

func (l *slogLogger) SetBaseFields() MutableLoggerContext {
	return &slogMutableLoggerContext{target: l, attrs: make([]any, 0)}
}

type slogLoggerContext struct {
	logger *slog.Logger
	attrs  []any
}

func (c *slogLoggerContext) Str(key, val string) LoggerContext {
	c.attrs = append(c.attrs, slog.String(key, val))
	return c
}

func (c *slogLoggerContext) Int(key string, val int) LoggerContext {
	c.attrs = append(c.attrs, slog.Int(key, val))
	return c
}

func (c *slogLoggerContext) Int64(key string, val int64) LoggerContext {
	c.attrs = append(c.attrs, slog.Int64(key, val))
	return c
}

func (c *slogLoggerContext) Float64(key string, val float64) LoggerContext {
	c.attrs = append(c.attrs, slog.Float64(key, val))
	return c
}

func (c *slogLoggerContext) Bool(key string, val bool) LoggerContext {
	c.attrs = append(c.attrs, slog.Bool(key, val))
	return c
}

func (c *slogLoggerContext) Err(key string, val error) LoggerContext {
	c.attrs = append(c.attrs, slog.Any(key, val))
	return c
}

func (c *slogLoggerContext) Duration(key string, val time.Duration) LoggerContext {
	c.attrs = append(c.attrs, slog.Duration(key, val))
	return c
}

func (c *slogLoggerContext) Any(key string, val any) LoggerContext {
	c.attrs = append(c.attrs, slog.Any(key, val))
	return c
}

func (c *slogLoggerContext) Logger() Logger {
	return &slogLogger{logger: c.logger.With(c.attrs...)}
}

type slogMutableLoggerContext struct {
	target *slogLogger
	attrs  []any
}

func (c *slogMutableLoggerContext) Str(key, val string) MutableLoggerContext {
	c.attrs = append(c.attrs, slog.String(key, val))
	return c
}

func (c *slogMutableLoggerContext) Int(key string, val int) MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Int(key, val))
	return c
}

func (c *slogMutableLoggerContext) Int64(key string, val int64) MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Int64(key, val))
	return c
}

func (c *slogMutableLoggerContext) Float64(key string, val float64) MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Float64(key, val))
	return c
}

func (c *slogMutableLoggerContext) Bool(key string, val bool) MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Bool(key, val))
	return c
}

func (c *slogMutableLoggerContext) Err(key string, val error) MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Any(key, val))
	return c
}

func (c *slogMutableLoggerContext) Duration(key string, val time.Duration) MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Duration(key, val))
	return c
}

func (c *slogMutableLoggerContext) Any(key string, val any) MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Any(key, val))
	return c
}

func (c *slogMutableLoggerContext) Apply() {
	c.target.logger = c.target.logger.With(c.attrs...)
}

type slogLogEvent struct {
	logger *slog.Logger
	msg    string
	attrs  []slog.Attr
	lvl    slog.Level
}

func (e *slogLogEvent) Str(key, value string) LogEvent {
	e.attrs = append(e.attrs, slog.String(key, value))
	return e
}

func (e *slogLogEvent) Int(key string, value int) LogEvent {
	e.attrs = append(e.attrs, slog.Int(key, value))
	return e
}

func (e *slogLogEvent) Int64(key string, value int64) LogEvent {
	e.attrs = append(e.attrs, slog.Int64(key, value))
	return e
}

func (e *slogLogEvent) Float64(key string, value float64) LogEvent {
	e.attrs = append(e.attrs, slog.Float64(key, value))
	return e
}

func (e *slogLogEvent) Bool(key string, value bool) LogEvent {
	e.attrs = append(e.attrs, slog.Bool(key, value))
	return e
}

func (e *slogLogEvent) Err(key string, value error) LogEvent {
	e.attrs = append(e.attrs, slog.Any(key, value))
	return e
}

func (e *slogLogEvent) Duration(key string, value time.Duration) LogEvent {
	e.attrs = append(e.attrs, slog.Duration(key, value))
	return e
}

func (e *slogLogEvent) Any(key string, value any) LogEvent {
	e.attrs = append(e.attrs, slog.Any(key, value))
	return e
}

func (e *slogLogEvent) Send() {
	e.logger.LogAttrs(context.Background(), e.lvl, e.msg, e.attrs...)
}
