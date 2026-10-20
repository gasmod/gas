package gas

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
)

// lockedBuffer is a bytes.Buffer safe for concurrent writes from a
// slog.Handler.
type lockedBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) lines() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err == nil {
			out = append(out, rec)
		}
	}
	return out
}

// countKey reports how many times key appears as a field in one JSON log
// line. slog's JSON handler writes a repeated attribute twice.
func countKey(line, key string) int {
	return strings.Count(line, `"`+key+`":`)
}

// newRequestLoggerApp builds an App with a singleton logger writing JSON to
// buf, chi's RequestID and RequestLogger as middleware, and a GET /hi
// handler that logs through the request's logger.
func newRequestLoggerApp(t *testing.T, buf *lockedBuffer) *App {
	t.Helper()

	shared := Logger(newSlogLogger(slog.New(slog.NewJSONHandler(buf, nil))))
	app := NewApp(WithServiceInstance[Logger](shared))
	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(RequestLogger[Logger]())
	app.Router().Handle(nil, http.MethodGet, "/hi", func(w http.ResponseWriter, r *http.Request) {
		l := LoggerFromContext(r.Context())
		if l == nil {
			http.Error(w, "no request logger", http.StatusInternalServerError)
			return
		}
		l.Info("handler").Send()
		w.WriteHeader(http.StatusOK)
	})
	return app
}

// RequestLogger must not mutate the shared singleton logger: concurrent
// requests must not race, and each request's lines carry exactly one
// request_id, its own.
func TestRequestLogger_SharedLoggerIsNotMutated(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	app := newRequestLoggerApp(t, buf)

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			rr := httptest.NewRecorder()
			app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/hi", nil))
		})
	}
	wg.Wait()

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/hi", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /hi = %d %q, want 200", rr.Code, rr.Body.String())
	}

	buf.mu.Lock()
	raw := strings.Split(strings.TrimSpace(buf.buf.String()), "\n")
	buf.mu.Unlock()
	if len(raw) != 42 {
		t.Fatalf("log lines = %d, want 42 (a handler and a request line per request)", len(raw))
	}
	for _, line := range raw {
		if n := countKey(line, "request_id"); n != 1 {
			t.Errorf("line has %d request_id fields, want 1: %s", n, line)
		}
	}
}

// The handler's logger, from LoggerFromContext, carries the same
// request_id as the middleware's request line.
func TestRequestLogger_HandlerLoggerCarriesRequestID(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	app := newRequestLoggerApp(t, buf)

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/hi", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /hi = %d %q, want 200", rr.Code, rr.Body.String())
	}

	lines := buf.lines()
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want 2", len(lines))
	}
	handlerID, requestID := lines[0]["request_id"], lines[1]["request_id"]
	if handlerID == nil || handlerID != requestID {
		t.Errorf("handler request_id = %v, request line request_id = %v, want equal and set",
			handlerID, requestID)
	}
}

// scopedRequestLogger is the per-request logger type of the documented
// pattern: registered scoped, built from the singleton with With.
type scopedRequestLogger interface{ Logger }

// newScopedPatternApp registers a singleton Logger writing JSON to buf and a
// scoped scopedRequestLogger derived from it, and mounts chi's RequestID and
// RequestLogger[scopedRequestLogger].
func newScopedPatternApp(t *testing.T, buf *lockedBuffer) *App {
	t.Helper()

	shared := Logger(newSlogLogger(slog.New(slog.NewJSONHandler(buf, nil))))
	app := NewApp(
		WithServiceInstance[Logger](shared),
		WithScopedService[scopedRequestLogger](func(l Logger) scopedRequestLogger {
			return l.With().Logger()
		}),
	)
	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(RequestLogger[scopedRequestLogger]())
	return app
}

// With a scoped request-logger type, a handler that takes it as a DI
// parameter gets the logger carrying the request's request_id, for every
// one of many concurrent requests.
func TestRequestLogger_ScopedTypeInjectedWithRequestID(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	app := newScopedPatternApp(t, buf)
	app.Router().Get(nil, "/hi", func(_ Context, l scopedRequestLogger) error {
		l.Info("handler").Send()
		return nil
	})

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			rr := httptest.NewRecorder()
			app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/hi", nil))
			if rr.Code != http.StatusOK {
				t.Errorf("GET /hi = %d %q, want 200", rr.Code, rr.Body.String())
			}
		})
	}
	wg.Wait()

	lines := buf.lines()
	if len(lines) != 40 {
		t.Fatalf("log lines = %d, want 40", len(lines))
	}
	handlerIDs := map[any]int{}
	requestIDs := map[any]int{}
	for _, l := range lines {
		id := l["request_id"]
		if id == nil {
			t.Errorf("line without request_id: %v", l)
			continue
		}
		if l["msg"] == "handler" {
			handlerIDs[id]++
		} else {
			requestIDs[id]++
		}
	}
	if len(handlerIDs) != 20 {
		t.Errorf("distinct handler request_ids = %d, want 20", len(handlerIDs))
	}
	for id := range handlerIDs {
		if requestIDs[id] != 1 {
			t.Errorf("handler request_id %v has %d request lines, want 1", id, requestIDs[id])
		}
	}
	buf.mu.Lock()
	raw := buf.buf.String()
	buf.mu.Unlock()
	for line := range strings.SplitSeq(strings.TrimSpace(raw), "\n") {
		if n := countKey(line, "request_id"); n != 1 {
			t.Errorf("line has %d request_id fields, want 1: %s", n, line)
		}
	}
}

// With the singleton Logger as T, the request scope still resolves the
// singleton itself: the request logger lives only on the context.
func TestRequestLogger_SingletonTypeIsNotShadowed(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	shared := Logger(newSlogLogger(slog.New(slog.NewJSONHandler(buf, nil))))
	app := NewApp(WithServiceInstance[Logger](shared))
	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(RequestLogger[Logger]())
	app.Router().Get(nil, "/hi", func(_ Context, l Logger) error {
		if l != shared {
			return NewError(http.StatusInternalServerError, "shadowed", "singleton shadowed")
		}
		return nil
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/hi", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /hi = %d %q, want 200", rr.Code, rr.Body.String())
	}
}

// closingRequestLogger is a scoped request logger that is also a Service,
// so the scope closes it when the request ends.
type closingRequestLogger struct {
	Logger
	closed *atomic.Bool
}

func (l *closingRequestLogger) Name() string                { return "closing-request-logger" }
func (l *closingRequestLogger) Init(context.Context) error  { return nil }
func (l *closingRequestLogger) Close(context.Context) error { l.closed.Store(true); return nil }

// A scoped request logger that is a Service is not replaced, so the scope
// still closes the instance it built.
func TestRequestLogger_ScopedServiceIsNotReplaced(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	closed := &atomic.Bool{}
	shared := Logger(newSlogLogger(slog.New(slog.NewJSONHandler(buf, nil))))
	app := NewApp(
		WithServiceInstance[Logger](shared),
		WithScopedService[scopedRequestLogger](func(l Logger) scopedRequestLogger {
			return &closingRequestLogger{Logger: l.With().Logger(), closed: closed}
		}),
	)
	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.Router().UseMiddlewareFunc(RequestLogger[scopedRequestLogger]())
	app.Router().Get(nil, "/hi", func(_ Context, l scopedRequestLogger) error {
		if _, ok := l.(*closingRequestLogger); !ok {
			return NewError(http.StatusInternalServerError, "replaced", "service replaced")
		}
		return nil
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/hi", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /hi = %d %q, want 200", rr.Code, rr.Body.String())
	}
	if !closed.Load() {
		t.Error("the scoped request logger was not closed at the end of the request")
	}
}

// newStatusLoggerApp builds an App with a singleton logger writing JSON to
// buf, chi's RequestID, and RequestLogger[Logger](opts...), serving
// GET /status/{code} with that status.
func newStatusLoggerApp(t *testing.T, buf *lockedBuffer, opts ...RequestLoggerOption) *App {
	t.Helper()

	shared := Logger(newSlogLogger(slog.New(slog.NewJSONHandler(buf, nil))))
	app := NewApp(WithServiceInstance[Logger](shared))
	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(RequestLogger[Logger](opts...))
	app.Router().
		Handle(nil, http.MethodGet, "/status/{code}", func(w http.ResponseWriter, r *http.Request) {
			code, err := strconv.Atoi(r.PathValue("code"))
			if err != nil {
				t.Errorf("bad status code %q", r.PathValue("code"))
				return
			}
			w.WriteHeader(code)
		})
	return app
}

// The request line is logged at error level for a status >= 400 and at info
// level below it, with the status field set.
func TestRequestLogger_LevelByStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		level string
		code  int
	}{
		{"INFO", http.StatusOK},
		{"INFO", http.StatusFound},
		{"ERROR", http.StatusBadRequest},
		{"ERROR", http.StatusNotFound},
		{"ERROR", http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.code), func(t *testing.T) {
			t.Parallel()

			buf := &lockedBuffer{}
			app := newStatusLoggerApp(t, buf)
			rr := httptest.NewRecorder()
			app.Router().
				ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/status/"+strconv.Itoa(tt.code), nil))

			lines := buf.lines()
			if len(lines) != 1 {
				t.Fatalf("log lines = %d, want 1", len(lines))
			}
			if got := lines[0]["level"]; got != tt.level {
				t.Errorf("level = %v, want %s", got, tt.level)
			}
			if got := lines[0]["status"]; got != float64(tt.code) {
				t.Errorf("status = %v, want %d", got, tt.code)
			}
		})
	}
}

// WithRequestLoggerAppendRequestID(false) leaves request_id off the request
// line.
func TestRequestLogger_AppendRequestIDDisabled(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	app := newStatusLoggerApp(t, buf, WithRequestLoggerAppendRequestID(false))
	app.Router().
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/status/200", nil))

	lines := buf.lines()
	if len(lines) != 1 {
		t.Fatalf("log lines = %d, want 1", len(lines))
	}
	if id, ok := lines[0]["request_id"]; ok {
		t.Errorf("request_id = %v, want absent", id)
	}
}

// WithRequestLoggerBuilder runs after request_id is appended and its logger
// is the one the request line is written through.
func TestRequestLogger_Builder(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	app := newStatusLoggerApp(
		t,
		buf,
		WithRequestLoggerBuilder(func(l Logger, r *http.Request) Logger {
			return l.With().Str("tenant", r.Header.Get("X-Tenant")).Logger()
		}),
	)
	req := httptest.NewRequest(http.MethodGet, "/status/200", nil)
	req.Header.Set("X-Tenant", "acme")
	app.Router().ServeHTTP(httptest.NewRecorder(), req)

	lines := buf.lines()
	if len(lines) != 1 {
		t.Fatalf("log lines = %d, want 1", len(lines))
	}
	if got := lines[0]["tenant"]; got != "acme" {
		t.Errorf("tenant = %v, want acme", got)
	}
	if got := lines[0]["request_id"]; got == nil || got == "" {
		t.Errorf("request_id = %v, want set alongside the builder's fields", got)
	}
}
