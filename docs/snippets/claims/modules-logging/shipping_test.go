package moduleslogging_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// Docs: modules/logging.mdx#what-it-is-for
// "| `NewShippingLogger()` | `log/slog` plus HTTP | Writes locally and sends records in batches to an HTTP endpoint. |"
// Docs: modules/logging.mdx#shipping-logs
// "A background goroutine posts the queue to `endpoint` in batches, one `POST` per batch, encoded by the marshaler. `NewOTLPMarshaler` encodes OTLP/HTTP JSON logs."
func TestShippingLocalAndBatches(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	local := &logSink{}
	var methods []string
	var mu sync.Mutex
	l, err := gaslog.NewShippingLogger(srv.URL+"/v1/logs", gaslog.NewOTLPMarshaler(),
		gaslog.WithLocalHandler(slog.NewJSONHandler(local, nil)),
		gaslog.WithBatchSize(3), gaslog.WithFlushInterval(time.Hour),
		gaslog.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			methods = append(methods, r.Method+" "+r.URL.Path)
			mu.Unlock()
			return http.DefaultTransport.RoundTrip(r)
		})}))()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close(context.Background()) })
	sendN(l, 6, "rec")
	if !eventually(t, 5*time.Second, func() bool { return len(srv.snapshot()) == 2 }) {
		t.Fatalf("%d batches, want 2 of 3", len(srv.snapshot()))
	}
	for _, b := range srv.snapshot() {
		if n := len(decodeOTLP(t, b.body).records()); n != 3 {
			t.Errorf("batch holds %d records, want 3", n)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 2 || methods[0] != "POST /v1/logs" || methods[1] != "POST /v1/logs" {
		t.Errorf("requests = %v, want one POST to the endpoint per batch", methods)
	}
	if n := len(local.records(t)); n != 6 {
		t.Errorf("local handler wrote %d records, want 6", n)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Docs: modules/logging.mdx#shipping-logs
// "Each record goes to the local handler when it is at or above that handler's level, and to a queue when it is at or above the shipping level."
func TestShippingLevels(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	local := &logSink{}
	l := shipper(t, srv.URL, gaslog.WithLevel(slog.LevelWarn),
		gaslog.WithLocalHandler(slog.NewJSONHandler(local, &slog.HandlerOptions{Level: slog.LevelDebug})))
	l.Debug("debug").Send()
	l.Info("info").Send()
	l.Warn("warn").Send()
	l.Error("error").Send()
	l.Flush()
	if got := strings.Join(srv.messages(t), ","); got != "warn,error" {
		t.Errorf("shipped %q, want warn,error", got)
	}
	if n := len(local.records(t)); n != 4 {
		t.Errorf("local wrote %d records, want all 4", n)
	}

	srv2 := newShipServer(t)
	local2 := &logSink{}
	l2 := shipper(t, srv2.URL, gaslog.WithLevel(slog.LevelInfo),
		gaslog.WithLocalHandler(slog.NewJSONHandler(local2, &slog.HandlerOptions{Level: slog.LevelError})))
	l2.Info("info").Send()
	l2.Error("error").Send()
	l2.Flush()
	if got := strings.Join(srv2.messages(t), ","); got != "info,error" {
		t.Errorf("shipped %q, want info,error", got)
	}
	if len(local2.withMsg(t, "info")) != 0 || len(local2.withMsg(t, "error")) != 1 {
		t.Errorf("local at Error wrote %s", local2.String())
	}
}

// Docs: modules/logging.mdx#shipping-logs
// "| `WithBatchSize(n)` | 100 records | A batch is posted when it reaches this size. Values that are not positive are ignored. |"
func TestShippingBatchSize(t *testing.T) {
	t.Parallel()
	for name, opts := range map[string][]gaslog.ShippingOption{
		"default": nil,
		"zero":    {gaslog.WithBatchSize(0)},
		"minus":   {gaslog.WithBatchSize(-5)},
	} {
		srv := newShipServer(t)
		l := shipper(t, srv.URL, opts...)
		sendN(l, 99, "r")
		time.Sleep(100 * time.Millisecond)
		if n := len(srv.snapshot()); n != 0 {
			t.Errorf("%s: posted %d batches at 99 records, want none", name, n)
		}
		sendN(l, 1, "last")
		if !eventually(t, 5*time.Second, func() bool { return len(srv.snapshot()) == 1 }) {
			t.Errorf("%s: no batch at 100 records", name)
			continue
		}
		if n := len(srv.messages(t)); n != 100 {
			t.Errorf("%s: batch held %d records, want 100", name, n)
		}
	}
}

// queueCapacity measures how many records wait while the sender is busy:
// one record is posted to a held endpoint, then 3000 are logged.
func queueCapacity(t *testing.T, opts ...gaslog.ShippingOption) int {
	t.Helper()
	srv := newShipServer(t)
	release := srv.hold()
	l := shipper(t, srv.URL, append([]gaslog.ShippingOption{gaslog.WithBatchSize(5000)}, opts...)...)
	l.Info("first").Send()
	flushed := make(chan struct{})
	go func() { l.Flush(); close(flushed) }()
	srv.waitArrived(t)
	sendN(l, 3000, "q")
	release()
	<-flushed
	l.Flush()
	return len(srv.messages(t)) - 1
}

// Docs: modules/logging.mdx#shipping-logs
// "| `WithQueueSize(n)` | 1024 records | Records waiting for delivery. Values that are not positive are ignored. |"
func TestShippingQueueSize(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		opts []gaslog.ShippingOption
		want int
	}{
		"default": {nil, 1024},
		"zero":    {[]gaslog.ShippingOption{gaslog.WithQueueSize(0)}, 1024},
		"minus":   {[]gaslog.ShippingOption{gaslog.WithQueueSize(-1)}, 1024},
		"ten":     {[]gaslog.ShippingOption{gaslog.WithQueueSize(10)}, 10},
	} {
		if got := queueCapacity(t, c.opts...); got != c.want {
			t.Errorf("%s: %d records waited, want %d", name, got, c.want)
		}
	}
}

// arrivalAfter logs one record and returns how long it took to arrive
// without a Flush.
func arrivalAfter(t *testing.T, opts ...gaslog.ShippingOption) time.Duration {
	t.Helper()
	srv := newShipServer(t)
	l, err := gaslog.NewShippingLogger(srv.URL, gaslog.NewOTLPMarshaler(), append([]gaslog.ShippingOption{gaslog.WithoutLocalHandler()}, opts...)...)()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close(context.Background()) })
	begin := time.Now()
	l.Info("tick").Send()
	srv.waitArrived(t)
	return time.Since(begin)
}

// Docs: modules/logging.mdx#shipping-logs
// "| `WithFlushInterval(d)` | 2s | A non-empty batch is posted at least this often. Values that are not positive are ignored. |"
func TestShippingFlushInterval(t *testing.T) {
	t.Parallel()
	for name, opts := range map[string][]gaslog.ShippingOption{
		"default": nil,
		"zero":    {gaslog.WithFlushInterval(0)},
		"minus":   {gaslog.WithFlushInterval(-time.Second)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if d := arrivalAfter(t, opts...); d > 2500*time.Millisecond || d < 100*time.Millisecond {
				t.Errorf("record arrived after %v, want within the 2s interval", d)
			}
		})
	}
	t.Run("100ms", func(t *testing.T) {
		t.Parallel()
		if d := arrivalAfter(t, gaslog.WithFlushInterval(100*time.Millisecond)); d > 500*time.Millisecond {
			t.Errorf("record arrived after %v, want about 100ms", d)
		}
	})
}

// Docs: modules/logging.mdx#shipping-logs
// "| `WithHTTPClient(c)` | a client with a 10s timeout | `nil` is ignored. |"
func TestShippingHTTPClient(t *testing.T) {
	t.Parallel()
	t.Run("default 10s timeout", func(t *testing.T) {
		t.Parallel()
		srv := newShipServer(t)
		release := srv.hold()
		t.Cleanup(release)
		errs := &errorLog{}
		l := shipper(t, srv.URL, gaslog.WithErrorHandler(errs.handle))
		l.Info("slow").Send()
		begin := time.Now()
		l.Flush()
		took := time.Since(begin)
		if took < 9*time.Second || took > 12*time.Second {
			t.Errorf("Flush against a stalled endpoint took %v, want the 10s client timeout", took)
		}
		if got := errs.list(); len(got) != 1 || !strings.Contains(got[0].Error(), "Timeout") {
			t.Errorf("errors = %v, want one client timeout", got)
		}
	})
	t.Run("nil ignored, custom used", func(t *testing.T) {
		t.Parallel()
		srv := newShipServer(t)
		l := shipper(t, srv.URL, gaslog.WithHTTPClient(nil))
		l.Info("with nil").Send()
		l.Flush()
		if len(srv.messages(t)) != 1 {
			t.Error("WithHTTPClient(nil) broke delivery, want it ignored")
		}
		var used atomic.Int32
		custom := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			used.Add(1)
			return http.DefaultTransport.RoundTrip(r)
		})}
		l2 := shipper(t, srv.URL, gaslog.WithHTTPClient(custom))
		l2.Info("with custom").Send()
		l2.Flush()
		if used.Load() != 1 {
			t.Errorf("custom client used %d times, want 1", used.Load())
		}
	})
}

// captureStderr swaps os.Stderr for a pipe until the returned stop is
// called, which restores it and returns what was written.
func captureStderr(t *testing.T) (stop func() string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	return func() string {
		os.Stderr = orig
		_ = w.Close()
		return <-done
	}
}

// Docs: modules/logging.mdx#shipping-logs
// "| `WithLevel(l)` | `slog.LevelInfo` | Minimum level shipped, and the level of the default local handler. `nil` is ignored. |" and "| `WithLocalHandler(h)` | JSON on stderr at the shipping level | Replaces the local handler, with its own level. |" and "| `WithoutLocalHandler()` | | Ships only. |"
func TestShippingLevelAndLocalHandler(t *testing.T) {
	srv := newShipServer(t)
	stop := captureStderr(t)
	def, err := gaslog.NewShippingLogger(srv.URL, gaslog.NewOTLPMarshaler(), gaslog.WithFlushInterval(time.Hour))()
	if err != nil {
		t.Fatal(err)
	}
	nilLevel, err := gaslog.NewShippingLogger(srv.URL, gaslog.NewOTLPMarshaler(), gaslog.WithFlushInterval(time.Hour), gaslog.WithLevel(nil))()
	if err != nil {
		t.Fatal(err)
	}
	warn, err := gaslog.NewShippingLogger(srv.URL, gaslog.NewOTLPMarshaler(), gaslog.WithFlushInterval(time.Hour), gaslog.WithLevel(slog.LevelWarn))()
	if err != nil {
		t.Fatal(err)
	}
	shipOnly, err := gaslog.NewShippingLogger(srv.URL, gaslog.NewOTLPMarshaler(), gaslog.WithFlushInterval(time.Hour), gaslog.WithoutLocalHandler())()
	if err != nil {
		t.Fatal(err)
	}
	def.Debug("def-debug").Send()
	def.Info("def-info").Send()
	nilLevel.Debug("nil-debug").Send()
	nilLevel.Info("nil-info").Send()
	warn.Info("warn-info").Send()
	warn.Warn("warn-warn").Send()
	shipOnly.Error("shiponly-error").Send()
	for _, l := range []*gaslog.ShippingLogger{def, nilLevel, warn, shipOnly} {
		_ = l.Close(context.Background())
	}
	stderr := stop()

	var lines []string
	for line := range strings.SplitSeq(strings.TrimSpace(stderr), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "{") {
			t.Errorf("stderr line %q is not JSON", line)
		}
	}
	for _, want := range []string{"def-info", "nil-info", "warn-warn"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %s:\n%s", want, stderr)
		}
	}
	for _, unwanted := range []string{"def-debug", "nil-debug", "warn-info", "shiponly-error"} {
		if strings.Contains(stderr, unwanted) {
			t.Errorf("stderr has %s:\n%s", unwanted, stderr)
		}
	}
	shipped := strings.Join(srv.messages(t), ",")
	for _, want := range []string{"def-info", "nil-info", "warn-warn", "shiponly-error"} {
		if !strings.Contains(shipped, want) {
			t.Errorf("not shipped: %s (shipped %s)", want, shipped)
		}
	}
	for _, unwanted := range []string{"def-debug", "nil-debug", "warn-info"} {
		if strings.Contains(shipped, unwanted) {
			t.Errorf("shipped below the level: %s", unwanted)
		}
	}
}

// Docs: modules/logging.mdx#shipping-logs
// "| `WithHeader(k, v)` | | Sent on every batch request, for example an API key. |"
func TestShippingHeader(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	l := shipper(t, srv.URL, gaslog.WithHeader("Authorization", "Bearer k"), gaslog.WithHeader("X-Api-Key", "abc"))
	for range 3 {
		sendN(l, 1, "h")
		l.Flush()
	}
	batches := srv.snapshot()
	if len(batches) != 3 {
		t.Fatalf("%d batches, want 3", len(batches))
	}
	for _, b := range batches {
		if b.header.Get("Authorization") != "Bearer k" || b.header.Get("X-Api-Key") != "abc" {
			t.Errorf("batch headers %v lack the WithHeader values", b.header)
		}
	}
}

// Docs: modules/logging.mdx#shipping-logs
// "| `WithName(name)` | `gas/log-shipping` | The `gas.Service` name. An empty name is ignored. |"
func TestShippingName(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	for _, c := range []struct {
		opts []gaslog.ShippingOption
		want string
	}{
		{nil, "gas/log-shipping"},
		{[]gaslog.ShippingOption{gaslog.WithName("")}, "gas/log-shipping"},
		{[]gaslog.ShippingOption{gaslog.WithName("audit-shipper")}, "audit-shipper"},
	} {
		if got := shipper(t, srv.URL, c.opts...).Name(); got != c.want {
			t.Errorf("Name = %q, want %q", got, c.want)
		}
	}
}

// Docs: modules/logging.mdx#delivery
// "A record is queued without blocking. When the queue is full, it is dropped silently."
func TestShippingNeverBlocks(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	release := srv.hold()
	t.Cleanup(release)
	errs := &errorLog{}
	l := shipper(t, srv.URL, gaslog.WithQueueSize(2), gaslog.WithBatchSize(1), gaslog.WithErrorHandler(errs.handle))
	l.Info("first").Send()
	srv.waitArrived(t)
	begin := time.Now()
	sendN(l, 500, "drop")
	if took := time.Since(begin); took > time.Second {
		t.Fatalf("logging 500 records against a full queue took %v, want no blocking", took)
	}
	release()
	l.Flush()
	if got := errs.list(); len(got) != 0 {
		t.Fatalf("drops reported errors %v, want silence", got)
	}
}

// Docs: modules/logging.mdx#delivery
// "A batch is posted when it reaches the batch size, when the flush interval passes, on `Flush`, and on `Close`."
func TestShippingPostTriggers(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	l := shipper(t, srv.URL, gaslog.WithBatchSize(2))
	sendN(l, 2, "size")
	if !eventually(t, 5*time.Second, func() bool { return len(srv.snapshot()) == 1 }) {
		t.Fatal("no post at the batch size")
	}
	l.Info("flush").Send()
	l.Flush()
	if len(srv.snapshot()) != 2 {
		t.Fatal("no post on Flush")
	}
	l.Info("close").Send()
	_ = l.Close(context.Background())
	if len(srv.snapshot()) != 3 {
		t.Fatal("no post on Close")
	}
	if d := arrivalAfter(t, gaslog.WithFlushInterval(50*time.Millisecond)); d > time.Second {
		t.Fatalf("no post when the interval passed (%v)", d)
	}
}

type failingMarshaler struct{}

func (failingMarshaler) Marshal([]gaslog.Record) ([]byte, error) {
	return nil, errors.New("no encoding")
}
func (failingMarshaler) ContentType() string { return "text/plain" }

// Docs: modules/logging.mdx#delivery
// "Marshal errors, transport errors and responses with a status of 300 or more go to the `WithErrorHandler` callback, never to the code that logged. Without a callback they are discarded."
// Docs: modules/logging.mdx#errors
// "| `marshal log batch: ...` | The marshaler returned an error. The batch is dropped. | | `build log request: ...` | The request could not be built, for example from an invalid endpoint URL. | | `ship logs: ...` | The HTTP request failed. | | `ship logs: unexpected status <code>` | The endpoint answered with a status of 300 or more. |"
func TestShippingFailures(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	closed := newShipServer(t)
	closed.Close()
	cases := map[string]struct {
		endpoint string
		m        gaslog.Marshaler
		status   int
		prefix   string
	}{
		"marshal":   {srv.URL, failingMarshaler{}, 200, "marshal log batch: "},
		"build":     {"://bad url", gaslog.NewOTLPMarshaler(), 200, "build log request: "},
		"transport": {closed.URL, gaslog.NewOTLPMarshaler(), 200, "ship logs: "},
		"status300": {srv.URL, gaslog.NewOTLPMarshaler(), 300, "ship logs: unexpected status 300"},
		"status503": {srv.URL, gaslog.NewOTLPMarshaler(), 503, "ship logs: unexpected status 503"},
	}
	for name, c := range cases {
		target := newShipServer(t)
		target.setStatus(c.status)
		endpoint := c.endpoint
		if endpoint == srv.URL {
			endpoint = target.URL
		}
		errs := &errorLog{}
		l, err := gaslog.NewShippingLogger(endpoint, c.m, gaslog.WithoutLocalHandler(),
			gaslog.WithFlushInterval(time.Hour), gaslog.WithErrorHandler(errs.handle))()
		if err != nil {
			t.Fatal(err)
		}
		l.Info("doomed").Send()
		l.Flush()
		got := errs.list()
		if len(got) != 1 || !strings.HasPrefix(got[0].Error(), c.prefix) {
			t.Errorf("%s: errors %v, want one starting %q", name, got, c.prefix)
		}
		if name == "marshal" {
			l.Flush()
			if n := len(errs.list()); n != 1 {
				t.Errorf("marshal: the failed batch was retried (%d errors), want it dropped", n)
			}
		}
		_ = l.Close(context.Background())

		quiet := shipper(t, endpoint)
		quiet.Info("discarded").Send()
		quiet.Flush()
	}
	statusOK := newShipServer(t)
	statusOK.setStatus(299)
	errs := &errorLog{}
	l := shipper(t, statusOK.URL, gaslog.WithErrorHandler(errs.handle))
	l.Info("fine").Send()
	l.Flush()
	if len(errs.list()) != 0 {
		t.Errorf("status 299 reported %v, want no error", errs.list())
	}
}

type failingHandler struct{ calls atomic.Int32 }

func (h *failingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *failingHandler) Handle(context.Context, slog.Record) error {
	h.calls.Add(1)
	return errors.New("local failed")
}
func (h *failingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *failingHandler) WithGroup(string) slog.Handler      { return h }

// Docs: modules/logging.mdx#delivery
// "The local handler sees each record first. If it returns an error, the record is not shipped, and `Send` drops the error."
func TestShippingLocalHandlerError(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	local := &failingHandler{}
	l := shipper(t, srv.URL, gaslog.WithLocalHandler(local))
	l.Info("local fails").Send()
	l.Flush()
	if local.calls.Load() != 1 || len(srv.messages(t)) != 0 {
		t.Fatalf("local calls %d, shipped %v; want the record not shipped", local.calls.Load(), srv.messages(t))
	}
}

// Docs: modules/logging.mdx#delivery
// "The shipping logger implements `gas.Service`. Its goroutine starts when it is constructed, so it works with or without the container, and its `Init` does nothing."
func TestShippingIsService(t *testing.T) {
	t.Parallel()
	var _ gas.Service = (*gaslog.ShippingLogger)(nil)
	srv := newShipServer(t)
	l := shipper(t, srv.URL, gaslog.WithFlushInterval(50*time.Millisecond))
	l.Info("no init").Send()
	if !eventually(t, 5*time.Second, func() bool { return len(srv.messages(t)) == 1 }) {
		t.Fatal("nothing shipped without Init")
	}
	if err := l.Init(context.Background()); err != nil {
		t.Fatalf("Init = %v", err)
	}
	l.Info("after init").Send()
	if !eventually(t, 5*time.Second, func() bool { return len(srv.messages(t)) == 2 }) {
		t.Fatal("Init changed delivery")
	}
}

// Docs: modules/logging.mdx#delivery
// "`Close`, which the container runs at shutdown, posts the records still queued, stops the goroutine and returns once that last post has finished, so it can take as long as the HTTP client's timeout; it ignores its context and can be called more than once."
func TestShippingClose(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	app := gas.NewApp(gas.WithSingletonService[gas.Logger](
		gaslog.NewShippingLogger(srv.URL, gaslog.NewOTLPMarshaler(), gaslog.WithoutLocalHandler(), gaslog.WithFlushInterval(time.Hour))))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	logger := app.ServiceContainer().MustResolve[gas.Logger]()
	logger.Info("at shutdown").Send()
	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(srv.messages(t), ","), "at shutdown") {
		t.Fatalf("shipped %v, want the queued record posted at shutdown", srv.messages(t))
	}

	slow := newShipServer(t)
	release := slow.hold()
	l := shipper(t, slow.URL, gaslog.WithHTTPClient(&http.Client{Timeout: 400 * time.Millisecond}))
	l.Info("last").Send()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	begin := time.Now()
	go func() { time.Sleep(200 * time.Millisecond); release() }()
	if err := l.Close(ctx); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if took := time.Since(begin); took < 150*time.Millisecond {
		t.Fatalf("Close returned after %v, before the last post finished", took)
	}
	if len(slow.messages(t)) != 1 {
		t.Fatal("Close with a canceled context did not post the queued record")
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatalf("second Close = %v", err)
	}
}

// Docs: modules/logging.mdx#delivery
// "After `Close`, the local handler still writes, records meant for shipping are dropped, and `Flush` returns at once."
func TestShippingAfterClose(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	local := &logSink{}
	l := shipper(t, srv.URL, gaslog.WithLocalHandler(slog.NewJSONHandler(local, nil)), gaslog.WithFlushInterval(10*time.Millisecond))
	_ = l.Close(context.Background())
	l.Info("after close").Send()
	begin := time.Now()
	l.Flush()
	if took := time.Since(begin); took > 100*time.Millisecond {
		t.Fatalf("Flush after Close took %v, want it to return at once", took)
	}
	time.Sleep(100 * time.Millisecond)
	if len(local.withMsg(t, "after close")) != 1 {
		t.Fatal("local handler stopped writing after Close")
	}
	if len(srv.snapshot()) != 0 {
		t.Fatal("a record was shipped after Close")
	}
}

// Docs: modules/logging.mdx#delivery
// "A sub-logger made with `With()` shares the same handlers and sender, so its records are shipped and its `Flush` posts and waits like the parent's."
func TestShippingSubLogger(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	local := &logSink{}
	l := shipper(t, srv.URL, gaslog.WithLocalHandler(slog.NewJSONHandler(local, nil)))
	sub := l.With().Str("component", "sub").Logger()
	l.Info("parent queued").Send()
	sub.Info("sub queued").Send()
	sub.Flush()
	if got := strings.Join(srv.messages(t), ","); got != "parent queued,sub queued" {
		t.Fatalf("after the sub-logger's Flush the endpoint had %q, want both records in one sender", got)
	}
	if len(srv.snapshot()) != 1 {
		t.Fatalf("%d batches, want one shared batch", len(srv.snapshot()))
	}
	if local.line(t, "sub queued")["component"] != "sub" {
		t.Fatal("sub-logger did not write through the local handler")
	}
}

// Docs: modules/logging.mdx#shipping-from-plain-slog
// "`NewShippingHandler` takes the same arguments and options and returns the same local-plus-shipping pipeline as an `slog.Handler`, with an `io.Closer` that drains and stops it like `Close` above."
func TestShippingHandler(t *testing.T) {
	t.Parallel()
	srv := newShipServer(t)
	local := &logSink{}
	h, closer := gaslog.NewShippingHandler(srv.URL, gaslog.NewOTLPMarshaler(gaslog.WithServiceName("svc")),
		gaslog.WithLocalHandler(slog.NewJSONHandler(local, nil)), gaslog.WithFlushInterval(time.Hour),
		gaslog.WithLevel(slog.LevelWarn), gaslog.WithHeader("X-K", "v"))
	logger := slog.New(h)
	logger.Info("local only")
	logger.Warn("both", "k", "v")
	if len(srv.snapshot()) != 0 {
		t.Fatal("posted before Close")
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(srv.messages(t), ","); got != "both" {
		t.Fatalf("shipped %q, want both", got)
	}
	if srv.snapshot()[0].header.Get("X-K") != "v" {
		t.Fatal("options not applied to NewShippingHandler")
	}
	if len(local.records(t)) != 2 {
		t.Fatalf("local wrote %s, want 2 records", local.String())
	}
	logger.Warn("after close")
	time.Sleep(50 * time.Millisecond)
	if len(srv.snapshot()) != 1 {
		t.Fatal("the closer did not stop the pipeline")
	}
}
