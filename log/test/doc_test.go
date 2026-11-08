package log_test

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/log"
)

// The example in the package documentation's Usage section, posting to a
// test server.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	srv := newCollector(t)
	token := "t0ken"

	logger, err := log.NewShippingLogger(
		srv.srv.URL+"/v1/logs",
		log.NewOTLPMarshaler(log.WithServiceName("shop")),
		log.WithHeader("Authorization", "Bearer "+token),
	)()
	require.NoError(t, err)
	require.NoError(t, logger.Init(ctx))

	logger.Info("order placed").Str("order", "A-1").Int("items", 3).Send()

	require.NoError(t, logger.Close(ctx))

	bodies := srv.posted()
	require.Len(t, bodies, 1)
	assert.Contains(t, string(bodies[0]), `"stringValue":"order placed"`)
	assert.Contains(t, string(bodies[0]), `{"value":{"stringValue":"A-1"},"key":"order"}`)
	assert.Contains(t, string(bodies[0]), `{"value":{"intValue":"3"},"key":"items"}`)
	assert.Equal(t, "Bearer t0ken", srv.headers[0].Get("Authorization"))
	assert.Equal(t, log.OTLPContentType, srv.headers[0].Get("Content-Type"))
}

// failingHandler is a slog.Handler that enables every level and whose
// Handle fails with err.
type failingHandler struct {
	// err is what Handle returns.
	err error
}

// Enabled returns true.
func (failingHandler) Enabled(context.Context, slog.Level) bool { return true }

// Handle returns h.err.
func (h failingHandler) Handle(context.Context, slog.Record) error { return h.err }

// WithAttrs returns h.
func (h failingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

// WithGroup returns h.
func (h failingHandler) WithGroup(string) slog.Handler { return h }

// The error messages the package documentation lists are the ones the
// package returns or reports, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	m := &capture{}
	// option returns the constructor's error with opt.
	option := func(opt log.ShippingOption) func() error {
		return func() error {
			_, err := log.NewShippingHandler(endpoint, m, opt)

			return err
		}
	}
	// reported returns the first report of a handler on marshaler with
	// opts, after one record and a Flush.
	reported := func(marshaler log.Marshaler, opts ...log.ShippingOption) func() error {
		return func() error {
			r := newReports()
			h := shippingHandler(
				t,
				marshaler,
				quiet(append(opts, log.WithErrorHandler(r.handler))...)...)

			slog.New(h).Info("x")
			require.Error(t, h.Flush(t.Context()))

			return r.next(t)
		}
	}

	queueFull := newReports()
	queueFullOnce := func() func() error {
		var produced []error

		return func() error {
			if produced == nil {
				produced = queueFullReports(t, queueFull)
			}

			err := produced[0]

			produced = produced[1:]

			return err
		}
	}()

	cases := map[string]func() error{
		"gas/log: logger is nil": func() error {
			_, err := log.NewSlogLogger(log.WithSlogLogger(nil))()

			return err
		},
		"gas/log: marshaler is nil": func() error {
			_, err := log.NewShippingHandler(endpoint, nil)

			return err
		},
		"gas/log: invalid endpoint: must be an absolute http or https URL": func() error {
			_, err := log.NewShippingHandler("ftp://collector.example/v1/logs", m)

			return err
		},
		"gas/log: invalid option: WithBatchSize must be positive": option(
			log.WithBatchSize(0),
		),
		"gas/log: invalid option: WithQueueSize must be positive": option(
			log.WithQueueSize(-1),
		),
		"gas/log: invalid option: WithFlushInterval must be positive": option(
			log.WithFlushInterval(0),
		),
		"gas/log: invalid option: WithHTTPClient requires a client": option(
			log.WithHTTPClient(nil),
		),
		"gas/log: invalid option: WithLevel requires a level": option(log.WithLevel(nil)),
		"gas/log: invalid option: WithLocalHandler requires a handler": option(
			log.WithLocalHandler(nil),
		),
		"gas/log: invalid option: WithErrorHandler requires a function": option(
			log.WithErrorHandler(nil),
		),
		"gas/log: invalid option: WithName requires a name": option(log.WithName("")),
		"gas/log: invalid option: WithHeader requires a token as the name": option(
			log.WithHeader("X Api Key", "k"),
		),
		"gas/log: invalid option: WithHeader requires a value without control characters": option(
			log.WithHeader("Authorization", "Bearer s3cret\r\nX-Injected: 1"),
		),
		"gas/log: not running: record dropped": func() error {
			r := newReports()

			l, err := log.NewShippingLogger(
				endpoint,
				m,
				quiet(log.WithErrorHandler(r.handler))...)()
			require.NoError(t, err)

			l.Info("x").Send()

			return r.next(t)
		},
		"gas/log: still closing: context deadline exceeded": func() error {
			g := newGate(t)
			l := shippingLogger(t, g, quiet(log.WithErrorHandler(func(error) {}))...)

			l.Info("x").Send()

			ctx, cancel := context.WithCancel(t.Context())

			go func() {
				<-g.entered
				cancel()
			}()

			require.ErrorIs(t, l.Close(ctx), context.Canceled)

			expired, cancelExpired := context.WithDeadline(t.Context(), time.Unix(0, 0))
			defer cancelExpired()

			return l.Init(expired)
		},
		"gas/log: queue full: record dropped":           queueFullOnce,
		"gas/log: queue full: record dropped (3 times)": queueFullOnce,
		"gas/log: local handler failed: disk full": func() error {
			h := shippingHandler(t, m,
				log.WithLocalHandler(failingHandler{err: errDiskFull}),
				log.WithErrorHandler(func(error) {}))

			return h.Handle(t.Context(), slog.NewRecord(time.Now(), slog.LevelInfo, "x", 0))
		},
		"gas/log: marshal failed: unsupported value": reported(
			&capture{err: errUnsupported},
		),
		"gas/log: marshaler panicked": reported(&capture{panics: true}),
		`gas/log: post failed: Post "https://collector.example:4318": connection refused`: func() error {
			r := newReports()

			h, err := log.NewShippingHandler(
				"https://user:s3cret@collector.example:4318/v1/logs?api_key=s3cret#s3cret",
				m,
				quiet(
					log.WithHTTPClient(failing(errRefused)),
					log.WithErrorHandler(r.handler),
				)...,
			)
			require.NoError(t, err)

			slog.New(h).Info("x")
			require.Error(t, h.Close(t.Context()))

			return r.next(t)
		},
		"gas/log: unexpected status 503 from https://collector.example:4318": reported(
			m, log.WithHTTPClient(status(http.StatusServiceUnavailable))),
		"gas/log: not delivered: 1 record": func() error {
			h := shippingHandler(t, m, quiet(
				log.WithHTTPClient(failing(errFake)),
				log.WithErrorHandler(func(error) {}),
			)...)

			slog.New(h).Info("x")

			return h.Close(t.Context())
		},
		"gas/log: not delivered: 2 records: context deadline exceeded": func() error {
			client, _ := blocking()
			h := shippingHandler(
				t,
				m,
				quiet(log.WithHTTPClient(client), log.WithErrorHandler(func(error) {}))...)

			slog.New(h).Info("x")
			slog.New(h).Info("y")

			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()

			return h.Close(ctx)
		},
	}

	documented := docLines(t, "../doc.go", "gas/log: ")
	assert.ElementsMatch(t, documented, slices.Collect(maps.Keys(cases)))

	// The queue-full cases run in order: the first report, then the
	// coalesced one.
	for _, msg := range []string{"gas/log: queue full: record dropped", "gas/log: queue full: record dropped (3 times)"} {
		require.EqualError(t, cases[msg](), msg)
		delete(cases, msg)
	}

	for msg, produce := range cases {
		assert.EqualError(t, produce(), msg, msg)
	}
}

// queueFullReports fills a queue of one behind a post that blocks, drops
// a record, takes its report, made at once, drops three more, and takes
// their report, coalesced into one when the interval has passed.
func queueFullReports(t *testing.T, r *reports) []error {
	t.Helper()

	client, started := blocking()
	h := shippingHandler(t, &capture{}, log.WithoutLocalHandler(),
		log.WithQueueSize(1), log.WithBatchSize(1), log.WithFlushInterval(100*time.Millisecond),
		log.WithHTTPClient(client), log.WithErrorHandler(r.handler))
	logger := slog.New(h)

	logger.Info("posting")
	<-started
	logger.Info("queued")

	logger.Info("dropped")

	first := r.next(t)

	for range 3 {
		logger.Info("dropped")
	}

	second := r.next(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	require.ErrorIs(t, h.Close(ctx), context.DeadlineExceeded)

	return []error{first, second}
}
