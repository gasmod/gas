package zerolog_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	rz "github.com/rs/zerolog"
	rzlog "github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/log"
	gaszerolog "github.com/gasmod/gas/log/zerolog"
)

// The errors of the field tests.
var (
	errBoom = errors.New("boom")
	errBang = errors.New("bang")
)

// lockedBuffer is a bytes.Buffer safe for concurrent use.
type lockedBuffer struct {
	// buf holds what was written.
	buf bytes.Buffer
	// mu guards buf.
	mu sync.Mutex
}

// Write appends p.
func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

// lines decodes each line written as a JSON object.
func (b *lockedBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()

	b.mu.Lock()
	defer b.mu.Unlock()

	var out []map[string]any

	for line := range strings.Lines(b.buf.String()) {
		var m map[string]any

		require.NoError(t, json.Unmarshal([]byte(line), &m), line)

		out = append(out, m)
	}

	return out
}

// newLogger returns a Logger over zl.
func newLogger(t *testing.T, zl rz.Logger) *gaszerolog.Logger {
	t.Helper()

	l, err := gaszerolog.New(gaszerolog.WithLogger(&zl))()
	require.NoError(t, err)

	return l
}

// The example in the package documentation's Usage section, writing to a
// buffer.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	zl := rz.New(&buf).With().Timestamp().Logger()

	logger, err := gaszerolog.New(gaszerolog.WithLogger(&zl))()
	require.NoError(t, err)

	logger.Info("order placed").Str("order", "A-1").Int("items", 3).Send()

	line := buf.lines(t)[0]
	assert.Equal(t, "info", line["level"])
	assert.Contains(t, line, "time")
	assert.Equal(t, "A-1", line["order"])
	assert.InDelta(t, 3, line["items"], 0)
	assert.Equal(t, "order placed", line["message"])
}

// The error messages the package documentation lists are exactly the one
// New's constructor returns.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	l, err := gaszerolog.New(gaszerolog.WithLogger(nil))()
	require.ErrorIs(t, err, gaszerolog.ErrNilLogger)
	require.ErrorIs(t, err, log.ErrNilLogger)
	require.EqualError(t, err, "gas/log: logger is nil")
	assert.Nil(t, l)

	doc, readErr := os.ReadFile("../doc.go")
	require.NoError(t, readErr)

	// Every error the package documentation lists is one the package
	// returns, and it lists every one.
	var documented []string

	for line := range strings.Lines(string(doc)) {
		code, found := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "//\t")
		if found && strings.HasPrefix(code, "gas/log: ") {
			documented = append(documented, code)
		}
	}

	assert.ElementsMatch(t, []string{err.Error()}, documented)
}

// Without WithLogger, the logger writes to zerolog's global logger, read
// through a pointer.
//
// Not parallel: it replaces zerolog's global logger.
func TestNew_Default(t *testing.T) {
	prev := rzlog.Logger

	t.Cleanup(func() { rzlog.Logger = prev })

	var buf lockedBuffer

	l, err := gaszerolog.New()()
	require.NoError(t, err)

	rzlog.Logger = rz.New(&buf)

	l.Info("global").Send()

	assert.Equal(t, "global", buf.lines(t)[0]["message"])
}

// Each level writes at zerolog's level of the same name, Trace included.
func TestLogger_Levels(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	l := newLogger(t, rz.New(&buf).Level(rz.TraceLevel))

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

	assert.Equal(t, []any{"trace", "debug", "info", "warn", "error"}, levels)
}

// Every field method writes its field with zerolog's encoding: a nil
// error is left out, an error in Any is its text, a duration is in
// milliseconds.
func TestLogger_Fields(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	newLogger(t, rz.New(&buf)).Info("m").
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
	assert.NotContains(t, line, "nil")
	assert.Equal(t, "boom", line["err"])
	assert.Equal(t, "bang", line["anyerr"])
	assert.InDelta(t, 1500, line["d"], 0)
	assert.Equal(t, map[string]any{"k": float64(3)}, line["a"])
}

// A level the logger does not enable gives the shared no-op event, which
// allocates nothing and writes nothing.
//
// Not parallel: it measures allocations.
func TestLogger_DisabledLevel(t *testing.T) {
	l := newLogger(t, rz.New(io.Discard).Level(rz.InfoLevel))

	allocs := testing.AllocsPerRun(100, func() {
		l.Debug("d").Str("a", "b").Int("i", 1).Send()
	})

	assert.Zero(t, allocs)
	assert.Same(t, (&gas.NopLogger{}).Info(""), l.Trace("t"))
}

// An event is sent at most once, and a method called after Send cannot
// write into the event zerolog hands out next from its pool.
func TestLogger_SendOnce(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	l := newLogger(t, rz.New(&buf))

	old := l.Info("one")
	old.Send()

	next := l.Info("two")

	old.Str("leak", "from old").Int("i", 1).Int64("j", 2).Float64("f", 1).Bool("b", true).
		Err("e", errBoom).Duration("d", time.Second).Any("a", 1).Send()
	next.Str("own", "x").Send()

	lines := buf.lines(t)
	require.Len(t, lines, 2)
	assert.Equal(t, map[string]any{"level": "info", "message": "two", "own": "x"}, lines[1])
}

// With zerolog's Caller on the logger, the caller written is the line
// that called Send.
func TestLogger_Caller(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	newLogger(t, rz.New(&buf).With().Caller().Logger()).Info("src").Send()

	caller, _ := buf.lines(t)[0]["caller"].(string)
	assert.Equal(t, "logger_test.go", filepath.Base(caller[:strings.LastIndex(caller, ":")]))
}

// A logger from With carries its fields and leaves its parent alone;
// Logger again on the same context, after more fields, gives a second
// logger with them all and leaves the first alone.
func TestLogger_With(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	parent := newLogger(t, rz.New(&buf))
	ctx := parent.With().Str("s", "v").Int("i", 1).Int64("i64", 2).Float64("f", 0.5).
		Bool("b", true).Err("e", errBoom).Err("nil", nil).Duration("d", time.Second).
		Any("a", "x").Any("ae", errBang)
	first := ctx.Logger()
	ctx.Str("more", "y")

	second := ctx.Logger()

	first.Info("first").Send()
	second.Info("second").Send()
	parent.Info("parent").Send()

	lines := buf.lines(t)
	require.Len(t, lines, 3)
	assert.Equal(t, "boom", lines[0]["e"])
	assert.Equal(t, "bang", lines[0]["ae"])
	assert.NotContains(t, lines[0], "nil")
	assert.NotContains(t, lines[0], "more")
	assert.Equal(t, "y", lines[1]["more"])
	assert.NotContains(t, lines[2], "s")

	first.Flush()
}

// Many goroutines log through one logger and its sub-loggers.
func TestLogger_Concurrent(t *testing.T) {
	t.Parallel()

	var (
		buf lockedBuffer
		wg  sync.WaitGroup
	)

	l := newLogger(t, rz.New(&buf))

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
