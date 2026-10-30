package testutil_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/internal/testutil"
)

// errLogged is an error field value.
var errLogged = errors.New("logged")

// The zero RecordingLogger records every event it sends, in order, with
// its level, message and fields; an event that is never sent is not
// recorded.
func TestRecordingLogger_Records(t *testing.T) {
	t.Parallel()

	var l testutil.RecordingLogger

	l.Trace("t").Str("s", "v").Send()
	l.Debug("d").Int("i", 1).Int64("i64", 2).Send()
	l.Info("i").Float64("f", 0.5).Bool("b", true).Send()
	l.Warn("w").Err("error", errLogged).Duration("d", time.Second).Send()
	l.Error("e").Any("a", []int{1}).Send()
	l.Info("unsent").Str("s", "v")
	l.Flush()

	entries := l.Entries()
	require.Len(t, entries, 5)

	levels := make([]string, 0, len(entries))
	for _, e := range entries {
		levels = append(levels, e.Level)
	}

	assert.Equal(t, []string{"trace", "debug", "info", "warn", "error"}, levels)
	assert.Equal(t, map[string]any{"s": "v"}, entries[0].Fields)
	assert.Equal(t, map[string]any{"i": 1, "i64": int64(2)}, entries[1].Fields)
	assert.Equal(t, map[string]any{"f": 0.5, "b": true}, entries[2].Fields)
	assert.Equal(t, map[string]any{"error": errLogged, "d": time.Second}, entries[3].Fields)
	assert.Equal(t, map[string]any{"a": []int{1}}, entries[4].Fields)
	assert.Equal(t, "e", entries[4].Msg)
}

// A logger from With records into the same entries, and each of its
// events starts with the context's fields; a field the event sets itself
// replaces one of the same key.
func TestRecordingLogger_With(t *testing.T) {
	t.Parallel()

	var l testutil.RecordingLogger

	child := l.With().
		Str("service", "session").
		Int("i", 1).
		Int64("i64", 2).
		Float64("f", 0.5).
		Bool("b", true).
		Err("cause", errLogged).
		Duration("d", time.Second).
		Any("a", "x").
		Logger()

	l.Info("parent").Send()
	child.Warn("child").Str("i", "own").Send()

	want := map[string]any{
		"service": "session", "i": "own", "i64": int64(2), "f": 0.5, "b": true,
		"cause": errLogged, "d": time.Second, "a": "x",
	}

	for _, entries := range [][]testutil.Entry{l.Entries(), child.(*testutil.RecordingLogger).Entries()} {
		require.Len(t, entries, 2)
		assert.Equal(t, "parent", entries[0].Msg)
		assert.Empty(t, entries[0].Fields)
		assert.Equal(t, "child", entries[1].Msg)
		assert.Equal(t, "warn", entries[1].Level)
		assert.Equal(t, want, entries[1].Fields)
	}
}

// A logger from With on a logger from With carries the fields of both,
// the inner ones replacing outer ones of the same key, and still records
// into the first logger's entries. Fields added to a context after its
// Logger was called do not reach that logger.
func TestRecordingLogger_WithNested(t *testing.T) {
	t.Parallel()

	var l testutil.RecordingLogger

	ctx := l.With().Str("service", "token").Str("k", "outer")
	outer := ctx.Logger()
	ctx.Str("late", "x")

	inner := outer.With().Str("k", "inner").Logger()

	outer.Info("outer").Send()
	inner.Info("inner").Send()

	entries := l.Entries()
	require.Len(t, entries, 2)
	assert.Equal(t, map[string]any{"service": "token", "k": "outer"}, entries[0].Fields)
	assert.Equal(t, map[string]any{"service": "token", "k": "inner"}, entries[1].Fields)
}

// A RecordingLogger and the loggers from its With may send from several
// goroutines at once.
func TestRecordingLogger_Concurrent(t *testing.T) {
	t.Parallel()

	var (
		l  testutil.RecordingLogger
		wg sync.WaitGroup
	)

	child := l.With().Str("service", "apikey").Logger()

	for range 4 {
		wg.Go(func() {
			for range 25 {
				l.Info("parent").Send()
				child.Info("child").Send()

				_ = l.Entries()
			}
		})
	}

	wg.Wait()
	assert.Len(t, l.Entries(), 200)
}
