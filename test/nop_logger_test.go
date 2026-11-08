package gas_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gasmod/gas"
)

// A NopLogger's events and contexts chain and discard everything, and
// the logger from a context is a NopLogger.
func TestNopLogger_Discards(t *testing.T) {
	t.Parallel()

	l := &gas.NopLogger{}

	for _, ev := range []gas.LogEvent{l.Trace("t"), l.Debug("d"), l.Info("i"), l.Warn("w"), l.Error("e")} {
		ev.Str("s", "v").Int("i", 1).Int64("i64", 2).Float64("f", 0.5).Bool("b", true).
			Err("err", errX).Duration("d", time.Second).Any("a", struct{}{}).Send()
	}

	sub := l.With().Str("s", "v").Int("i", 1).Int64("i64", 2).Float64("f", 0.5).Bool("b", true).
		Err("err", errX).Duration("d", time.Second).Any("a", 1).Logger()
	assert.IsType(t, &gas.NopLogger{}, sub)

	l.Flush()
}
