package guideslogging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

func start(t *testing.T, app *gas.App) {
	t.Helper()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
}

func zerologSink() (*logSink, gas.Logger, error) {
	sink := &logSink{}
	zl := zerolog.New(sink).Level(zerolog.TraceLevel)
	l, err := gaslog.NewZeroLogLogger(gaslog.WithZeroLogInstance(&zl))()
	return sink, l, err
}

// Docs: guides/logging.mdx#choosing-a-backend
// "| `NewZeroLogLogger()` | [rs/zerolog] | Structured JSON, full level support including Trace | | `NewSlogLogger()` | `log/slog` | Zero extra dependencies. Trace maps to Debug |"
// Docs: guides/logging.mdx#levels
// "| `Trace` | `TraceLevel` | `LevelDebug` | | `Debug` | `DebugLevel` | `LevelDebug` | | `Info` | `InfoLevel` | `LevelInfo` | | `Warn` | `WarnLevel` | `LevelWarn` | | `Error` | `ErrorLevel` | `LevelError` |"
func TestLevelMapping(t *testing.T) {
	t.Parallel()
	zsink, zl, err := zerologSink()
	if err != nil {
		t.Fatalf("zerolog: %v", err)
	}
	ssink, ctor := newSink()
	sl, err := ctor()
	if err != nil {
		t.Fatalf("slog: %v", err)
	}
	type row struct {
		emit         func(gas.Logger, string) gas.LogEvent
		zlevel, slog string
	}
	rows := map[string]row{
		"Trace": {gas.Logger.Trace, zerolog.TraceLevel.String(), slog.LevelDebug.String()},
		"Debug": {gas.Logger.Debug, zerolog.DebugLevel.String(), slog.LevelDebug.String()},
		"Info":  {gas.Logger.Info, zerolog.InfoLevel.String(), slog.LevelInfo.String()},
		"Warn":  {gas.Logger.Warn, zerolog.WarnLevel.String(), slog.LevelWarn.String()},
		"Error": {gas.Logger.Error, zerolog.ErrorLevel.String(), slog.LevelError.String()},
	}
	for name, r := range rows {
		r.emit(zl, "z-"+name).Send()
		r.emit(sl, "s-"+name).Send()
	}
	zlines := map[string]string{}
	for _, rec := range zsink.records(t) {
		zlines[rec["message"].(string)] = rec["level"].(string)
	}
	slines := map[string]string{}
	for _, rec := range ssink.records(t) {
		slines[rec["msg"].(string)] = rec["level"].(string)
	}
	for name, r := range rows {
		if got := zlines["z-"+name]; got != r.zlevel {
			t.Errorf("zerolog %s level = %q, want %q", name, got, r.zlevel)
		}
		if got := slines["s-"+name]; got != r.slog {
			t.Errorf("slog %s level = %q, want %q", name, got, r.slog)
		}
	}
}

// otlpServer collects every batch an OTLP/HTTP JSON sender posts.
type otlpServer struct {
	*httptest.Server
	mu       sync.Mutex
	messages []string
	requests int
	headers  []http.Header
}

func newOTLPServer(t *testing.T, handler func(w http.ResponseWriter)) *otlpServer {
	t.Helper()
	s := &otlpServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			ResourceLogs []struct {
				ScopeLogs []struct {
					LogRecords []struct {
						Body struct {
							StringValue string `json:"stringValue"`
						} `json:"body"`
					} `json:"logRecords"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}
		_ = json.Unmarshal(body, &payload)
		s.mu.Lock()
		s.requests++
		s.headers = append(s.headers, r.Header.Clone())
		for _, rl := range payload.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					s.messages = append(s.messages, lr.Body.StringValue)
				}
			}
		}
		s.mu.Unlock()
		if handler != nil {
			handler(w)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *otlpServer) snapshot() (int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, append([]string(nil), s.messages...)
}

// Docs: guides/logging.mdx#choosing-a-backend
// "| `NewShippingLogger()` | `log/slog` plus HTTP | Writes locally and batches records to an endpoint |"
// Docs: guides/logging.mdx#shipping-logs
// "Records are captured by an `slog.Handler`, batched, and delivered by a background goroutine. The wire format is a pluggable `Marshaler`, with an OTLP/HTTP JSON one included."
func TestShippingLoggerWritesLocallyAndBatches(t *testing.T) {
	t.Parallel()
	srv := newOTLPServer(t, nil)
	local := &logSink{}
	logger, err := gaslog.NewShippingLogger(srv.URL, gaslog.NewOTLPMarshaler(gaslog.WithServiceName("notes")),
		gaslog.WithLocalHandler(slog.NewJSONHandler(local, nil)),
		gaslog.WithHeader("X-API-Key", "k-1"),
		gaslog.WithBatchSize(3),
		gaslog.WithFlushInterval(time.Hour),
	)()
	if err != nil {
		t.Fatalf("shipping logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close(context.Background()) })
	for _, m := range []string{"one", "two", "three"} {
		logger.Info(m).Send()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		n, msgs := srv.snapshot()
		if n >= 1 {
			if n != 1 || strings.Join(msgs, ",") != "one,two,three" {
				t.Fatalf("server got %d requests with %v, want one batch of three", n, msgs)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no batch delivered after reaching the batch size")
		}
		time.Sleep(10 * time.Millisecond)
	}
	srv.mu.Lock()
	h := srv.headers[0]
	srv.mu.Unlock()
	if h.Get("X-API-Key") != "k-1" || !strings.Contains(h.Get("Content-Type"), "json") {
		t.Fatalf("batch headers = %v, want X-API-Key and a JSON content type", h)
	}
	if got := len(local.find(t, "")); got != 3 {
		t.Fatalf("local handler got %d records, want 3", got)
	}
}

type countingMarshaler struct{ calls atomic.Int32 }

func (m *countingMarshaler) Marshal(records []gaslog.Record) ([]byte, error) {
	m.calls.Add(1)
	return []byte("custom"), nil
}
func (m *countingMarshaler) ContentType() string { return "text/x-custom" }

// Docs: guides/logging.mdx#shipping-logs
// "The wire format is a pluggable `Marshaler`"
func TestShippingMarshalerIsPluggable(t *testing.T) {
	t.Parallel()
	var gotType, gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotType.Store(r.Header.Get("Content-Type"))
		gotBody.Store(string(b))
	}))
	t.Cleanup(srv.Close)
	m := &countingMarshaler{}
	logger, err := gaslog.NewShippingLogger(srv.URL, m, gaslog.WithoutLocalHandler())()
	if err != nil {
		t.Fatalf("shipping logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close(context.Background()) })
	logger.Info("x").Send()
	logger.Flush()
	if m.calls.Load() == 0 || gotType.Load() != "text/x-custom" || gotBody.Load() != "custom" {
		t.Fatalf("marshaler calls=%d content-type=%v body=%v", m.calls.Load(), gotType.Load(), gotBody.Load())
	}
}

// Docs: guides/logging.mdx#shipping-logs
// "when the queue is full, records are dropped rather than blocking the code that logged them, and delivery failures go to `WithErrorHandler` rather than surfacing at the call site."
func TestShippingNeverBlocksAndReportsFailures(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	srv := newOTLPServer(t, func(w http.ResponseWriter) {
		<-release
		w.WriteHeader(http.StatusInternalServerError)
	})
	var failures atomic.Int32
	logger, err := gaslog.NewShippingLogger(srv.URL, gaslog.NewOTLPMarshaler(),
		gaslog.WithoutLocalHandler(),
		gaslog.WithBatchSize(1),
		gaslog.WithQueueSize(1),
		gaslog.WithErrorHandler(func(error) { failures.Add(1) }),
	)()
	if err != nil {
		t.Fatalf("shipping logger: %v", err)
	}
	begin := time.Now()
	for range 200 {
		logger.Info("flood").Send()
	}
	if took := time.Since(begin); took > time.Second {
		t.Fatalf("logging 200 records took %v with a stuck endpoint", took)
	}
	unblock()
	_ = logger.Close(context.Background())
	if _, msgs := srv.snapshot(); len(msgs) >= 200 {
		t.Fatalf("server received %d records, want some dropped", len(msgs))
	}
	if failures.Load() == 0 {
		t.Fatal("500 responses never reached WithErrorHandler")
	}
}

// Docs: guides/logging.mdx#shipping-logs
// "The shipping logger implements `gas.Service`, so registering it in the container means `Close` drains buffered records at shutdown. Outside the container, call `Flush()` before exit."
func TestShippingDrainOnShutdownAndFlush(t *testing.T) {
	t.Parallel()
	var _ gas.Service = (*gaslog.ShippingLogger)(nil)

	t.Run("container Close drains", func(t *testing.T) {
		t.Parallel()
		srv := newOTLPServer(t, nil)
		w := gas.NewWorker(gas.WithSingletonService[gas.Logger](gaslog.NewShippingLogger(srv.URL, gaslog.NewOTLPMarshaler(),
			gaslog.WithoutLocalHandler(), gaslog.WithFlushInterval(time.Hour))))
		if err := w.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		w.ServiceContainer().MustResolve[gas.Logger]().Info("drained-at-shutdown").Send()
		if err := w.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown: %v", err)
		}
		if _, msgs := srv.snapshot(); !strings.Contains(strings.Join(msgs, ","), "drained-at-shutdown") {
			t.Fatalf("server got %v after shutdown, want the buffered record", msgs)
		}
	})
	t.Run("Flush", func(t *testing.T) {
		t.Parallel()
		srv := newOTLPServer(t, nil)
		logger, err := gaslog.NewShippingLogger(srv.URL, gaslog.NewOTLPMarshaler(),
			gaslog.WithoutLocalHandler(), gaslog.WithFlushInterval(time.Hour))()
		if err != nil {
			t.Fatalf("shipping logger: %v", err)
		}
		t.Cleanup(func() { _ = logger.Close(context.Background()) })
		logger.Info("flushed").Send()
		logger.Flush()
		if _, msgs := srv.snapshot(); len(msgs) != 1 || msgs[0] != "flushed" {
			t.Fatalf("server got %v after Flush, want [flushed]", msgs)
		}
	})
}

// Docs: guides/logging.mdx#choosing-a-backend
// "Each returns a constructor function, not a logger, so it drops straight into the container."
func TestBackendsReturnConstructors(t *testing.T) {
	t.Parallel()
	for name, ctor := range map[string]any{
		"NewZeroLogLogger":  gaslog.NewZeroLogLogger(),
		"NewSlogLogger":     gaslog.NewSlogLogger(),
		"NewShippingLogger": gaslog.NewShippingLogger("http://127.0.0.1:1", gaslog.NewOTLPMarshaler()),
	} {
		typ := reflect.TypeOf(ctor)
		if typ.Kind() != reflect.Func || typ.NumIn() != 0 || typ.NumOut() != 2 ||
			!typ.Out(0).Implements(reflect.TypeFor[gas.Logger]()) || typ.Out(1) != reflect.TypeFor[error]() {
			t.Errorf("%s returns %v, want func() (gas.Logger implementation, error)", name, typ)
		}
	}
	w := gas.NewWorker(gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if w.ServiceContainer().MustResolve[gas.Logger]() == nil {
		t.Fatal("container resolved a nil logger")
	}
}

type RequestLogger interface{ gas.Logger }

func requestApp(t *testing.T, scoped bool, opts ...gas.RequestLoggerOption) (*gas.App, *logSink) {
	t.Helper()
	sink, logger := newSink()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger { return l.With().Logger() }),
	)
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	if scoped {
		app.Router().UseMiddlewareFunc(gas.RequestLogger[RequestLogger](opts...))
	} else {
		app.Router().UseMiddlewareFunc(gas.RequestLogger[gas.Logger](opts...))
	}
	return app, sink
}

func withID(path, id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(middleware.RequestIDHeader, id)
	return req
}

func lineWith(t *testing.T, sink *logSink, msg string) map[string]any {
	t.Helper()
	lines := sink.find(t, msg)
	if len(lines) != 1 {
		t.Fatalf("found %d %q lines, want 1; log:\n%s", len(lines), msg, sink.String())
	}
	return lines[0]
}

// Docs: guides/logging.mdx#singleton-or-scoped
// "register a separate **scoped** type derived from it, as above, and give that type to the request middleware: each request gets its own logger, and handlers and services that take the scoped type get the request's fields. Loggers are never changed in place, so nothing leaks between requests."
func TestScopedRequestLoggerPerRequest(t *testing.T) {
	t.Parallel()
	app, sink := requestApp(t, true)
	var seen []RequestLogger
	var base gas.Logger
	app.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context, l RequestLogger, b gas.Logger) error {
		seen = append(seen, l)
		base = b
		l.Info("handled " + ctx.Query("n")).Send()
		return ctx.NoContent()
	})
	start(t, app)
	app.Handler().ServeHTTP(httptest.NewRecorder(), withID("/x?n=1", "rid-1"))
	app.Handler().ServeHTTP(httptest.NewRecorder(), withID("/x?n=2", "rid-2"))
	base.Info("after requests").Send()
	if len(seen) != 2 || seen[0] == seen[1] {
		t.Fatal("two requests shared one request logger")
	}
	if l := lineWith(t, sink, "handled 1"); !hasValue(l, "rid-1") || hasValue(l, "rid-2") {
		t.Fatalf("first request line %v, want only rid-1", l)
	}
	if l := lineWith(t, sink, "handled 2"); !hasValue(l, "rid-2") || hasValue(l, "rid-1") {
		t.Fatalf("second request line %v, want only rid-2", l)
	}
	if l := lineWith(t, sink, "after requests"); hasValue(l, "rid-1") || hasValue(l, "rid-2") {
		t.Fatalf("singleton picked up request fields: %v", l)
	}
}

// Docs: guides/logging.mdx#logging
// "Every event ends with `Send()`. Fields available on events and on `With()`: `Str`, `Int`, `Int64`, `Float64`, `Bool`, `Err`, `Duration`, and `Any`."
func TestFluentFields(t *testing.T) {
	t.Parallel()
	sink, ctor := newSink()
	l, err := ctor()
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	pending := l.Info("not yet")
	if strings.Contains(sink.String(), "not yet") {
		t.Fatal("event emitted before Send")
	}
	pending.Send()
	if !strings.Contains(sink.String(), "not yet") {
		t.Fatal("event not emitted by Send")
	}

	l.Info("event fields").Str("s", "v").Int("i", 1).Int64("i64", 2).Float64("f", 1.5).
		Bool("b", true).Err("e", errors.New("bad")).Duration("d", time.Second).Any("a", "any").Send()
	sub := l.With().Str("s", "v").Int("i", 1).Int64("i64", 2).Float64("f", 1.5).
		Bool("b", true).Err("e", errors.New("bad")).Duration("d", time.Second).Any("a", "any").Logger()
	sub.Info("context fields").Send()
	for _, msg := range []string{"event fields", "context fields"} {
		line := lineWith(t, sink, msg)
		for _, key := range []string{"s", "i", "i64", "f", "b", "e", "d", "a"} {
			if _, ok := line[key]; !ok {
				t.Errorf("%q line %v lacks field %q", msg, line, key)
			}
		}
	}
}

// Docs: guides/logging.mdx#request-scoped-fields
// "`With()` branches into a new logger and leaves the original alone. ... middleware derives that logger and stores it on the request context with `gas.WithLogger`; handlers and services log through `gas.LoggerFromContext(r.Context())`."
func TestRequestScopedFieldsMiddleware(t *testing.T) {
	t.Parallel()
	sink, logger := newSink()
	app := gas.NewApp(gas.WithSingletonService[gas.Logger](logger))
	app.Router().UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			base := gas.LoggerFromContext(r.Context())
			if base == nil {
				base = gas.MustResolveFromRequestScope[gas.Logger](r)
			}
			l := base.With().Str("user_id", "u-123").Logger()
			next.ServeHTTP(w, r.WithContext(gas.WithLogger(r.Context(), l)))
		})
	})
	var root gas.Logger
	app.Router().Handle(nil, http.MethodGet, "/x", func(w http.ResponseWriter, r *http.Request) {
		root = gas.MustResolveFromRequestScope[gas.Logger](r)
		gas.LoggerFromContext(r.Context()).Info("note created").Send()
	})
	start(t, app)
	app.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	root.Info("root logger").Send()
	if l := lineWith(t, sink, "note created"); l["user_id"] != "u-123" {
		t.Fatalf("handler line %v lacks user_id", l)
	}
	if l := lineWith(t, sink, "root logger"); l["user_id"] != nil {
		t.Fatalf("With changed the original logger: %v", l)
	}
}

type audit struct{ logger RequestLogger }

// Docs: guides/logging.mdx#request-logging
// "Logs method, path, status, bytes written, duration, and remote address for every request, at error level for statuses at 400 and above. ... When its type is scoped, as `RequestLogger` above, it also puts that logger in the request scope, so handlers and services that take `RequestLogger` as a dependency get it."
func TestRequestLoggerScopedType(t *testing.T) {
	t.Parallel()
	sink, logger := newSink()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger { return l.With().Logger() }),
		gas.WithScopedService[*audit](func(l RequestLogger) *audit { return &audit{logger: l} }),
	)
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[RequestLogger]())
	app.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context, a *audit) error {
		a.logger.Info("from a scoped service").Send()
		return ctx.Text(http.StatusBadRequest, "bad")
	})
	start(t, app)
	req := withID("/x", "rid-svc")
	req.RemoteAddr = "192.0.2.9:9999"
	app.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if l := lineWith(t, sink, "from a scoped service"); !hasValue(l, "rid-svc") {
		t.Fatalf("service line %v lacks the request ID", l)
	}
	var reqLine map[string]any
	for _, rec := range sink.records(t) {
		if hasValue(rec, "/x") && hasValue(rec, "400") {
			reqLine = rec
		}
	}
	if reqLine == nil {
		t.Fatalf("no request log line; log:\n%s", sink.String())
	}
	for _, want := range []string{http.MethodGet, "/x", "400", "3", "192.0.2.9:9999"} {
		if !hasValue(reqLine, want) {
			t.Errorf("request line %v lacks %q", reqLine, want)
		}
	}
	if reqLine["level"] != "ERROR" {
		t.Errorf("400 logged at %v, want ERROR", reqLine["level"])
	}
}

// Docs: guides/logging.mdx#request-logging
// "Given the singleton `gas.Logger` instead, the singleton is left unchanged and the request logger is only on the context. If chi's `RequestID` middleware is mounted upstream, that logger carries the request id; pass `gas.WithRequestLoggerAppendRequestID(false)` to turn that off."
func TestRequestLoggerSingletonAndRequestIDOption(t *testing.T) {
	t.Parallel()
	t.Run("singleton type", func(t *testing.T) {
		t.Parallel()
		app, sink := requestApp(t, false)
		app.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context, l gas.Logger) error {
			gas.LoggerFromContext(ctx).Info("ctx logger").Send()
			l.Info("injected singleton").Send()
			return ctx.NoContent()
		})
		start(t, app)
		app.Handler().ServeHTTP(httptest.NewRecorder(), withID("/x", "rid-s"))
		if l := lineWith(t, sink, "ctx logger"); !hasValue(l, "rid-s") {
			t.Fatalf("context logger line %v lacks the request ID", l)
		}
		if l := lineWith(t, sink, "injected singleton"); hasValue(l, "rid-s") {
			t.Fatalf("singleton was changed: %v", l)
		}
	})
	t.Run("append request ID off", func(t *testing.T) {
		t.Parallel()
		app, sink := requestApp(t, true, gas.WithRequestLoggerAppendRequestID(false))
		app.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context) error {
			gas.LoggerFromContext(ctx).Info("no id").Send()
			return ctx.NoContent()
		})
		start(t, app)
		app.Handler().ServeHTTP(httptest.NewRecorder(), withID("/x", "rid-off"))
		if l := lineWith(t, sink, "no id"); hasValue(l, "rid-off") {
			t.Fatalf("request ID appended although turned off: %v", l)
		}
	})
}

// Docs: guides/logging.mdx#context-propagation
// "`gas.WithLogger(ctx, logger)` puts a logger on a context and `gas.LoggerFromContext(ctx)` takes it back out, returning nil when absent."
func TestLoggerContextPropagation(t *testing.T) {
	t.Parallel()
	if got := gas.LoggerFromContext(context.Background()); got != nil {
		t.Fatalf("LoggerFromContext on an empty context = %v, want nil", got)
	}
	l := &gas.NopLogger{}
	if got := gas.LoggerFromContext(gas.WithLogger(context.Background(), l)); got != l {
		t.Fatalf("LoggerFromContext = %v, want the stored logger", got)
	}
}

// Docs: guides/logging.mdx#levels
// "If no logger is registered at all, Gas logs its own lifecycle messages through a slog-backed fallback and warns once, so startup diagnostics are never silently lost."
// Assumption: the fallback writes through the default slog logger or to
// stderr; both are captured here.
func TestFallbackLoggerWarnsOnce(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origErr := os.Stderr
	os.Stderr = w

	app := gas.NewApp()
	startErr := app.Start(t.Context())
	_ = app.Stop(context.Background())

	os.Stderr = origErr
	_ = w.Close()
	stderr, _ := io.ReadAll(r)
	if startErr != nil {
		t.Fatalf("start: %v", startErr)
	}
	mu.Lock()
	out := buf.String() + string(stderr)
	mu.Unlock()

	if !strings.Contains(out, "level=INFO") {
		t.Fatalf("no lifecycle messages through the fallback:\n%s", out)
	}
	if n := strings.Count(out, "level=WARN"); n != 1 {
		t.Fatalf("fallback warned %d times over Start and Stop, want once:\n%s", n, out)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// Docs: guides/logging.mdx#levels
// "The fallback is not registered in the container: services that need a `gas.Logger` still need one registered."
func TestFallbackNotInContainer(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(gas.WithSingletonService[*svc[kA]](func(gas.Logger) *svc[kA] { return &svc[kA]{} }))
	if err := w.Start(t.Context()); !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("Start = %v, want ErrNotRegistered for gas.Logger", err)
	}
}
