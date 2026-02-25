package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/gasmod/gas"
)

type Logger struct {
	logger     *slog.Logger
	instanceId int
}

func NewSlogLogger(logger *slog.Logger) func() gas.Logger {
	return func() gas.Logger { return &Logger{logger: logger, instanceId: 1} }
}

func (l *Logger) Trace(msg string) gas.LogEvent {
	return &LogEvent{logger: l.logger, lvl: slog.LevelDebug, msg: msg}
}

func (l *Logger) Debug(msg string) gas.LogEvent {
	return &LogEvent{logger: l.logger, lvl: slog.LevelDebug, msg: msg}
}

func (l *Logger) Info(msg string) gas.LogEvent {
	return &LogEvent{logger: l.logger, lvl: slog.LevelInfo, msg: msg}
}

func (l *Logger) Warn(msg string) gas.LogEvent {
	return &LogEvent{logger: l.logger, lvl: slog.LevelWarn, msg: msg}
}

func (l *Logger) Error(msg string) gas.LogEvent {
	return &LogEvent{logger: l.logger, lvl: slog.LevelError, msg: msg}
}

func (l *Logger) Flush() {}

func (l *Logger) With() gas.LoggerContext {
	return &LoggerContext{logger: l.logger, attrs: make([]any, 0)}
}

func (l *Logger) SetBaseFields() gas.MutableLoggerContext {
	return &MutableLoggerContext{target: l, attrs: make([]any, 0)}
}

type LoggerContext struct {
	logger *slog.Logger
	attrs  []any
}

func (c *LoggerContext) Str(key, val string) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.String(key, val))
	return c
}

func (c *LoggerContext) Int(key string, val int) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Int(key, val))
	return c
}

func (c *LoggerContext) Int64(key string, val int64) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Int64(key, val))
	return c
}

func (c *LoggerContext) Float64(key string, val float64) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Float64(key, val))
	return c
}

func (c *LoggerContext) Bool(key string, val bool) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Bool(key, val))
	return c
}

func (c *LoggerContext) Err(key string, val error) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Any(key, val))
	return c
}

func (c *LoggerContext) Duration(key string, val time.Duration) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Duration(key, val))
	return c
}

func (c *LoggerContext) Any(key string, val any) gas.LoggerContext {
	c.attrs = append(c.attrs, slog.Any(key, val))
	return c
}

func (c *LoggerContext) Logger() gas.Logger {
	return &Logger{logger: c.logger.With(c.attrs...)}
}

type MutableLoggerContext struct {
	target *Logger
	attrs  []any
}

func (c *MutableLoggerContext) Str(key, val string) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.String(key, val))
	return c
}

func (c *MutableLoggerContext) Int(key string, val int) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Int(key, val))
	return c
}

func (c *MutableLoggerContext) Int64(key string, val int64) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Int64(key, val))
	return c
}

func (c *MutableLoggerContext) Float64(key string, val float64) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Float64(key, val))
	return c
}

func (c *MutableLoggerContext) Bool(key string, val bool) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Bool(key, val))
	return c
}

func (c *MutableLoggerContext) Err(key string, val error) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Any(key, val))
	return c
}

func (c *MutableLoggerContext) Duration(key string, val time.Duration) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Duration(key, val))
	return c
}

func (c *MutableLoggerContext) Any(key string, val any) gas.MutableLoggerContext {
	c.attrs = append(c.attrs, slog.Any(key, val))
	return c
}

func (c *MutableLoggerContext) Apply() {
	c.target.logger = c.target.logger.With(c.attrs...)
}

type LogEvent struct {
	logger *slog.Logger
	msg    string
	attrs  []slog.Attr
	lvl    slog.Level
}

func (e *LogEvent) Str(key string, value string) gas.LogEvent {
	e.attrs = append(e.attrs, slog.String(key, value))
	return e
}

func (e *LogEvent) Int(key string, value int) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Int(key, value))
	return e
}

func (e *LogEvent) Int64(key string, value int64) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Int64(key, value))
	return e
}

func (e *LogEvent) Float64(key string, value float64) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Float64(key, value))
	return e
}

func (e *LogEvent) Bool(key string, value bool) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Bool(key, value))
	return e
}

func (e *LogEvent) Err(key string, value error) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Any(key, value))
	return e
}

func (e *LogEvent) Duration(key string, value time.Duration) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Duration(key, value))
	return e
}

func (e *LogEvent) Any(key string, value any) gas.LogEvent {
	e.attrs = append(e.attrs, slog.Any(key, value))
	return e
}

func (e *LogEvent) Send() {
	e.logger.LogAttrs(context.Background(), e.lvl, e.msg, e.attrs...)
}
