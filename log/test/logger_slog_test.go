package log_test

import (
	"bytes"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/log"
)

// slogLogger returns a SlogLogger writing JSON to buf at every level.
func slogLogger(t *testing.T, buf io.Writer, source bool) *log.SlogLogger {
	t.Helper()

	l, err := log.NewSlogLogger(log.WithSlogLogger(jsonLogger(buf, source)))()
	require.NoError(t, err)

	return l
}

// Each level writes at its slog level; Trace is LevelTrace, below Debug.
func TestSlogLogger_Levels(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	l := slogLogger(t, &buf, false)

	l.Trace("t").Send()
	l.Debug("d").Send()
	l.Info("i").Send()
	l.Warn("w").Send()
	l.Error("e").Send()

	lines := buf.lines(t)
	levels := make([]any, 0, len(lines))

	for _, line := range lines {
		levels = append(levels, line["level"])
	}

	assert.Equal(t, []any{"DEBUG-4", "DEBUG", "INFO", "WARN", "ERROR"}, levels)
	assert.Equal(t, slog.LevelDebug-4, log.LevelTrace)
}

// Every field method writes its field, with the documented encodings.
func TestSlogLogger_Fields(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	slogLogger(t, &buf, false).Info("m").
		Str("s", "v").Int("i", 1).Int64("i64", 2).Float64("f", 1.5).Bool("b", true).
		Err("nil", nil).Err("err", errBoom).Any("anyerr", errBang).
		Duration("d", 1500*time.Millisecond).Any("a", map[string]int{"k": 3}).
		Send()

	line := buf.lines(t)[0]
	assert.Equal(t, "v", line["s"])
	assert.InDelta(t, 1, line["i"], 0)
	assert.InDelta(t, 2, line["i64"], 0)
	assert.InDelta(t, 1.5, line["f"], 0)
	assert.Equal(t, true, line["b"])
	assert.Contains(t, line, "nil")
	assert.Nil(t, line["nil"])
	assert.Equal(t, "boom", line["err"])
	assert.Equal(t, "bang", line["anyerr"])
	assert.InDelta(t, 1.5e9, line["d"], 0)
	assert.Equal(t, map[string]any{"k": float64(3)}, line["a"])
}

// A level the handler does not enable gives the shared no-op event, so
// it costs no allocation and writes nothing.
//
// Not parallel: it measures allocations.
func TestSlogLogger_DisabledLevel(t *testing.T) {
	var buf bytes.Buffer

	l, err := log.NewSlogLogger(log.WithSlogLogger(slog.New(slog.NewJSONHandler(&buf, nil))))()
	require.NoError(t, err)

	allocs := testing.AllocsPerRun(100, func() {
		l.Debug("d").Str("a", "b").Int("i", 1).Send()
	})

	assert.Zero(t, allocs)
	assert.Empty(t, buf.String())
	assert.Same(t, (&gas.NopLogger{}).Info(""), l.Trace("t"))
}

// Without WithSlogLogger, the logger writes to slog.Default() as it was
// when the constructor ran.
//
// Not parallel: it sets the default slog logger.
func TestNewSlogLogger_Default(t *testing.T) {
	prev := slog.Default()

	t.Cleanup(func() { slog.SetDefault(prev) })

	var buf lockedBuffer

	slog.SetDefault(jsonLogger(&buf, false))

	l, err := log.NewSlogLogger()()
	require.NoError(t, err)

	slog.SetDefault(prev)
	l.Info("to default").Send()

	assert.Equal(t, "to default", buf.lines(t)[0]["msg"])
}

// WithSlogLogger(nil) fails the constructor with ErrNilLogger.
func TestNewSlogLogger_Nil(t *testing.T) {
	t.Parallel()

	l, err := log.NewSlogLogger(log.WithSlogLogger(nil))()
	require.ErrorIs(t, err, log.ErrNilLogger)
	require.EqualError(t, err, "gas/log: logger is nil")
	assert.Nil(t, l)
}

// An event is sent at most once; methods after Send do nothing.
func TestSlogLogger_SendOnce(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	evt := slogLogger(t, &buf, false).Info("once").Str("a", "1")
	evt.Send()
	evt.Str("late", "x").Int("i", 1).Send()

	lines := buf.lines(t)
	require.Len(t, lines, 1)
	assert.NotContains(t, lines[0], "late")
}

// The source slog's AddSource reports is the line that called Send.
func TestSlogLogger_Source(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	slogLogger(t, &buf, true).Info("src").Send() // the line reported

	source, _ := buf.lines(t)[0]["source"].(map[string]any)
	file, _ := source["file"].(string)
	assert.Equal(t, "logger_slog_test.go", filepath.Base(file))
	assert.Contains(t, source["function"], "TestSlogLogger_Source")
}

// A logger from With carries its fields before the event's own and leaves
// its parent alone; Logger again on the same context, after more fields,
// gives a second logger with them all and leaves the first alone.
func TestSlogLogger_With(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	parent := slogLogger(t, &buf, false)
	ctx := parent.With().Str("s", "v").Int("i", 1).Int64("i64", 2).Float64("f", 0.5).
		Bool("b", true).Err("e", errBoom).Duration("d", time.Second).Any("a", "x")
	first := ctx.Logger()
	ctx.Str("more", "y")

	second := ctx.Logger()

	first.Info("first").Send()
	second.Info("second").Send()
	parent.Info("parent").Send()

	lines := buf.lines(t)
	require.Len(t, lines, 3)
	assert.Equal(t, "v", lines[0]["s"])
	assert.Equal(t, "boom", lines[0]["e"])
	assert.NotContains(t, lines[0], "more")
	assert.Equal(t, "y", lines[1]["more"])
	assert.NotContains(t, lines[2], "s")

	first.Flush()
	parent.Flush()
}

// Send discards the handler's error, as slog.Logger does.
func TestSlogLogger_HandlerError(t *testing.T) {
	t.Parallel()

	l, err := log.NewSlogLogger(log.WithSlogLogger(slog.New(failingHandler{err: errFake})))()
	require.NoError(t, err)

	assert.NotPanics(t, func() { l.Error("e").Send() })
}

// Many goroutines log through one logger and its sub-loggers.
func TestSlogLogger_Concurrent(t *testing.T) {
	t.Parallel()

	var (
		buf lockedBuffer
		wg  sync.WaitGroup
	)

	l := slogLogger(t, &buf, false)

	for i := range 8 {
		wg.Go(func() {
			sub := l.With().Int("g", i).Logger()
			for j := range 50 {
				sub.Info("c").Int("j", j).Send()
			}
		})
	}

	wg.Wait()

	assert.Len(t, buf.lines(t), 400)
}
