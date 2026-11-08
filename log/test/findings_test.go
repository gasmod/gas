package log_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/log"
)

// KI-042: the exported shipping Handler had only unexported fields and no
// constructor, so a user could not build one, and a zero Handler panicked
// on Enabled. It is gone; NewShippingHandler returns a ShippingHandler
// that is a working slog.Handler with Flush, Close and Dropped.
func TestKI042_ShippingHandlerConstructible(t *testing.T) {
	t.Parallel()

	srv := newCollector(t)

	h, err := log.NewShippingHandler(srv.srv.URL, log.NewOTLPMarshaler(), quiet()...)
	require.NoError(t, err)

	var handler slog.Handler = h

	slog.New(handler).Info("via slog", "k", "v")

	require.NoError(t, h.Flush(t.Context()))
	require.NoError(t, h.Close(t.Context()))
	assert.Zero(t, h.Dropped())

	bodies := srv.posted()
	require.Len(t, bodies, 1)
	assert.Contains(t, string(bodies[0]), `"stringValue":"via slog"`)
}

// A nil Marshaler was accepted and crashed the process on the sender
// goroutine at the first post. The constructors now fail with
// ErrNilMarshaler.
func TestFinding_NilMarshaler(t *testing.T) {
	t.Parallel()

	_, err := log.NewShippingLogger(endpoint, nil)()
	require.ErrorIs(t, err, log.ErrNilMarshaler)

	_, err = log.NewShippingHandler(endpoint, nil)
	require.ErrorIs(t, err, log.ErrNilMarshaler)
}

// One NaN made json.Marshal fail, and the whole OTLP batch was dropped.
// A NaN is now the string "NaN", and the batch is delivered whole.
func TestFinding_NaNKeepsBatch(t *testing.T) {
	t.Parallel()

	srv := newCollector(t)
	l, err := log.NewShippingLogger(srv.srv.URL, log.NewOTLPMarshaler(), quiet()...)()
	require.NoError(t, err)
	require.NoError(t, l.Init(t.Context()))

	for i := range 50 {
		l.Info("ok").Int("i", i).Send()
	}

	l.Info("bad").Float64("f", math.NaN()).Send()
	require.NoError(t, l.Close(context.Background()))

	var records int

	for _, body := range srv.posted() {
		var req otlpRequest

		require.NoError(t, json.Unmarshal(body, &req))

		records += len(logRecords(req))
	}

	assert.Equal(t, 51, records)
	assert.Zero(t, l.Dropped())
}

// The sender reused its batch slice, and the Marshaler doc did not say a
// batch must not be kept. The documentation now says so, and no batch is
// larger than WithBatchSize, Flush and Close included.
func TestFinding_BatchesBounded(t *testing.T) {
	t.Parallel()

	m := &capture{}
	l := shippingLogger(t, m, quiet(log.WithBatchSize(4))...)

	for range 30 {
		l.Info("r").Send()
	}

	require.NoError(t, l.Close(t.Context()))

	total := 0

	for _, n := range m.sizes() {
		assert.LessOrEqual(t, n, 4)

		total += n
	}

	assert.Equal(t, 30, total)
}
