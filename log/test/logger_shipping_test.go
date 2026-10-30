package log_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/log"
)

// gated returns a client whose requests wait until release is closed,
// answering 204, and a channel that receives each request as it starts.
func gated(release <-chan struct{}) (*http.Client, chan *http.Request) {
	started := make(chan *http.Request, 100)

	return clientOf(func(r *http.Request) (*http.Response, error) {
		started <- r

		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}

		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Request: r}, nil
	}), started
}

// A batch is posted when it holds WithBatchSize records, and the rest on
// Flush.
func TestShippingLogger_BatchSize(t *testing.T) {
	t.Parallel()

	m := &capture{}
	l := shippingLogger(t, m, quiet(log.WithBatchSize(3))...)

	for range 7 {
		l.Info("r").Send()
	}

	require.Eventually(
		t,
		func() bool { return len(m.sizes()) == 2 },
		5*time.Second,
		time.Millisecond,
	)

	l.Flush()

	assert.Equal(t, []int{3, 3, 1}, m.sizes())
}

// A record waits at most the flush interval.
func TestShippingLogger_FlushInterval(t *testing.T) {
	t.Parallel()

	m := &capture{}
	l := shippingLogger(t, m, log.WithoutLocalHandler(), log.WithFlushInterval(5*time.Millisecond))

	l.Info("r").Send()

	require.Eventually(
		t,
		func() bool { return len(m.records()) == 1 },
		5*time.Second,
		time.Millisecond,
	)
}

// Flush and Close post what is queued in batches of WithBatchSize, not as
// one request.
func TestShippingHandler_FlushInBatches(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	client, started := gated(release)
	m := &capture{}
	h := shippingHandler(t, m, quiet(log.WithBatchSize(2), log.WithHTTPClient(client))...)
	logger := slog.New(h)

	logger.Info("a")
	logger.Info("b")
	<-started

	for _, msg := range []string{"c", "d", "e"} {
		logger.Info(msg)
	}

	close(release)

	require.NoError(t, h.Flush(t.Context()))
	assert.Equal(t, []int{2, 2, 1}, m.sizes())
	assert.Equal(t, []string{"a", "b", "c", "d", "e"}, m.messages())

	logger.Info("f")
	require.NoError(t, h.Close(t.Context()))
	assert.Equal(t, []int{2, 2, 1, 1}, m.sizes())
}

// A record logged before Init or after Close is written locally, dropped
// for shipping and counted; one logged before the first Init is reported,
// one logged after Close is not. Close before Init and a second Close
// return nil. Init after Close ships again.
func TestShippingLogger_Lifecycle(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	m := &capture{}
	r := newReports()
	l, err := log.NewShippingLogger(
		endpoint,
		m,
		log.WithHTTPClient(ok()),
		log.WithFlushInterval(time.Hour),
		log.WithLocalHandler(slog.NewJSONHandler(&buf, nil)),
		log.WithErrorHandler(r.handler),
	)()
	require.NoError(t, err)

	ctx := t.Context()

	require.NoError(t, l.Close(ctx))
	l.Info("before").Send()
	l.Flush()

	require.NoError(t, l.Init(ctx))
	require.NoError(t, l.Init(ctx), "Init of a running logger")
	l.Info("first").Send()
	require.NoError(t, l.Close(ctx))
	require.NoError(t, l.Close(ctx), "a second Close")

	l.Info("after").Send()

	require.NoError(t, l.Init(ctx))
	l.Info("again").Send()
	require.NoError(t, l.Close(ctx))

	assert.Equal(t, []string{"first", "again"}, m.messages())
	assert.Equal(t, uint64(2), l.Dropped())
	assert.Len(t, buf.lines(t), 4)
	require.EqualError(t, r.next(t), "gas/log: not running: record dropped")
	r.none(t)
}

// Close returns when its context ends, with the records it did not
// deliver, though the endpoint hangs.
func TestShippingLogger_CloseDeadline(t *testing.T) {
	t.Parallel()

	client, started := blocking()
	l := shippingLogger(
		t,
		&capture{},
		quiet(log.WithHTTPClient(client), log.WithErrorHandler(func(error) {}))...)

	l.Info("x").Send()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := l.Close(ctx)

	require.ErrorIs(t, err, log.ErrNotDelivered)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.NotNil(t, <-started)
	require.Eventually(t, func() bool { return l.Dropped() == 1 }, 5*time.Second, time.Millisecond)
}

// A Close whose context ends while a post hangs returns the records still
// pending, the hanging one included, cancels the post, and drops them all,
// counted and not reported.
func TestShippingLogger_CloseDeadlineDuringPost(t *testing.T) {
	t.Parallel()

	client, started := blocking()
	r := newReports()
	l := shippingLogger(t, &capture{}, log.WithoutLocalHandler(), log.WithBatchSize(1),
		log.WithHTTPClient(client), log.WithErrorHandler(r.handler))

	l.Info("posting").Send()
	<-started
	l.Info("queued").Send()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	err := l.Close(ctx)
	require.EqualError(t, err, "gas/log: not delivered: 2 records: context deadline exceeded")
	require.Eventually(t, func() bool { return l.Dropped() == 2 }, 5*time.Second, time.Millisecond)
	r.none(t)
}

// Flush returns ErrNotDelivered with the context's error when its context
// ends first, and nil when the sender is not running. The post it cancels
// goes back to the queue, so nothing is dropped until Close's context
// ends.
func TestShippingHandler_FlushDeadline(t *testing.T) {
	t.Parallel()

	client, started := blocking()
	h := shippingHandler(
		t,
		&capture{},
		quiet(log.WithHTTPClient(client), log.WithErrorHandler(func(error) {}))...)

	slog.New(h).Info("x")

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	err := h.Flush(ctx)
	require.ErrorIs(t, err, log.ErrNotDelivered)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	<-started

	busy, cancelBusy := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancelBusy()

	slog.New(h.WithGroup("g")).Info("y")
	require.ErrorIs(t, h.Flush(busy), context.DeadlineExceeded)
	assert.Zero(t, h.Dropped())

	closing, cancelClosing := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancelClosing()

	require.EqualError(
		t,
		h.Close(closing),
		"gas/log: not delivered: 2 records: context deadline exceeded",
	)
	require.Eventually(t, func() bool { return h.Dropped() == 2 }, 5*time.Second, time.Millisecond)
	require.NoError(t, h.Flush(t.Context()), "not running")
}

// A record logged on a full queue is dropped, counted and reported.
func TestShippingHandler_QueueFull(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	client, started := gated(release)
	r := newReports()
	m := &capture{}
	h := shippingHandler(
		t,
		m,
		log.WithoutLocalHandler(),
		log.WithQueueSize(1),
		log.WithBatchSize(1),
		log.WithHTTPClient(client),
		log.WithErrorHandler(r.handler),
	)
	logger := slog.New(h)

	logger.Info("posting")
	<-started

	for range 10 {
		logger.Info("x")
	}

	assert.Equal(t, uint64(9), h.Dropped())
	require.ErrorIs(t, r.next(t), log.ErrQueueFull)

	close(release)
	require.NoError(t, h.Close(t.Context()))
	assert.Len(t, m.records(), 2)
}

// A Marshaler's error or panic drops its batch, counts it and reports it;
// the sender goes on.
func TestShippingHandler_MarshalFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		m    *capture
		want error
	}{
		{m: &capture{err: errFake}, want: log.ErrMarshal},
		{m: &capture{panics: true}, want: log.ErrMarshalPanic},
	} {
		r := newReports()
		h := shippingHandler(t, tc.m, quiet(log.WithErrorHandler(r.handler))...)

		slog.New(h).Info("secret record")
		slog.New(h).Info("second")

		err := h.Flush(t.Context())
		require.EqualError(t, err, "gas/log: not delivered: 2 records")

		report := r.next(t)
		require.ErrorIs(t, report, tc.want)
		assert.NotContains(t, report.Error(), "secret record")
		assert.Equal(t, uint64(2), h.Dropped())
	}
}

// A status outside 2xx is ErrStatus, and the response body is read.
func TestShippingHandler_Status(t *testing.T) {
	t.Parallel()

	var read atomic.Bool

	client := clientOf(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Body:       readNotifier{Reader: strings.NewReader("moved"), read: &read},
			Request:    r,
		}, nil
	})
	r := newReports()
	h := shippingHandler(
		t,
		&capture{},
		quiet(log.WithHTTPClient(client), log.WithErrorHandler(r.handler))...)

	slog.New(h).Info("x")
	require.Error(t, h.Flush(t.Context()))

	err := r.next(t)
	require.ErrorIs(t, err, log.ErrStatus)
	require.EqualError(t, err, "gas/log: unexpected status 302 from "+endpoint)
	assert.True(t, read.Load())
}

// readNotifier is a response body that notes a read.
type readNotifier struct {
	io.Reader

	// read is set by Read.
	read *atomic.Bool
}

// Read notes the read and reads.
func (r readNotifier) Read(p []byte) (int, error) {
	r.read.Store(true)

	return r.Reader.Read(p)
}

// Close does nothing.
func (readNotifier) Close() error { return nil }

// The request carries the Marshaler's Content-Type, then the WithHeader
// headers, which can replace it, and the body.
func TestShippingHandler_Headers(t *testing.T) {
	t.Parallel()

	srv := newCollector(t)

	for _, tc := range []struct {
		want string
		opts []log.ShippingOption
	}{
		{want: "application/x-test"},
		{opts: []log.ShippingOption{log.WithHeader("Content-Type", "text/plain")}, want: "text/plain"},
	} {
		h, err := log.NewShippingHandler(
			srv.srv.URL,
			&capture{},
			append(
				quiet(log.WithHeader("X-Api-Key", "k1"), log.WithHeader("X-Api-Key", "k2")),
				tc.opts...)...)
		require.NoError(t, err)

		slog.New(h).Info("hello")
		require.NoError(t, h.Close(t.Context()))
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()

	require.Len(t, srv.headers, 2)
	assert.Equal(t, "application/x-test", srv.headers[0].Get("Content-Type"))
	assert.Equal(t, "text/plain", srv.headers[1].Get("Content-Type"))
	assert.Equal(t, []string{"k2"}, srv.headers[0].Values("X-Api-Key"))
	assert.Equal(t, "hello\n", string(srv.bodies[0]))
}

// The error handler may call Flush and Close, without a deadlock.
func TestShippingLogger_ErrorHandlerReentrant(t *testing.T) {
	t.Parallel()

	var l *log.ShippingLogger

	done := make(chan error, 1)

	l = shippingLogger(
		t,
		&capture{},
		quiet(log.WithHTTPClient(failing(errFake)), log.WithErrorHandler(func(error) {
			l.Flush()

			done <- l.Close(context.Background())
		}))...)

	l.Info("x").Send()

	go l.Flush()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "deadlock")
	}
}

// An error handler that logs through the logger it reports for makes at
// most one report per class per flush interval, not a loop.
func TestShippingLogger_ErrorHandlerLogs(t *testing.T) {
	t.Parallel()

	var l *log.ShippingLogger

	const interval = 50 * time.Millisecond

	calls := make(chan time.Time, 100)

	l = shippingLogger(t, &capture{}, log.WithoutLocalHandler(), log.WithFlushInterval(interval),
		log.WithHTTPClient(failing(errFake)), log.WithErrorHandler(func(err error) {
			calls <- time.Now()

			l.Error("ship failed").Err("err", err).Send()
		}))

	l.Info("x").Send()

	// Each call logs a record whose failed post is the next report, so
	// the calls go on; the reporter's timer spaces them by the interval.
	// The bound leaves room for the delay between a report's delivery and
	// the handler's clock read.
	prev := <-calls

	for range 3 {
		select {
		case at := <-calls:
			assert.GreaterOrEqual(t, at.Sub(prev), interval/2)

			prev = at
		case <-time.After(5 * time.Second):
			require.FailNow(t, "the reports stopped")
		}
	}
}

// Reports of a class made within the interval become one, with the count.
func TestShippingHandler_ReportsCoalesced(t *testing.T) {
	t.Parallel()

	r := newReports()
	h := shippingHandler(
		t,
		&capture{},
		log.WithoutLocalHandler(),
		log.WithFlushInterval(50*time.Millisecond),
		log.WithBatchSize(1),
		log.WithHTTPClient(failing(errFake)),
		log.WithErrorHandler(r.handler),
	)

	slog.New(h).Info("first")

	first := r.next(t)
	assert.NotContains(t, first.Error(), "times")

	for range 5 {
		slog.New(h).Info("x")
	}

	require.Eventually(t, func() bool { return h.Dropped() == 6 }, 5*time.Second, time.Millisecond)

	second := r.next(t)
	require.ErrorIs(t, second, log.ErrPost)
	require.ErrorIs(t, second, errFake)
	assert.True(t, strings.HasSuffix(second.Error(), "(5 times)"), second.Error())
}

// A panic of the error handler is recovered and noted on standard error,
// and the default error handler writes the error to standard error.
//
// Not parallel: it replaces os.Stderr.
func TestShippingHandler_ErrorHandlerStderr(t *testing.T) {
	want := []string{"gas/log: error handler panicked", "gas/log: marshal failed: fake failure"}

	lines := stderrLines(t, func() {
		for _, opts := range [][]log.ShippingOption{
			{log.WithErrorHandler(func(error) { panic("handler") })},
			nil,
		} {
			h, err := log.NewShippingHandler(endpoint, &capture{err: errFake},
				append(quiet(log.WithHTTPClient(ok())), opts...)...)
			require.NoError(t, err)

			slog.New(h).Info("x")
			require.Error(t, h.Close(t.Context()))
		}
	}, len(want))

	assert.ElementsMatch(t, want, lines)
}

// ShippingLogger.Flush reports a Flush that did not deliver, in the class
// of ErrNotDelivered; a logger from With flushes the same sender.
func TestShippingLogger_FlushReports(t *testing.T) {
	t.Parallel()

	r := newReports()
	l := shippingLogger(t, &capture{err: errFake}, quiet(log.WithErrorHandler(r.handler))...)
	sub := l.With().Str("k", "v").Logger()

	sub.Info("x").Send()
	sub.Flush()

	got := []error{r.next(t), r.next(t)}

	assert.True(t, errors.Is(got[0], log.ErrNotDelivered) || errors.Is(got[1], log.ErrNotDelivered))
	assert.True(t, errors.Is(got[0], log.ErrMarshal) || errors.Is(got[1], log.ErrMarshal))
}

// Name is DefaultShippingName, or WithName's.
func TestShippingLogger_Name(t *testing.T) {
	t.Parallel()

	l, err := log.NewShippingLogger(endpoint, &capture{})()
	require.NoError(t, err)
	assert.Equal(t, log.DefaultShippingName, l.Name())
	assert.Equal(t, "gas/log-shipping", l.Name())

	named, err := log.NewShippingLogger(endpoint, &capture{}, log.WithName("audit"))()
	require.NoError(t, err)
	assert.Equal(t, "audit", named.Name())

	var _ gas.Service = l
}

// Every level ships at its slog level, Trace at LevelTrace, and the local
// handler's source is the line that called Send.
func TestShippingLogger_LevelsAndSource(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	m := &capture{}
	l := shippingLogger(
		t,
		m,
		log.WithFlushInterval(time.Hour),
		log.WithLevel(log.LevelTrace),
		log.WithLocalHandler(
			slog.NewJSONHandler(&buf, &slog.HandlerOptions{AddSource: true, Level: log.LevelTrace}),
		),
	)

	l.Trace("t").Send()
	l.Debug("d").Send()
	l.Info("i").Send()
	l.Warn("w").Send()
	l.Error("e").Send()
	l.Flush()

	recs := m.records()
	levels := make([]slog.Level, 0, len(recs))

	for _, r := range recs {
		levels = append(levels, r.Level)
	}

	assert.Equal(
		t,
		[]slog.Level{
			log.LevelTrace,
			slog.LevelDebug,
			slog.LevelInfo,
			slog.LevelWarn,
			slog.LevelError,
		},
		levels,
	)

	source, _ := buf.lines(t)[0]["source"].(map[string]any)
	file, _ := source["file"].(string)
	assert.Equal(t, "logger_shipping_test.go", filepath.Base(file))
}

// Many goroutines log through sub-loggers, with Flush, while Close runs;
// -race checks it. Every record is delivered or dropped.
func TestShippingLogger_Concurrent(t *testing.T) {
	t.Parallel()

	m := &capture{}
	l := shippingLogger(t, m, log.WithoutLocalHandler(), log.WithFlushInterval(time.Millisecond),
		log.WithBatchSize(7), log.WithErrorHandler(func(error) {}))

	var wg sync.WaitGroup

	for i := range 8 {
		wg.Go(func() {
			sub := l.With().Int("g", i).Logger()
			for j := range 200 {
				sub.Info("c").Int("j", j).Send()

				if j%50 == 0 {
					sub.Flush()
				}
			}
		})
	}

	wg.Go(func() {
		time.Sleep(5 * time.Millisecond)
		assert.NoError(t, l.Close(context.Background()))
	})

	wg.Wait()
	l.Flush()

	assert.Equal(t, uint64(1600), uint64(len(m.records()))+l.Dropped())
}

// A Flush whose context has already ended posts nothing and drops
// nothing: every record stays queued, and Close ships them all.
func TestShippingHandler_FlushCanceledKeepsQueue(t *testing.T) {
	t.Parallel()

	m := &capture{}
	r := newReports()
	h := shippingHandler(t, m, quiet(log.WithErrorHandler(r.handler))...)

	for _, msg := range []string{"a", "b", "c", "d", "e"} {
		slog.New(h).Info(msg)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.EqualError(t, h.Flush(ctx), "gas/log: not delivered: 5 records: context canceled")
	assert.Empty(t, m.records())
	assert.Zero(t, h.Dropped())

	require.NoError(t, h.Close(context.Background()))
	assert.Equal(t, []string{"a", "b", "c", "d", "e"}, m.messages())
	assert.Zero(t, h.Dropped())
	r.none(t)
}

// A post a Flush's context cancels puts its batch back at the front of
// the queue, unreported, and the next post sends it before what was
// logged later. Without room for it, the batch is dropped, counted and
// not reported.
func TestShippingHandler_FlushCanceledPost(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		delivered []string
		queueSize int
		dropped   uint64
	}{
		{name: "room", queueSize: 10, delivered: []string{"a", "b", "c", "d"}},
		{name: "no room", queueSize: 2, delivered: []string{"c", "d"}, dropped: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// The first request waits for its context to end and fails;
			// the others answer 204.
			var requests atomic.Int32

			started := make(chan struct{})
			client := clientOf(func(r *http.Request) (*http.Response, error) {
				if requests.Add(1) == 1 {
					close(started)
					<-r.Context().Done()

					return nil, r.Context().Err()
				}

				return &http.Response{
					StatusCode: http.StatusNoContent,
					Body:       http.NoBody,
					Request:    r,
				}, nil
			})
			m := &capture{}
			r := newReports()
			h := shippingHandler(t, m, quiet(log.WithHTTPClient(client), log.WithBatchSize(3),
				log.WithQueueSize(tc.queueSize), log.WithErrorHandler(r.handler))...)
			logger := slog.New(h)

			logger.Info("a")
			logger.Info("b")

			ctx, cancel := context.WithCancel(t.Context())

			go func() {
				<-started
				logger.Info("c")
				logger.Info("d")
				cancel()
			}()

			require.ErrorIs(t, h.Flush(ctx), context.Canceled)

			// Close's drain runs after the Flush's, which puts the batch
			// back or drops it.
			require.NoError(t, h.Close(context.Background()))

			// The canceled post marshaled a and b before it was canceled.
			assert.Equal(t, append([]string{"a", "b"}, tc.delivered...), m.messages())
			assert.Equal(t, tc.dropped, h.Dropped())
			r.none(t)
		})
	}
}

// Init after a Close whose context ended waits for the sender of that
// Close to stop, or returns ErrStillClosing when its own context ends
// first; Marshal is never called concurrently.
func TestShippingLogger_InitWaitsForClose(t *testing.T) {
	t.Parallel()

	g := newGate(t)
	l := shippingLogger(t, g, quiet(log.WithErrorHandler(func(error) {}))...)

	l.Info("a").Send()

	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		<-g.entered
		cancel()
	}()

	err := l.Close(ctx)
	require.ErrorIs(t, err, log.ErrNotDelivered)
	require.ErrorIs(t, err, context.Canceled)

	expired, cancelExpired := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer cancelExpired()

	err = l.Init(expired)
	require.ErrorIs(t, err, log.ErrStillClosing)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	inited := make(chan error, 1)

	go func() { inited <- l.Init(context.Background()) }()

	// A concurrent Close of the stopped logger returns at once.
	require.NoError(t, l.Close(t.Context()))

	select {
	case err := <-inited:
		require.FailNow(t, "Init returned while the sender ran", "%v", err)
	default:
	}

	g.open()
	require.NoError(t, <-inited)
	assert.Equal(t, uint64(1), l.Dropped(), "the record of the canceled Close")

	l.Info("b").Send()
	require.NoError(t, l.Close(context.Background()))
	assert.False(t, g.overlapped.Load())
	assert.Equal(t, uint64(1), l.Dropped())
}

// The default client follows no redirect: a 307 to another server is a
// failed post with its status, and that server receives nothing, neither
// the headers nor the body.
func TestShippingHandler_NoRedirect(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(other.Close)

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(first.Close)

	r := newReports()
	h, err := log.NewShippingHandler(first.URL+"/v1/logs", &capture{},
		quiet(log.WithHeader("Authorization", "Bearer s3cret"), log.WithErrorHandler(r.handler))...)
	require.NoError(t, err)

	slog.New(h).Info("x")
	require.EqualError(t, h.Close(t.Context()), "gas/log: not delivered: 1 record")

	report := r.next(t)
	require.ErrorIs(t, report, log.ErrStatus)
	require.EqualError(t, report, "gas/log: unexpected status 307 from "+first.URL+"/v1/logs")
	assert.Zero(t, hits.Load())
}
