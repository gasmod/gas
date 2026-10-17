package gas

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
