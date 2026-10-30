package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gasmod/gas"
)

// lockedBuffer is a bytes.Buffer safe for concurrent writes from a
// slog.Handler.
type lockedBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) lines() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err == nil {
			out = append(out, rec)
		}
	}
	return out
}

// jsonLogger is a gas.Logger writing JSON lines through slog's JSON
// handler, the shape the tests read back with lockedBuffer.lines. Trace
// writes at slog's Debug.
type jsonLogger struct {
	logger *slog.Logger
}

// newJSONLogger returns a jsonLogger writing to w.
func newJSONLogger(w io.Writer) *jsonLogger {
	return &jsonLogger{logger: slog.New(slog.NewJSONHandler(w, nil))}
}

// event starts an event at level.
func (l *jsonLogger) event(level slog.Level, msg string) gas.LogEvent {
	return &jsonEvent{logger: l.logger, level: level, msg: msg}
}

// Trace starts an event at slog.LevelDebug.
func (l *jsonLogger) Trace(msg string) gas.LogEvent { return l.event(slog.LevelDebug, msg) }

// Debug starts an event at slog.LevelDebug.
func (l *jsonLogger) Debug(msg string) gas.LogEvent { return l.event(slog.LevelDebug, msg) }

// Info starts an event at slog.LevelInfo.
func (l *jsonLogger) Info(msg string) gas.LogEvent { return l.event(slog.LevelInfo, msg) }

// Warn starts an event at slog.LevelWarn.
func (l *jsonLogger) Warn(msg string) gas.LogEvent { return l.event(slog.LevelWarn, msg) }

// Error starts an event at slog.LevelError.
func (l *jsonLogger) Error(msg string) gas.LogEvent { return l.event(slog.LevelError, msg) }

// Flush does nothing: each event is written on Send.
func (l *jsonLogger) Flush() {}

// With returns a context collecting fields for a child logger.
func (l *jsonLogger) With() gas.LoggerContext { return &jsonContext{logger: l.logger} }

// jsonContext collects the fields of a jsonLogger from With.
type jsonContext struct {
	logger *slog.Logger
	attrs  []any
}

// add appends a and returns c.
func (c *jsonContext) add(a slog.Attr) gas.LoggerContext { c.attrs = append(c.attrs, a); return c }

// Str adds a string field.
func (c *jsonContext) Str(k, v string) gas.LoggerContext { return c.add(slog.String(k, v)) }

// Int adds an int field.
func (c *jsonContext) Int(k string, v int) gas.LoggerContext { return c.add(slog.Int(k, v)) }

// Int64 adds an int64 field.
func (c *jsonContext) Int64(k string, v int64) gas.LoggerContext { return c.add(slog.Int64(k, v)) }

// Float64 adds a float64 field.
func (c *jsonContext) Float64(k string, v float64) gas.LoggerContext {
	return c.add(slog.Float64(k, v))
}

// Bool adds a bool field.
func (c *jsonContext) Bool(k string, v bool) gas.LoggerContext { return c.add(slog.Bool(k, v)) }

// Err adds an error field.
func (c *jsonContext) Err(k string, v error) gas.LoggerContext { return c.add(slog.Any(k, v)) }

// Any adds a field of any value.
func (c *jsonContext) Any(k string, v any) gas.LoggerContext { return c.add(slog.Any(k, v)) }

// Duration adds a duration field.
func (c *jsonContext) Duration(k string, v time.Duration) gas.LoggerContext {
	return c.add(slog.Duration(k, v))
}

// Logger returns a jsonLogger carrying the fields added so far.
func (c *jsonContext) Logger() gas.Logger { return &jsonLogger{logger: c.logger.With(c.attrs...)} }

// jsonEvent collects the fields of one record until Send, which writes it
// once; every call after Send does nothing.
type jsonEvent struct {
	logger *slog.Logger
	msg    string
	attrs  []slog.Attr
	level  slog.Level
	sent   bool
}

// add appends a unless the event was sent, and returns e.
func (e *jsonEvent) add(a slog.Attr) gas.LogEvent {
	if !e.sent {
		e.attrs = append(e.attrs, a)
	}
	return e
}

// Str adds a string field.
func (e *jsonEvent) Str(k, v string) gas.LogEvent { return e.add(slog.String(k, v)) }

// Int adds an int field.
func (e *jsonEvent) Int(k string, v int) gas.LogEvent { return e.add(slog.Int(k, v)) }

// Int64 adds an int64 field.
func (e *jsonEvent) Int64(k string, v int64) gas.LogEvent { return e.add(slog.Int64(k, v)) }

// Float64 adds a float64 field.
func (e *jsonEvent) Float64(k string, v float64) gas.LogEvent {
	return e.add(slog.Float64(k, v))
}

// Bool adds a bool field.
func (e *jsonEvent) Bool(k string, v bool) gas.LogEvent { return e.add(slog.Bool(k, v)) }

// Err adds an error field.
func (e *jsonEvent) Err(k string, v error) gas.LogEvent { return e.add(slog.Any(k, v)) }

// Any adds a field of any value.
func (e *jsonEvent) Any(k string, v any) gas.LogEvent { return e.add(slog.Any(k, v)) }

// Duration adds a duration field.
func (e *jsonEvent) Duration(k string, v time.Duration) gas.LogEvent {
	return e.add(slog.Duration(k, v))
}

// Send writes the record, the first time only.
func (e *jsonEvent) Send() {
	if e.sent {
		return
	}
	e.sent = true
	e.logger.LogAttrs(context.Background(), e.level, e.msg, e.attrs...)
}
