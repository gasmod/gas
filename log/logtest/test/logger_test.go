package logtest_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/log/logtest"
)

// errBoom is the error of the field tests.
var errBoom = errors.New("boom")

// The example in the package documentation's Usage section, with a
// service that logs one warning.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	run := func(l gas.Logger) { l.Warn("slow").Int("ms", 900).Send() }

	logger := logtest.New()

	run(logger)

	for _, e := range logger.Entries() {
		if e.Level == "error" {
			t.Errorf("unexpected error: %s %v", e.Msg, e.Fields)
		}
	}

	assert.Equal(
		t,
		[]logtest.Entry{{Level: "warn", Msg: "slow", Fields: map[string]any{"ms": 900}}},
		logger.Entries(),
	)
}

// Each level records its name, and every field method its value as given.
func TestRecordingLogger_LevelsAndFields(t *testing.T) {
	t.Parallel()

	var l logtest.RecordingLogger

	l.Trace("t").Send()
	l.Debug("d").Send()
	l.Info("i").Send()
	l.Warn("w").Send()
	l.Error("e").Str("s", "v").Int("i", 1).Int64("i64", 2).Float64("f", 0.5).Bool("b", true).
		Err("err", errBoom).Duration("d", time.Second).Any("a", []int{1}).Str("s", "last").Send()
	l.Flush()

	entries := l.Entries()
	require.Len(t, entries, 5)

	levels := make([]string, 0, len(entries))

	for _, e := range entries {
		levels = append(levels, e.Level)
	}

	assert.Equal(t, []string{"trace", "debug", "info", "warn", "error"}, levels)
	assert.Equal(t, map[string]any{
		"s": "last", "i": 1, "i64": int64(2), "f": 0.5, "b": true,
		"err": errBoom, "d": time.Second, "a": []int{1},
	}, entries[4].Fields)
}

// An event is recorded at most once; methods after Send do nothing.
func TestRecordingLogger_SendOnce(t *testing.T) {
	t.Parallel()

	l := logtest.New()
	evt := l.Info("once")

	evt.Send()
	evt.Str("late", "x").Send()

	entries := l.Entries()
	require.Len(t, entries, 1)
	assert.Empty(t, entries[0].Fields)
}

// Entries returns a copy, field maps included.
func TestRecordingLogger_EntriesCopy(t *testing.T) {
	t.Parallel()

	l := logtest.New()
	l.Info("x").Str("k", "v").Send()

	first := l.Entries()

	first[0].Fields["k"] = "changed"
	first[0].Msg = "changed"

	assert.Equal(t, "v", l.Entries()[0].Fields["k"])
	assert.Equal(t, "x", l.Entries()[0].Msg)
}

// A logger from With shares the entries, puts its fields before the
// event's own, and leaves the first logger of a context alone when more
// fields are added; a logger from that logger's With keeps them.
func TestRecordingLogger_With(t *testing.T) {
	t.Parallel()

	l := logtest.New()
	ctx := l.With().Str("s", "v").Int("i", 1).Int64("i64", 2).Float64("f", 0.5).Bool("b", true).
		Err("err", errBoom).Duration("d", time.Second).Any("k", "ctx")
	first := ctx.Logger()

	ctx.Str("more", "y")

	second := ctx.Logger()
	nested := second.With().Str("n", "1").Logger()

	first.Info("first").Any("k", "evt").Send()
	second.Info("second").Send()
	nested.Info("nested").Send()
	l.Info("root").Send()

	entries := nested.(*logtest.RecordingLogger).Entries()
	require.Len(t, entries, 4)
	assert.Equal(t, map[string]any{
		"s": "v", "i": 1, "i64": int64(2), "f": 0.5, "b": true,
		"err": errBoom, "d": time.Second, "k": "evt",
	}, entries[0].Fields)
	assert.Equal(t, "y", entries[1].Fields["more"])
	assert.Equal(t, "1", entries[2].Fields["n"])
	assert.Equal(t, "y", entries[2].Fields["more"])
	assert.Empty(t, entries[3].Fields)
	assert.Equal(t, entries, l.Entries())

	empty := l.With().Logger()
	empty.Info("e").Send()
	assert.Len(t, l.Entries(), 5)
}

// Many goroutines record through one logger and its sub-loggers.
func TestRecordingLogger_Concurrent(t *testing.T) {
	t.Parallel()

	var wg sync.WaitGroup

	l := logtest.New()

	for i := range 8 {
		wg.Go(func() {
			sub := l.With().Int("g", i).Logger()
			for j := range 50 {
				sub.Info("c").Int("j", j).Send()

				_ = l.Entries()
			}
		})
	}

	wg.Wait()

	assert.Len(t, l.Entries(), 400)
}
